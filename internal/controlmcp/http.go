package controlmcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPOptions defines the explicitly trusted addresses for a local MCP endpoint.
// Host and Origin validation prevents browser cross-origin and DNS rebinding
// requests. It is not authentication: serve this endpoint on a trusted interface.
type HTTPOptions struct {
	AllowedHosts   []string // host[:port], such as localhost:8000
	AllowedOrigins []string // HTTP(S) origins, such as http://localhost:5173
	// OnActivity runs after a successful initialize, tool list, or tool call.
	// It may run concurrently. Values identify a client, not an authenticated user.
	OnActivity func(name, version string)
}

// NewHTTP constructs a stateless Streamable HTTP endpoint with one persistent
// server. Call it separately for full-control and read-only connections.
func NewHTTP(config Config, options HTTPOptions) (http.Handler, error) {
	hosts := make(map[string]bool, len(options.AllowedHosts))
	for _, host := range options.AllowedHosts {
		normalized, err := allowedHost(host)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed MCP host %q: %w", host, err)
		}
		hosts[normalized] = true
	}
	if len(hosts) == 0 {
		return nil, errors.New("MCP HTTP requires at least one explicitly allowed host")
	}
	origins := make(map[string]bool, len(options.AllowedOrigins))
	for _, origin := range options.AllowedOrigins {
		normalized, err := allowedOrigin(origin)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed MCP origin %q: %w", origin, err)
		}
		origins[normalized] = true
	}
	server, err := New(config)
	if err != nil {
		return nil, err
	}
	if options.OnActivity != nil {
		server.AddReceivingMiddleware(activityMiddleware(options.OnActivity))
	}
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 256 << 10,
		PropagateRequestCancellation: true,
		// The explicit guard below works on Docker bridge interfaces too, and
		// permits only configured hosts rather than every localhost address.
		DisableLocalhostProtection: true,
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, err := allowedHost(r.Host)
		if err != nil || !hosts[host] {
			http.Error(w, "MCP request host is not allowed", http.StatusForbidden)
			return
		}
		// No Origin is normal for native MCP clients. An explicitly empty,
		// opaque, repeated, or untrusted Origin must never reach tool dispatch.
		if values, present := r.Header["Origin"]; present {
			if len(values) != 1 {
				http.Error(w, "MCP request origin is not allowed", http.StatusForbidden)
				return
			}
			origin, err := allowedOrigin(values[0])
			if err != nil || !origins[origin] {
				http.Error(w, "MCP request origin is not allowed", http.StatusForbidden)
				return
			}
		}
		transport.ServeHTTP(w, r)
	}), nil
}

func allowedHost(value string) (string, error) {
	u, err := url.Parse("http://" + value)
	if err != nil || value == "" || u.Hostname() == "" || u.Host != value || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(value, "*\\ \t\r\n") {
		return "", errors.New("use an explicit host and optional port")
	}
	return strings.ToLower(u.Host), nil
}

func allowedOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") {
		return "", errors.New("use an HTTP(S) origin without a path, credentials, query, or fragment")
	}
	host, err := allowedHost(u.Host)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + host, nil
}

func activityMiddleware(onActivity func(string, string)) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || (method != "initialize" && method != "tools/list" && method != "tools/call") {
				return result, err
			}
			if called, ok := result.(*mcp.CallToolResult); ok && called.IsError {
				return result, nil
			}
			var info *mcp.Implementation
			if params, ok := req.GetParams().(*mcp.InitializeParams); ok {
				info = params.ClientInfo
			} else if identified, ok := req.(interface{ ClientInfo() *mcp.Implementation }); ok {
				info = identified.ClientInfo()
			}
			if info != nil {
				onActivity(activityText(info.Name), activityText(info.Version))
			}
			return result, nil
		}
	}
}

func activityText(value string) string {
	runes := []rune(strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)))
	return string(runes[:min(len(runes), 128)])
}
