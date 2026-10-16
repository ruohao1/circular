package artifacts_test

import (
	"archive/tar"
	"bytes"
	"github.com/ruohao1/circular/internal/artifacts"
	"os"
	"path/filepath"
	"testing"
)

func TestDelegationRestoreValidatesSymlinkGraph(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []tar.Header
		valid   bool
		read    string
	}{
		{"escape", []tar.Header{
			{Name: "b", Typeflag: tar.TypeDir}, {Name: "sub", Typeflag: tar.TypeDir},
			{Name: "sub/a", Typeflag: tar.TypeSymlink, Linkname: "../b"},
			{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "sub/a/../../outside"},
		}, false, "escape"},
		{"forward-escape", []tar.Header{
			{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "sub/a/../../outside"},
			{Name: "sub/a", Typeflag: tar.TypeSymlink, Linkname: "../b"},
			{Name: "b", Typeflag: tar.TypeDir},
		}, false, "escape"},
		{"cycle", []tar.Header{
			{Name: "first", Typeflag: tar.TypeSymlink, Linkname: "second"},
			{Name: "second", Typeflag: tar.TypeSymlink, Linkname: "first"},
		}, false, "first"},
		{"safe-forward-chain", []tar.Header{
			{Name: "first", Typeflag: tar.TypeSymlink, Linkname: "second"},
			{Name: "second", Typeflag: tar.TypeSymlink, Linkname: "file"},
			{Name: "file", Typeflag: tar.TypeReg},
		}, true, "first"},
		{"safe-parent-traversal", []tar.Header{
			{Name: "b", Typeflag: tar.TypeDir}, {Name: "file", Typeflag: tar.TypeReg},
			{Name: "sub/a", Typeflag: tar.TypeSymlink, Linkname: "../b"},
			{Name: "safe", Typeflag: tar.TypeSymlink, Linkname: "sub/a/../file"},
		}, true, "safe"},
		{"internal-dangling", []tar.Header{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "missing"}}, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var data bytes.Buffer
			writer := tar.NewWriter(&data)
			for _, header := range test.headers {
				header.Mode = 0755
				if err := writer.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "outside"), []byte("outside sentinel"), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "restored")
			err := artifacts.RestoreArchive(t.Context(), bytes.NewReader(data.Bytes()), destination)
			if test.valid {
				if err != nil {
					t.Fatal("safe graph rejected", err)
				}
				if test.read != "" {
					if _, err := os.ReadFile(filepath.Join(destination, test.read)); err != nil {
						t.Fatal("safe link changed", err)
					}
				}
			} else {
				if err == nil {
					data, _ := os.ReadFile(filepath.Join(destination, test.read))
					t.Fatalf("unsafe graph accepted; link read %q", data)
				}
				if _, err := os.Lstat(destination); !os.IsNotExist(err) {
					t.Fatal("partial restore survived", err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(root, "outside")); err != nil || string(data) != "outside sentinel" {
				t.Fatal("outside file changed", err)
			}
		})
	}
}

func TestDelegationRestoreRejectsEscapes(t *testing.T) {
	for _, headers := range [][]tar.Header{
		{{Name: "/absolute", Typeflag: tar.TypeReg}}, {{Name: "../escape", Typeflag: tar.TypeReg}}, {{Name: "a/../../escape", Typeflag: tar.TypeReg}},
		{{Name: ".git/config", Typeflag: tar.TypeReg}}, {{Name: "a/.git/config", Typeflag: tar.TypeReg}},
		{{Name: "same", Typeflag: tar.TypeReg}, {Name: "same", Typeflag: tar.TypeReg}},
		{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "../escape"}}, {{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}},
		{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: ".git"}}, {{Name: "device", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}},
		{{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "a/.git/../safe"}},
		{{Name: "dir", Typeflag: tar.TypeSymlink, Linkname: "safe"}, {Name: "dir/file", Typeflag: tar.TypeReg}},
		{{Name: "link", Typeflag: tar.TypeLink, Linkname: "missing"}},
	} {
		var data bytes.Buffer
		writer := tar.NewWriter(&data)
		for _, header := range headers {
			header.Mode = 0600
			if err := writer.WriteHeader(&header); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		destination := filepath.Join(root, "restored")
		if err := artifacts.RestoreArchive(t.Context(), bytes.NewReader(data.Bytes()), destination); err == nil {
			t.Fatalf("accepted %+v", headers)
		}
		if _, err := os.Lstat(destination); !os.IsNotExist(err) {
			t.Fatal("partial restore survived", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
			t.Fatal("escaped", err)
		}
	}
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	if err := w.WriteHeader(&tar.Header{Name: "file", Typeflag: tar.TypeReg, Size: 1024, Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	w.Write(make([]byte, 1024))
	w.Close()
	for _, cut := range []int{512, 1024, 1536} {
		if err := artifacts.RestoreArchive(t.Context(), bytes.NewReader(data.Bytes()[:cut]), filepath.Join(t.TempDir(), "output")); err == nil {
			t.Fatal("truncated archive accepted", cut)
		}
	}
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "preserve"), []byte("keep"), 0600)
	if err := artifacts.RestoreArchive(t.Context(), bytes.NewReader(data.Bytes()), root); err == nil {
		t.Fatal("overlaid existing directory")
	}
}
