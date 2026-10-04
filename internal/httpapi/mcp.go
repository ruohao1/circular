package httpapi

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ruohao1/circular/internal/controlmcp"
)

type mcpActivity struct {
	ClientName    string `json:"client_name"`
	ClientVersion string `json:"client_version"`
	Access        string `json:"access"`
	At            string `json:"at"`
}

func (a *api) mcpRoutes(mux *http.ServeMux) error {
	apiURL := strings.TrimRight(a.config.Integrations.APIURL, "/")
	webURL := strings.TrimRight(a.config.Integrations.WebURL, "/")
	if apiURL == "" {
		apiURL = controlmcp.DefaultAPIURL
	}
	if webURL == "" {
		webURL = controlmcp.DefaultWebURL
	}
	// integrations.New already validates both configured origins. The defaults
	// here also cover API fixtures without provider integration configuration.
	address, err := url.Parse(apiURL)
	if err != nil {
		return err
	}
	hosts := []string{address.Host}
	if address.Hostname() == "localhost" || net.ParseIP(address.Hostname()).IsLoopback() {
		for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
			if address.Port() != "" {
				host = net.JoinHostPort(host, address.Port())
			} else if host == "::1" {
				host = "[::1]"
			}
			hosts = append(hosts, host)
		}
	}
	origins := []string{apiURL, webURL}
	for _, origin := range a.config.CORSOrigins {
		if origin != "*" {
			origins = append(origins, origin)
		}
	}
	var mu sync.RWMutex
	var last *mcpActivity
	for _, access := range []string{"control", "read-only"} {
		readOnly := access == "read-only"
		handler, err := controlmcp.NewHTTP(controlmcp.Config{
			APIURL: apiURL, WebURL: webURL, ReadOnly: readOnly,
			HTTPClient: &http.Client{Transport: localAPITransport{handler: mux}},
		}, controlmcp.HTTPOptions{
			AllowedHosts: hosts, AllowedOrigins: origins,
			OnActivity: func(name, version string) {
				// Keep only bounded display metadata, never tool arguments or output.
				clip := func(value string) string { r := []rune(value); return string(r[:min(len(r), 80)]) }
				activity := &mcpActivity{ClientName: clip(name), ClientVersion: clip(version), Access: access, At: time.Now().UTC().Format(time.RFC3339Nano)}
				mu.Lock()
				last = activity
				mu.Unlock()
			},
		})
		if err != nil {
			return err
		}
		path := "/mcp"
		if readOnly {
			path += "/read-only"
		}
		mux.Handle(path, handler)
	}
	mux.HandleFunc("GET /api/v1/mcp/connection", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		activity := last
		mu.RUnlock()
		w.Header().Set("Cache-Control", "no-store")
		respond(w, http.StatusOK, map[string]any{
			"available": true, "url": apiURL + "/mcp", "read_only_url": apiURL + "/mcp/read-only", "last_activity": activity,
		})
	})
	return nil
}

// The bundled MCP server calls the same resource handlers as the console. An
// in-process adapter avoids a second listener and works behind a reverse proxy.
// Bound the adapter's extra response buffering to the external client's limit.
type localAPITransport struct{ handler http.Handler }

func (t localAPITransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	w := &localAPIResponse{header: make(http.Header)}
	t.handler.ServeHTTP(w, r)
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return &http.Response{StatusCode: w.status, Header: w.header.Clone(), Body: io.NopCloser(bytes.NewReader(w.body.Bytes())), Request: r, ContentLength: int64(w.body.Len())}, nil
}

type localAPIResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *localAPIResponse) Header() http.Header { return w.header }
func (w *localAPIResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *localAPIResponse) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	// Retain one extra byte so the MCP client's response limit can report an
	// oversized result rather than misclassifying it as a connection failure.
	remaining := (8 << 20) + 1 - w.body.Len()
	if len(data) > remaining {
		n, _ := w.body.Write(data[:remaining])
		return n, errors.New("response exceeds buffer limit")
	}
	return w.body.Write(data)
}
