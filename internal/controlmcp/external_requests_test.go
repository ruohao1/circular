package controlmcp_test

import (
	"encoding/json"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/controlmcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExternalRequestToolsRestrictMutationsAndPreserveFingerprint(t *testing.T) {
	id := uuid.NewString()
	var writes atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			writes.Add(1)
			var in object
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Error("missing input")
			}
			if strings.HasSuffix(r.URL.Path, "/start") && in["expected_input_fingerprint"] != strings.Repeat("a", 64) {
				t.Error("missing fingerprint", in)
			}
		}
		w.Write([]byte(`{"id":"` + id + `","items":[],"next_cursor":""}`))
	}))
	defer api.Close()
	ro := connect(t, controlmcp.Config{APIURL: api.URL, ReadOnly: true})
	full := connect(t, controlmcp.Config{APIURL: api.URL})
	call(t, ro, "circular_list_requests", object{"unrouted": true})
	call(t, ro, "circular_get_request", object{"request_id": id})
	args := object{"request_id": id, "expected_input_fingerprint": strings.Repeat("a", 64)}
	rejects(t, ro, "circular_start_request", args, "")
	rejects(t, ro, "circular_stop_request", object{"request_id": id}, "")
	rejects(t, full, "circular_start_request", object{"request_id": id}, "")
	call(t, full, "circular_start_request", args)
	call(t, full, "circular_stop_request", object{"request_id": id})
	if writes.Load() != 2 {
		t.Fatal("wrong mutation count", writes.Load())
	}
}
