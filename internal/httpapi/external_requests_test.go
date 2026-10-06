package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExternalRequestHTTPStrictInputAndStop(t *testing.T) {
	pool := testsupport.Database(t)
	h, e := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir(), Integrations: integrations.Config{WebURL: "https://circular.test", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}})
	if e != nil {
		t.Fatal(e)
	}
	identity, request := uuid.NewString(), uuid.NewString()
	if _, e = pool.Exec(t.Context(), `WITH i AS(INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,status) VALUES($1,'linear','fixture-linear','workspace','Fixture','actor','Circular','available')) INSERT INTO external_requests(id,identity_id,session_id,input_fingerprint) VALUES($2,$1,$3,$4)`, identity, request, uuid.NewString(), strings.Repeat("a", 64)); e != nil {
		t.Fatal(e)
	}
	base := "/api/v1/external-requests"
	for _, tc := range []struct {
		method, path, body, origin, content string
		want                                int
	}{
		{"GET", base + "?unrouted=true", "", "", "", 200},
		{"GET", base + "?unrouted=true&project_id=" + uuid.NewString(), "", "", "", 422},
		{"GET", base + "?unrouted=true&limit=101", "", "", "", 422},
		{"GET", base + "/" + request, "", "", "", 200},
		{"POST", base + "/" + request + "/start", "{}", "", "application/json", 422},
		{"POST", base + "/" + request + "/start", `{"expected_input_fingerprint":"` + strings.Repeat("a", 64) + `","extra":true}`, "", "application/json", 422},
		{"POST", base + "/" + request + "/stop", "{}", "https://evil.test", "application/json", 403},
		{"POST", base + "/" + request + "/stop", "{}", "", "text/plain", 415},
		{"POST", base + "/" + request + "/stop", "{}", "", "application/json", 200},
		{"POST", base + "/" + request + "/stop", "{}", "", "application/json", 200},
		{"GET", base + "/" + uuid.NewString(), "", "", "", 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cached request")
		}
	}
}

func TestExternalRequestAttentionPagination(t *testing.T) {
	f := setup(t)
	project := f.create(t, "projects", map[string]any{"name": "Attention"})["id"].(string)
	other := f.create(t, "projects", map[string]any{"name": "Other"})["id"].(string)
	identity := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,status) VALUES($1,'linear','fixture-linear','workspace','Fixture','actor','Circular','available')`, identity); err != nil {
		t.Fatal(err)
	}
	insert := func(scope any, status string, stamp string) string {
		id := uuid.NewString()
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO external_requests(id,identity_id,session_id,input_fingerprint,project_id,status,created_at) VALUES($1,$2,$3,$4,$5,$6,$7::timestamptz)`, id, identity, uuid.NewString(), strings.Repeat("a", 64), scope, status, stamp); err != nil {
			t.Fatal(err)
		}
		return id
	}
	approval := insert(project, "awaiting_approval", "2026-10-01T00:00:00Z")
	access := insert(project, "needs_access", "2026-09-30T00:00:00Z")
	insert(project, "needs_routing", "2026-09-29T00:00:00Z")
	foreign := insert(other, "awaiting_approval", "2026-10-03T00:00:00Z")
	unrouted := insert(nil, "needs_routing", "2026-10-02T00:00:00Z")
	for range 21 {
		insert(project, "succeeded", "2026-10-02T00:00:00Z")
	}
	read := func(query string) integrations.RequestPage {
		var page integrations.RequestPage
		raw := f.request(t, "GET", "/api/v1/external-requests?"+query, "", 200)
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	base := "project_id=" + project
	first := read(base + "&attention=true&limit=1")
	if len(first.Items) != 1 || first.Items[0].ID != approval || first.NextCursor != approval {
		t.Fatal("older approval missing", first)
	}
	// A status transition must not invalidate the cursor's position.
	if _, err := f.pool.Exec(t.Context(), "UPDATE external_requests SET status='stopped' WHERE id=$1", approval); err != nil {
		t.Fatal(err)
	}
	next := read(base + "&attention=true&cursor=" + approval)
	if len(next.Items) != 2 || next.Items[0].ID != access || next.NextCursor != "" {
		t.Fatal("attention pagination", next)
	}
	global := read("unrouted=true&attention=true")
	if len(global.Items) != 1 || global.Items[0].ID != unrouted {
		t.Fatal("unrouted scope", global)
	}
	for _, query := range []string{base + "&cursor=" + foreign, base + "&cursor=" + unrouted, "unrouted=true&cursor=" + access} {
		f.request(t, "GET", "/api/v1/external-requests?"+query, "", 422)
	}
	for _, suffix := range []string{"", "&attention=false"} {
		all := read(base + suffix)
		if len(all.Items) != 20 || all.NextCursor == "" {
			t.Fatal("default listing changed", all)
		}
	}
	f.request(t, "GET", "/api/v1/external-requests?"+base+"&attention=yes", "", 422)
}
