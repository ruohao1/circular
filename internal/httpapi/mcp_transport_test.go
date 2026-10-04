package httpapi

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/controlmcp"
)

func TestLocalMCPTransportPreservesResponseLimitError(t *testing.T) {
	var written int64
	transport := localAPITransport{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		written, _ = io.Copy(w, strings.NewReader(strings.Repeat("x", 9<<20)))
	})}
	err := controlmcp.Check(t.Context(), controlmcp.Config{HTTPClient: &http.Client{Transport: transport}})
	if err == nil || !strings.Contains(err.Error(), "exceeds 8 MiB") {
		t.Fatalf("expected actionable size-limit error, got %v", err)
	}
	if written != (8<<20)+1 {
		t.Fatalf("response buffer grew beyond MCP read limit: %d", written)
	}
}
