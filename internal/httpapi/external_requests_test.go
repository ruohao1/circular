package httpapi_test

import (
	"bytes"
	"encoding/base64"
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
