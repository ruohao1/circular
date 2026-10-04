package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ruohao1/circular/internal/prreviews"
)

func reviewFiles(raw []byte) ([]prreviews.ChangedFile, error) {
	files := []prreviews.ChangedFile{}
	if len(raw) == 0 {
		return files, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, prreviews.ErrSourceInvalid
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	for i := 0; i < len(parts); {
		status := string(parts[i])
		i++
		if len(status) == 0 || i >= len(parts) {
			return nil, prreviews.ErrSourceInvalid
		}
		name := string(parts[i])
		i++
		if !prreviews.SafePath(name) {
			return nil, prreviews.ErrSourceInvalid
		}
		f := prreviews.ChangedFile{OldPath: name, NewPath: name, BaseChanged: []prreviews.LineRange{}, HeadChanged: []prreviews.LineRange{}}
		switch status {
		case "A":
			f.Status = "added"
			f.OldPath = ""
		case "D":
			f.Status = "deleted"
			f.NewPath = ""
		case "M", "T":
			f.Status = "modified"
		default:
			if status[0] != 'R' || i >= len(parts) {
				return nil, prreviews.ErrSourceInvalid
			}
			score, err := strconv.Atoi(status[1:])
			if err != nil || score < 0 || score > 100 {
				return nil, prreviews.ErrSourceInvalid
			}
			f.Status = "renamed"
			f.NewPath = string(parts[i])
			i++
			if !prreviews.SafePath(f.NewPath) {
				return nil, prreviews.ErrSourceInvalid
			}
		}
		files = append(files, f)
		if len(files) > prreviews.MaxFiles {
			return nil, prreviews.ErrSourceInvalid
		}
	}
	return files, nil
}

type reviewObject struct{ mode, sha string }

func reviewTree(raw []byte) (map[string]reviewObject, error) {
	result := map[string]reviewObject{}
	if len(raw) == 0 {
		return result, nil
	}
	if raw[len(raw)-1] != 0 {
		return nil, prreviews.ErrSourceInvalid
	}
	for _, record := range bytes.Split(raw[:len(raw)-1], []byte{0}) {
		meta, name, ok := strings.Cut(string(record), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || !prreviews.SafePath(name) || !prreviews.CommitPattern.MatchString(fields[2]) {
			return nil, prreviews.ErrSourceInvalid
		}
		if fields[0] != "100644" && fields[0] != "100755" && fields[0] != "120000" && fields[0] != "160000" {
			return nil, prreviews.ErrSourceInvalid
		}
		if _, exists := result[name]; exists {
			return nil, prreviews.ErrSourceInvalid
		}
		result[name] = reviewObject{fields[0], fields[2]}
	}
	return result, nil
}

var reviewHunk = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

func reviewRanges(raw []byte) (base, head []prreviews.LineRange, err error) {
	base = []prreviews.LineRange{}
	head = []prreviews.LineRange{}
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if !bytes.HasPrefix(line, []byte("@@")) {
			continue
		}
		match := reviewHunk.FindSubmatch(line)
		if match == nil {
			return nil, nil, prreviews.ErrSourceInvalid
		}
		for side := range 2 {
			index := 1 + side*2
			start, e := strconv.Atoi(string(match[index]))
			if e != nil {
				return nil, nil, prreviews.ErrSourceInvalid
			}
			count := 1
			if len(match[index+1]) > 0 {
				count, e = strconv.Atoi(string(match[index+1]))
				if e != nil {
					return nil, nil, prreviews.ErrSourceInvalid
				}
			}
			if count == 0 {
				continue
			}
			if start < 1 || count > int(^uint(0)>>1)-start {
				return nil, nil, prreviews.ErrSourceInvalid
			}
			r := prreviews.LineRange{Start: start, End: start + count - 1}
			if side == 0 {
				base = append(base, r)
			} else {
				head = append(head, r)
			}
		}
	}
	return
}

func (l *Local) reviewManifest(ctx context.Context, repository string, env map[string]string, base, head string, files []prreviews.ChangedFile) ([]string, error) {
	run := func(limit int, args ...string) ([]byte, error) {
		out, code, err := l.runBounded(ctx, env, limit, append([]string{"-C", repository}, args...)...)
		if err != nil || code != 0 {
			return nil, errors.Join(prreviews.ErrSourceInvalid, err)
		}
		return out, nil
	}
	trees := make([]map[string]reviewObject, 2)
	for i, sha := range []string{base, head} {
		raw, err := run(prreviews.MaxContextBytes, "ls-tree", "-rz", "--full-tree", sha)
		if err != nil {
			return nil, err
		}
		trees[i], err = reviewTree(raw)
		if err != nil {
			return nil, err
		}
	}
	empty, err := run(512, "hash-object", "-w", "--stdin")
	if err != nil {
		return nil, err
	}
	emptySHA := strings.TrimSpace(string(empty))
	if !prreviews.CommitPattern.MatchString(emptySHA) {
		return nil, prreviews.ErrSourceInvalid
	}
	limitations := []string{}
	for i := range files {
		f := &files[i]
		objects := []reviewObject{{sha: emptySHA}, {sha: emptySHA}}
		for side, name := range []string{f.OldPath, f.NewPath} {
			if name == "" {
				continue
			}
			object, ok := trees[side][name]
			if !ok {
				return nil, prreviews.ErrSourceInvalid
			}
			objects[side] = object
			if object.mode == "160000" {
				f.Submodule = true
				continue
			}
			sizeRaw, err := run(512, "cat-file", "-s", object.sha)
			if err != nil {
				return nil, err
			}
			size, err := strconv.ParseInt(strings.TrimSpace(string(sizeRaw)), 10, 64)
			if err != nil || size < 0 {
				return nil, prreviews.ErrSourceInvalid
			}
			if size > MaxDeliveryBlobBytes {
				f.Binary = true
				continue
			}
			content, err := run(MaxDeliveryBlobBytes, "cat-file", "blob", object.sha)
			if err != nil {
				return nil, err
			}
			if int64(len(content)) != size {
				return nil, prreviews.ErrSourceInvalid
			}
			if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
				f.Binary = true
				continue
			}
			count := bytes.Count(content, []byte{'\n'})
			if len(content) > 0 && content[len(content)-1] != '\n' {
				count++
			}
			if side == 0 {
				f.BaseLines = count
			} else {
				f.HeadLines = count
			}
		}
		label := f.NewPath
		if label == "" {
			label = f.OldPath
		}
		if f.Submodule {
			limitations = append(limitations, fmt.Sprintf("Submodule change %q requires a separate review of its referenced repository.", label))
			continue
		}
		if f.Binary {
			limitations = append(limitations, fmt.Sprintf("Binary, non-UTF-8, or oversized content in %q could not be reviewed as text.", label))
			continue
		}
		diff, err := run(prreviews.MaxDiffBytes, "diff", "--no-ext-diff", "--no-textconv", "--unified=0", objects[0].sha, objects[1].sha, "--")
		if err != nil {
			return nil, err
		}
		f.BaseChanged, f.HeadChanged, err = reviewRanges(diff)
		if err != nil {
			return nil, err
		}
		for _, side := range []struct {
			ranges []prreviews.LineRange
			lines  int
		}{{f.BaseChanged, f.BaseLines}, {f.HeadChanged, f.HeadLines}} {
			for _, r := range side.ranges {
				if r.End > side.lines {
					return nil, prreviews.ErrSourceInvalid
				}
			}
		}
	}
	return limitations, nil
}
