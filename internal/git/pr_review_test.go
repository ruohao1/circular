package git_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
)

func TestReviewPinsPRObjectsWhenDefaultBranchAdvances(t *testing.T) {
	root := t.TempDir()
	source := sourceRepository(t, root)
	base := string(gitCommand(t, source, "rev-parse", "HEAD"))
	gitCommand(t, source, "checkout", "-b", "review-me")
	putFile(t, filepath.Join(source, "bug.txt"), "introduced on the PR\n")
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "-m", "PR change")
	head := string(gitCommand(t, source, "rev-parse", "HEAD"))
	gitCommand(t, source, "checkout", "main")
	putFile(t, filepath.Join(source, "upstream.txt"), "unrelated upstream\n")
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "-m", "upstream moves")
	local := localGit(t, root)
	repo, run := uuid.New(), uuid.New()
	identity := prreviews.PRIdentity{Number: 1, BaseRef: "main", HeadRef: "review-me", BaseSHA: base, HeadSHA: head}
	captured, err := local.PreparePRReview(t.Context(), run, repo, source, identity)
	if err != nil {
		t.Fatal(err)
	}
	if captured.MergeBaseSHA != base || !bytes.Contains(captured.Diff, []byte("introduced on the PR")) || bytes.Contains(captured.Diff, []byte("unrelated upstream")) {
		t.Fatal("comparison followed moving default")
	}
	gitCommand(t, source, "branch", "-f", "review-me", "main")
	gitCommand(t, source, "branch", "-D", "review-me")
	again, err := local.PreparePRReview(t.Context(), run, repo, source, identity)
	if err != nil || again.DiffSHA256 != captured.DiffSHA256 {
		t.Fatal("pinned source lost", err)
	}
	w, err := local.Provision(t.Context(), run, captured.RepositoryPath, head)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(gitCommand(t, w.Path, "rev-parse", "HEAD")); got != head {
		t.Fatal("wrong source", got)
	}
	advanced := identity
	advanced.BaseSHA = string(gitCommand(t, source, "rev-parse", "main"))
	newer, err := local.PreparePRReview(t.Context(), uuid.New(), repo, source, advanced)
	if err != nil || newer.MergeBaseSHA != base || newer.DiffSHA256 != captured.DiffSHA256 {
		t.Fatal("changed base lost the merge-base comparison", err)
	}
	// A pre-existing pin is durable identity, not a hint to overwrite.
	gitCommand(t, captured.RepositoryPath, "update-ref", "refs/circular/reviews/"+run.String()+"/head", base)
	if _, err = local.PreparePRReview(t.Context(), run, repo, source, identity); !errors.Is(err, prreviews.ErrSourceInvalid) {
		t.Fatal("conflicting pin accepted", err)
	}
	identity.HeadSHA = strings.Repeat("e", 40)
	if _, err = local.PreparePRReview(t.Context(), uuid.New(), repo, source, identity); !errors.Is(err, prreviews.ErrSourceInvalid) {
		t.Fatal("unavailable commit substituted", err)
	}
}

func TestReviewManifestHandlesRenamesDeletesBinaryAndUnusualNames(t *testing.T) {
	root := t.TempDir()
	source := sourceRepository(t, root)
	putFile(t, filepath.Join(source, "gone.txt"), "one\ntwo\n")
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "-m", "base")
	base := string(gitCommand(t, source, "rev-parse", "HEAD"))
	gitCommand(t, source, "mv", "README.md", "renamed é\nfile.txt")
	if err := os.Remove(filepath.Join(source, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(source, "binary"), "\x00\x01\xff")
	if err := os.Symlink("/outside/does-not-exist", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "update-index", "--add", "--cacheinfo", "160000,"+base+",module")
	gitCommand(t, source, "commit", "-m", "change")
	head := string(gitCommand(t, source, "rev-parse", "HEAD"))
	v, err := localGit(t, root).PreparePRReview(t.Context(), uuid.New(), uuid.New(), source, prreviews.PRIdentity{Number: 1, BaseSHA: base, HeadSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Files) != 5 || len(v.Limitations) != 2 {
		t.Fatalf("files/limits: %+v %+v", v.Files, v.Limitations)
	}
	for _, f := range v.Files {
		switch f.NewPath {
		case "renamed é\nfile.txt":
			if f.OldPath != "README.md" || f.Status != "renamed" || f.HeadLines != 1 {
				t.Fatal(f)
			}
		case "binary":
			if !f.Binary {
				t.Fatal(f)
			}
		case "module":
			if !f.Submodule {
				t.Fatal(f)
			}
		case "link":
			if f.HeadLines != 1 {
				t.Fatal(f)
			}
		}
		if f.OldPath == "gone.txt" && (f.Status != "deleted" || f.BaseLines != 2 || len(f.BaseChanged) != 1) {
			t.Fatal(f)
		}
	}
}

func TestReviewDeletedDefaultAndMovedPRRefNeverReplaceCapturedObjects(t *testing.T) {
	root := t.TempDir()
	source := sourceRepository(t, root)
	base := string(gitCommand(t, source, "rev-parse", "HEAD"))
	gitCommand(t, source, "checkout", "-b", "review-head")
	putFile(t, filepath.Join(source, "README.md"), "PR\n")
	gitCommand(t, source, "commit", "-am", "PR")
	head := string(gitCommand(t, source, "rev-parse", "HEAD"))
	gitCommand(t, source, "update-ref", "refs/pull/1/head", head)
	gitCommand(t, source, "symbolic-ref", "HEAD", "refs/heads/deleted-default")
	local := localGit(t, root)
	repo := uuid.New()
	identity := prreviews.PRIdentity{Number: 1, BaseSHA: base, HeadSHA: head}
	captured, err := local.PreparePRReview(t.Context(), uuid.New(), repo, source, identity)
	if err != nil || captured.MergeBaseSHA != base {
		t.Fatal("deleted default prevented exact source capture", err)
	}
	identity.HeadSHA = strings.Repeat("e", 40)
	if _, err := local.PreparePRReview(t.Context(), uuid.New(), repo, source, identity); !errors.Is(err, prreviews.ErrSourceInvalid) {
		t.Fatal("current PR ref replaced unavailable captured head", err)
	}
}

func TestReviewHugeOriginalBlobRecordsIncompleteCoverage(t *testing.T) {
	root := t.TempDir()
	source := sourceRepository(t, root)
	data := strings.Repeat("unchanged\n", (20<<20)/10+1)
	putFile(t, filepath.Join(source, "large.txt"), data)
	gitCommand(t, source, "add", ".")
	gitCommand(t, source, "commit", "-m", "large base")
	base := string(gitCommand(t, source, "rev-parse", "HEAD"))
	putFile(t, filepath.Join(source, "large.txt"), data+"added\n")
	gitCommand(t, source, "commit", "-am", "tiny change")
	head := string(gitCommand(t, source, "rev-parse", "HEAD"))
	captured, err := localGit(t, root).PreparePRReview(t.Context(), uuid.New(), uuid.New(), source, prreviews.PRIdentity{Number: 1, BaseSHA: base, HeadSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Diff) > 1000 || len(captured.Limitations) != 1 || !captured.Files[0].Binary {
		t.Fatal("large blob silently treated as fully reviewed", captured.Limitations)
	}
}
