package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"github.com/ruohao1/circular/internal/httpapi"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebhookPrivateSettingsGuardsAndRedaction(t *testing.T) {
	pool := testsupport.Database(t)
	h, e := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir(), Integrations: integrations.Config{WebURL: "https://circular.test", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), Linear: integrations.OAuthApp{ClientID: "fixture-linear"}}})
	if e != nil {
		t.Fatal(e)
	}
	payload := `{"public_origin":"https://receiver.example","signing_secret":"fixture-secret-not-for-response"}`
	for _, tc := range []struct {
		origin, content string
		want            int
	}{{"https://untrusted.test", "application/json", 403}, {"https://circular.test", "text/plain", 415}, {"https://circular.test", "application/json", 200}} {
		r := httptest.NewRequest("POST", "/api/v1/integrations/linear/webhooks", strings.NewReader(payload))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "fixture-secret") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("unsafe settings response")
		}
	}
}
