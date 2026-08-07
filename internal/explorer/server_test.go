package explorer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/application"
)

const (
	fixtureToken  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixtureOrigin = "http://127.0.0.1:43210"
)

func TestHandlerServesOnlyAllowlistedNoCacheAssets(t *testing.T) {
	t.Parallel()
	handler := fixtureHandler(t)
	request := httptest.NewRequest(http.MethodGet, fixtureOrigin+"/"+fixtureToken+"/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Weave graph explorer") {
		t.Fatalf("index response = %d %q", response.Code, response.Body.String())
	}
	for name, want := range map[string]string{
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := response.Header().Get(name); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'wasm-unsafe-eval'") || strings.Contains(csp, "script-src 'self' 'unsafe-eval'") {
		t.Errorf("CSP does not narrowly permit embedded WebAssembly: %q", csp)
	}
	if strings.Contains(response.Body.String(), "https://") || strings.Contains(response.Body.String(), "http://") {
		t.Fatalf("index contains a remote runtime asset: %q", response.Body.String())
	}

	tests := []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/" + fixtureToken + "/", http.StatusMethodNotAllowed},
		{http.MethodGet, "/wrong/", http.StatusNotFound},
		{http.MethodGet, "/" + fixtureToken + "/assets/../README.md", http.StatusNotFound},
		{http.MethodGet, "/" + fixtureToken + "/assets/app.js?cache=1", http.StatusNotFound},
		{http.MethodGet, "/" + fixtureToken + "/api/graph", http.StatusMethodNotAllowed},
		{http.MethodPost, "/" + fixtureToken + "/api/config", http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, fixtureOrigin+test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Errorf("%s %s = %d, want %d", test.method, test.path, response.Code, test.want)
		}
		if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") {
			t.Errorf("%s %s omitted no-store", test.method, test.path)
		}
	}
}

func TestHandlerValidatesHostOriginContentTypeAndBody(t *testing.T) {
	t.Parallel()
	handler := fixtureHandler(t)
	validBody := `{"target":"focus","direction":"both","max_depth":2,"limit":10,"max_edges":20}`
	tests := []struct {
		name        string
		body        string
		contentType string
		host        string
		origin      string
		fetchSite   string
		want        int
	}{
		{name: "bad host", body: validBody, contentType: "application/json", host: "attacker.example", want: http.StatusForbidden},
		{name: "foreign origin", body: validBody, contentType: "application/json", origin: "https://attacker.example", want: http.StatusForbidden},
		{name: "cross site", body: validBody, contentType: "application/json", fetchSite: "cross-site", want: http.StatusForbidden},
		{name: "wrong media", body: validBody, contentType: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "unknown field", body: `{"target":"focus","surprise":true}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "two objects", body: `{"target":"focus"}{"target":"focus"}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "too large", body: `{"target":"` + strings.Repeat("x", maxRequestBytes) + `"}`, contentType: "application/json", want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, fixtureOrigin+"/"+fixtureToken+"/api/graph", strings.NewReader(test.body))
			if test.host != "" {
				request.Host = test.host
			}
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.fetchSite != "" {
				request.Header.Set("Sec-Fetch-Site", test.fetchSite)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("response = %d %q, want %d", response.Code, response.Body.String(), test.want)
			}
		})
	}
}

func TestHandlerReturnsConfigAndRenderedGraph(t *testing.T) {
	t.Parallel()
	service := &fixtureService{response: graphResponse()}
	engine, err := New(service, application.Invocation{Arguments: []string{"Initial"}, Limit: 12, MaxDepth: 2, MaxEdges: 44})
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(engine, fixtureToken, fixtureOrigin)

	configRequest := httptest.NewRequest(http.MethodGet, fixtureOrigin+"/"+fixtureToken+"/api/config", nil)
	configResponse := httptest.NewRecorder()
	handler.ServeHTTP(configResponse, configRequest)
	if configResponse.Code != http.StatusOK || !strings.Contains(configResponse.Body.String(), `"target":"Initial"`) || !strings.Contains(configResponse.Body.String(), `"max_edges":44`) {
		t.Fatalf("config response = %d %q", configResponse.Code, configResponse.Body.String())
	}

	body := `{"target":"Focus","direction":"outgoing","max_depth":3,"limit":10,"max_edges":30,"kinds":["calls"]}`
	graphRequest := httptest.NewRequest(http.MethodPost, fixtureOrigin+"/"+fixtureToken+"/api/graph", strings.NewReader(body))
	graphRequest.Header.Set("Content-Type", "application/json; charset=utf-8")
	graphRequest.Header.Set("Origin", fixtureOrigin)
	graphRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	graphResponse := httptest.NewRecorder()
	handler.ServeHTTP(graphResponse, graphRequest)
	if graphResponse.Code != http.StatusOK {
		t.Fatalf("graph response = %d %q", graphResponse.Code, graphResponse.Body.String())
	}
	var result Result
	if err := json.Unmarshal(graphResponse.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != Schema || result.Focus != "focus" || !strings.Contains(result.DOT, "digraph weave") {
		t.Fatalf("graph result = %#v", result)
	}
}

func TestStartUsesRandomUnguessableLoopbackURL(t *testing.T) {
	service := &fixtureService{response: graphResponse()}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}})
	if err != nil {
		t.Fatal(err)
	}
	running, err := Start(Config{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer running.Close(shutdownCtx)
	parsed, err := url.Parse(running.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		t.Fatalf("explorer URL is not ephemeral IPv4 loopback: %q", running.URL)
	}
	if !regexp.MustCompile(`^/[0-9a-f]{64}/$`).MatchString(parsed.Path) {
		t.Fatalf("explorer URL lacks 256-bit session token: %q", running.URL)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(running.URL + "api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		content, _ := io.ReadAll(response.Body)
		t.Fatalf("live config = %d %q", response.StatusCode, content)
	}
}

func TestEmbeddedAssetsArePinnedLocalAndChecksumVerified(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"assets/index.html", "assets/app.js", "assets/app.css"} {
		content, err := assets.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, remote := range []string{"https://", "http://", "//cdn"} {
			if bytes.Contains(content, []byte(remote)) {
				t.Errorf("%s contains runtime remote reference %q", name, remote)
			}
		}
	}
	javascript, err := assets.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, behavior := range []string{
		`.keyMode("id")`, `useWorker: true`, `prefers-reduced-motion`,
		`d3.transition("weave-graph")`, `resetZoom()`, `ResizeObserver`,
	} {
		if !bytes.Contains(javascript, []byte(behavior)) {
			t.Errorf("app.js omitted %q", behavior)
		}
	}
	checksums, err := assets.ReadFile("assets/vendor/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(checksums)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("invalid checksum line %q", line)
		}
		content, err := assets.ReadFile("assets/vendor/" + fields[1])
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		if got := hex.EncodeToString(digest[:]); got != fields[0] {
			t.Errorf("%s checksum = %s, want %s", fields[1], got, fields[0])
		}
	}
	for _, license := range []string{
		"assets/licenses/D3-ISC.txt", "assets/licenses/d3-graphviz-BSD-3-Clause.txt",
		"assets/licenses/hpcc-js-wasm-Apache-2.0.txt", "assets/licenses/Graphviz-EPL-1.0.txt",
	} {
		if content, err := assets.ReadFile(license); err != nil || len(content) < 100 {
			t.Errorf("license %s unavailable or empty", license)
		}
	}
}

func fixtureHandler(t *testing.T) http.Handler {
	t.Helper()
	service := &fixtureService{response: graphResponse()}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}})
	if err != nil {
		t.Fatal(err)
	}
	return newHandler(engine, fixtureToken, fixtureOrigin)
}
