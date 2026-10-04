package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/testsupport"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Git Data API fixture writes real objects only into the disposable repository.
// This lets the production publisher and reviewer verify the same exact commits.
type fixtureGitDelivery struct {
	mu       sync.Mutex
	enabled  atomic.Bool
	source   string
	provider *testsupport.ProviderFixture
	prs      []map[string]any
}

func (f *fixtureGitDelivery) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !f.enabled.Load() || !strings.HasPrefix(r.URL.Path, "/repos/fixture/private-source/") {
		f.provider.ServeHTTP(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/fixture/private-source")
	if path != "/pulls" && !strings.HasPrefix(path, "/git/") {
		f.provider.ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	git := func(input string, args ...string) (string, error) {
		cmd := exec.CommandContext(r.Context(), "git", append([]string{"-C", f.source}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		out, e := cmd.Output()
		return strings.TrimSpace(string(out)), e
	}
	respond := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
	switch {
	case r.Method == "GET" && strings.HasPrefix(path, "/git/commits/"):
		sha := strings.TrimPrefix(path, "/git/commits/")
		tree, e := git("", "rev-parse", sha+"^{tree}")
		if e != nil {
			w.WriteHeader(404)
			return
		}
		respond(200, map[string]any{"sha": sha, "tree": map[string]string{"sha": tree}})
	case r.Method == "GET" && strings.HasPrefix(path, "/git/ref/heads/"):
		ref := "refs/heads/" + strings.TrimPrefix(path, "/git/ref/heads/")
		sha, e := git("", "rev-parse", "--verify", ref)
		if e != nil {
			w.WriteHeader(404)
			return
		}
		respond(200, map[string]any{"ref": ref, "object": map[string]string{"sha": sha}})
	case r.Method == "POST" && path == "/git/blobs":
		var in struct{ Content string }
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		raw, e := base64.StdEncoding.DecodeString(in.Content)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		sha, e := git(string(raw), "hash-object", "-w", "--stdin")
		if e != nil {
			w.WriteHeader(500)
			return
		}
		respond(201, map[string]string{"sha": sha})
	case r.Method == "POST" && path == "/git/trees":
		var in struct {
			Base string `json:"base_tree"`
			Tree []struct {
				Path, Mode, Type string
				SHA              *string
			} `json:"tree"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		index := filepath.Join(f.source, ".git", "fixture-delivery-index")
		defer os.Remove(index)
		run := func(args ...string) error {
			cmd := exec.CommandContext(r.Context(), "git", append([]string{"-C", f.source}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
			return cmd.Run()
		}
		e := run("read-tree", in.Base)
		for _, entry := range in.Tree {
			if e != nil {
				break
			}
			if entry.SHA == nil {
				e = run("update-index", "--force-remove", "--", entry.Path)
			} else {
				e = run("update-index", "--add", "--cacheinfo", entry.Mode+","+*entry.SHA+","+entry.Path)
			}
		}
		if e != nil {
			w.WriteHeader(500)
			return
		}
		cmd := exec.CommandContext(r.Context(), "git", "-C", f.source, "write-tree")
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		raw, e := cmd.Output()
		if e != nil {
			w.WriteHeader(500)
			return
		}
		respond(201, map[string]string{"sha": strings.TrimSpace(string(raw))})
	case r.Method == "POST" && path == "/git/commits":
		var in struct {
			Message, Tree     string
			Parents           []string
			Author, Committer map[string]string
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Parents) != 1 {
			w.WriteHeader(400)
			return
		}
		cmd := exec.CommandContext(r.Context(), "git", "-C", f.source, "commit-tree", in.Tree, "-p", in.Parents[0])
		cmd.Stdin = bytes.NewBufferString(in.Message)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME="+in.Author["name"], "GIT_AUTHOR_EMAIL="+in.Author["email"], "GIT_AUTHOR_DATE="+in.Author["date"], "GIT_COMMITTER_NAME="+in.Committer["name"], "GIT_COMMITTER_EMAIL="+in.Committer["email"], "GIT_COMMITTER_DATE="+in.Committer["date"])
		raw, e := cmd.Output()
		if e != nil {
			w.WriteHeader(500)
			return
		}
		respond(201, map[string]any{"sha": strings.TrimSpace(string(raw)), "tree": map[string]string{"sha": in.Tree}, "parents": []any{map[string]string{"sha": in.Parents[0]}}})
	case r.Method == "POST" && path == "/git/refs":
		var in struct{ Ref, SHA string }
		if json.NewDecoder(r.Body).Decode(&in) != nil || !strings.HasPrefix(in.Ref, "refs/heads/circular/run/") {
			w.WriteHeader(400)
			return
		}
		if _, e := git("", "update-ref", in.Ref, in.SHA); e != nil {
			w.WriteHeader(500)
			return
		}
		respond(201, map[string]any{"ref": in.Ref, "object": map[string]string{"sha": in.SHA}})
	case r.Method == "GET" && path == "/pulls":
		out := []map[string]any{}
		head := strings.TrimPrefix(r.URL.Query().Get("head"), "fixture:")
		for _, p := range f.prs {
			if p["head"].(map[string]any)["ref"] == head {
				out = append(out, p)
			}
		}
		respond(200, out)
	case r.Method == "POST" && path == "/pulls":
		var in struct {
			Head, Base, Body string
			Draft            bool
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || !in.Draft {
			w.WriteHeader(400)
			return
		}
		head, e := git("", "rev-parse", "refs/heads/"+in.Head)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		base, e := git("", "rev-parse", "refs/heads/"+in.Base)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		number := len(f.prs) + 1
		url := "https://github.com/fixture/private-source/pull/" + strconv.Itoa(number)
		author := 101
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_") {
			author = 501
		}
		p := map[string]any{"user": map[string]int{"id": author}, "number": number, "html_url": url, "body": in.Body, "draft": true, "state": "open", "head": map[string]any{"ref": in.Head, "sha": head, "repo": map[string]int{"id": 202}}, "base": map[string]any{"ref": in.Base, "sha": base, "repo": map[string]int{"id": 202}}}
		f.prs = append(f.prs, p)
		f.provider.SetReviewPR(prreviews.PRIdentity{InstallationID: "101", GitHubRepositoryID: "202", RepositoryName: "fixture/private-source", Number: number, URL: url, BaseRef: in.Base, HeadRef: in.Head, BaseSHA: base, HeadSHA: head}, "open")
		respond(201, p)
	default:
		http.Error(w, fmt.Sprintf("unsupported fixture git endpoint %s", path), 404)
	}
}
