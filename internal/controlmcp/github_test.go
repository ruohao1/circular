package controlmcp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/controlmcp"
)

func TestGitHubToolsPreserveCreationIntentAndReadOnlyAccess(t *testing.T) {
	project, key := uuid.NewString(), uuid.NewString()
	var posts atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/projects/" + project + "/integrations/github/installations":
			if r.URL.Query().Get("page") != "2" {
				t.Error("account pagination lost")
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"101","account":"owner","account_type":"User","can_create":true,"permission_message":"","management_url":"https://github.com/settings/installations/101"}],"next_page":0}`))
		case "POST /api/v1/projects/" + project + "/integrations/github/repositories":
			posts.Add(1)
			var input object
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Fatal("invalid creation JSON")
			}
			if input["request_key"] != key || input["installation_id"] != "101" || input["visibility"] != "private" || input["name"] != "new-repo" || input["project_id"] != nil {
				t.Error("creation intent changed", input)
			}
			_, _ = w.Write([]byte(`{"status":"uncertain","repository_id":"","github_repository_id":"","name":"owner/new-repo","url":"https://github.com/owner/new-repo","management_url":"","message":"Check GitHub before proceeding."}`))
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	client := connect(t, controlmcp.Config{APIURL: api.URL})
	accounts := call(t, client, "list_github_accounts", object{"project_id": project, "page": 2})
	if len(accounts["items"].([]any)) != 1 {
		t.Fatal("GitHub accounts missing")
	}
	args := object{"project_id": project, "installation_id": "101", "name": "new-repo", "request_key": key}
	for range 2 {
		result := call(t, client, "create_github_repository", args)["creation"].(object)
		if result["status"] != "uncertain" {
			t.Fatal("uncertain outcome hidden", result)
		}
	}
	if posts.Load() != 2 {
		t.Fatal("retry did not use the same API operation")
	}
	for _, bad := range []object{
		{"project_id": project, "installation_id": "../101", "name": "new-repo", "request_key": key},
		{"project_id": project, "installation_id": "101", "name": "new-repo", "request_key": uuid.Nil.String()},
		{"project_id": project, "installation_id": "101", "name": "new-repo", "request_key": key, "visibility": "internal"},
	} {
		rejects(t, client, "create_github_repository", bad, "")
	}
	if posts.Load() != 2 {
		t.Fatal("invalid creation reached the API")
	}
	ro := connect(t, controlmcp.Config{APIURL: api.URL, ReadOnly: true})
	rejects(t, ro, "create_github_repository", args, "")
	call(t, ro, "list_github_accounts", object{"project_id": project, "page": 2})
	if posts.Load() != 2 {
		t.Fatal("read-only connection created a repository")
	}
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "create_github_repository" && (tool.Annotations == nil || !tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint || tool.Annotations.ReadOnlyHint) {
			t.Fatal("creation annotations hide external writes")
		}
	}
}
