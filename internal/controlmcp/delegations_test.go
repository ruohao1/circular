package controlmcp_test

import (
	"github.com/ruohao1/circular/internal/controlmcp"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/testsupport"
	"net/http/httptest"
	"testing"
)

func TestDelegationPolicyMCP(t *testing.T) {
	pool := testsupport.Database(t)
	handler, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(handler)
	defer api.Close()
	config := controlmcp.Config{APIURL: api.URL}
	client := connect(t, config)
	project := call(t, client, "create_project", object{"name": "Delegation"})["project"].(object)["id"]
	parent := call(t, client, "create_agent", object{"project_id": project, "name": "Parent", "instructions": "Work", "backend": "fake"})["agent"].(object)["id"]
	target := call(t, client, "create_agent", object{"project_id": project, "name": "Target", "instructions": "Review", "backend": "fake"})["agent"].(object)["id"]
	call(t, client, "set_delegation_targets", object{"agent_id": parent, "target_ids": []any{target}})
	result := call(t, client, "get_delegation_targets", object{"agent_id": parent})
	if len(result["target_ids"].([]any)) != 1 {
		t.Fatal(result)
	}
	config.ReadOnly = true
	readonly := connect(t, config)
	rejects(t, readonly, "set_delegation_targets", object{"agent_id": parent, "target_ids": []any{}}, "")
	call(t, readonly, "get_delegation_targets", object{"agent_id": parent})
}
