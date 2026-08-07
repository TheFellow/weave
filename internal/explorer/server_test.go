package explorer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/bridge"
	"github.com/TheFellow/weave/internal/contextquery"
	"github.com/TheFellow/weave/internal/graph"
	"github.com/TheFellow/weave/internal/graphdiff"
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
		{http.MethodGet, "/" + fixtureToken + "/api/diff", http.StatusMethodNotAllowed},
		{http.MethodGet, "/" + fixtureToken + "/api/context", http.StatusMethodNotAllowed},
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

func TestHandlerServesSourceRichDetailsAndRevisionGuardedLinks(t *testing.T) {
	t.Parallel()
	revision := "sha256:" + strings.Repeat("a", 64)
	nextRevision := "sha256:" + strings.Repeat("b", 64)
	contextResult := contextquery.Result{
		Schema: contextquery.Schema,
		Focus: contextquery.Entity{Symbol: graph.Symbol{
			ID: "focus", DisplayName: "Focus", Provider: "fixture", Evidence: graph.EvidenceExact,
		}},
	}
	service := &routingService{execute: func(invocation application.Invocation) (application.Response, error) {
		switch invocation.Command {
		case "context":
			return application.Response{Command: "context", Context: &contextResult}, nil
		case "links list":
			return application.Response{Command: "links list", LinkRevision: revision}, nil
		case "links add":
			return application.Response{Command: "links add", LinkRevision: nextRevision, Links: []bridge.Link{{ID: "docs-code"}}}, nil
		case "links update":
			return application.Response{}, fmt.Errorf("fixture conflict: %w", application.ErrLinkRevision)
		default:
			return application.Response{}, fmt.Errorf("unexpected command %q", invocation.Command)
		}
	}}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}, Scope: "local"})
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(engine, fixtureToken, fixtureOrigin)

	detail := httptest.NewRequest(http.MethodPost, fixtureOrigin+"/"+fixtureToken+"/api/context", strings.NewReader(`{"target":"focus","limit":8,"context_lines":2,"max_source_bytes":4096}`))
	detail.Header.Set("Content-Type", "application/json")
	detail.Header.Set("Origin", fixtureOrigin)
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detail)
	if detailResponse.Code != http.StatusOK || !strings.Contains(detailResponse.Body.String(), `"kind":"node"`) || !strings.Contains(detailResponse.Body.String(), `"id":"focus"`) {
		t.Fatalf("detail response = %d %q", detailResponse.Code, detailResponse.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, fixtureOrigin+"/"+fixtureToken+"/api/links", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), revision) {
		t.Fatalf("list response = %d %q", listResponse.Code, listResponse.Body.String())
	}

	addBody := `{"id":"docs-code","revision":"` + revision + `","from":"entity:focus","to":"entity:dependency","kind":"documents","note":"why"}`
	add := httptest.NewRequest(http.MethodPost, fixtureOrigin+"/"+fixtureToken+"/api/links", strings.NewReader(addBody))
	add.Header.Set("Content-Type", "application/json")
	add.Header.Set("Origin", fixtureOrigin)
	add.Header.Set("Sec-Fetch-Site", "same-origin")
	addResponse := httptest.NewRecorder()
	handler.ServeHTTP(addResponse, add)
	if addResponse.Code != http.StatusOK || !strings.Contains(addResponse.Body.String(), nextRevision) || !strings.Contains(addResponse.Body.String(), `"operation":"add"`) || strings.Contains(addResponse.Body.String(), `"links"`) {
		t.Fatalf("add response = %d %q", addResponse.Code, addResponse.Body.String())
	}

	update := httptest.NewRequest(http.MethodPut, fixtureOrigin+"/"+fixtureToken+"/api/links", strings.NewReader(`{"id":"docs-code","revision":"`+revision+`","note":"new"}`))
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("Origin", fixtureOrigin)
	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusConflict || !strings.Contains(updateResponse.Body.String(), "fixture conflict") {
		t.Fatalf("conflict response = %d %q", updateResponse.Code, updateResponse.Body.String())
	}

	foreign := httptest.NewRequest(http.MethodDelete, fixtureOrigin+"/"+fixtureToken+"/api/links", strings.NewReader(`{"id":"docs-code","revision":"`+revision+`"}`))
	foreign.Header.Set("Content-Type", "application/json")
	foreign.Header.Set("Origin", "https://attacker.example")
	foreignResponse := httptest.NewRecorder()
	handler.ServeHTTP(foreignResponse, foreign)
	if foreignResponse.Code != http.StatusForbidden {
		t.Fatalf("foreign mutation response = %d %q", foreignResponse.Code, foreignResponse.Body.String())
	}

	invocations := service.Invocations()
	if len(invocations) != 4 || invocations[2].Command != "links add" || invocations[2].LinkRevision != revision || !invocations[2].LinkFromSet || invocations[3].Command != "links update" {
		t.Fatalf("application invocations = %#v", invocations)
	}
}

func TestHandlerRoundTripsCanonicalLinkCreateUpdateRemove(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "-q")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	engine, err := New(application.Local{Directory: root}, application.Invocation{Arguments: []string{"focus"}, Scope: "local"})
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(engine, fixtureToken, fixtureOrigin)

	do := func(method, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, fixtureOrigin+"/"+fixtureToken+"/api/links", strings.NewReader(body))
		if method != http.MethodGet {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", fixtureOrigin)
			request.Header.Set("Sec-Fetch-Site", "same-origin")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	decodeList := func(response *httptest.ResponseRecorder) LinkList {
		t.Helper()
		var result LinkList
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %d response %q: %v", response.Code, response.Body.String(), err)
		}
		return result
	}
	decodeMutation := func(response *httptest.ResponseRecorder) LinkMutationResult {
		t.Helper()
		var result LinkMutationResult
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %d response %q: %v", response.Code, response.Body.String(), err)
		}
		return result
	}

	initialResponse := do(http.MethodGet, "")
	if initialResponse.Code != http.StatusOK {
		t.Fatalf("initial list = %d %q", initialResponse.Code, initialResponse.Body.String())
	}
	initial := decodeList(initialResponse)
	addResponse := do(http.MethodPost, `{"id":"docs-code","revision":"`+initial.Revision+`","from":"id:docs","to":"id:code","kind":"documents","note":"first"}`)
	if addResponse.Code != http.StatusOK {
		t.Fatalf("add = %d %q", addResponse.Code, addResponse.Body.String())
	}
	added := decodeMutation(addResponse)
	if added.Operation != "add" || added.Link.ID != "docs-code" || added.Revision == initial.Revision {
		t.Fatalf("add result = %#v", added)
	}
	config, err := bridge.Load(bridge.Path(root))
	if err != nil || len(config.Links) != 1 || config.Links[0].Note != "first" {
		t.Fatalf("canonical add = %#v, %v", config, err)
	}

	staleResponse := do(http.MethodPost, `{"id":"stale","revision":"`+initial.Revision+`","from":"id:a","to":"id:b","kind":"links-to"}`)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale add = %d %q", staleResponse.Code, staleResponse.Body.String())
	}
	updateResponse := do(http.MethodPut, `{"id":"docs-code","revision":"`+added.Revision+`","note":"updated"}`)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf("update = %d %q", updateResponse.Code, updateResponse.Body.String())
	}
	updated := decodeMutation(updateResponse)
	if updated.Operation != "update" || updated.Link.ID != "docs-code" {
		t.Fatalf("update result = %#v", updated)
	}
	config, err = bridge.Load(bridge.Path(root))
	if err != nil || len(config.Links) != 1 || config.Links[0].Note != "updated" {
		t.Fatalf("canonical update = %#v, %v", config, err)
	}
	removeResponse := do(http.MethodDelete, `{"id":"docs-code","revision":"`+updated.Revision+`"}`)
	if removeResponse.Code != http.StatusOK {
		t.Fatalf("remove = %d %q", removeResponse.Code, removeResponse.Body.String())
	}
	removed := decodeMutation(removeResponse)
	config, err = bridge.Load(bridge.Path(root))
	if err != nil || len(config.Links) != 0 || removed.Operation != "remove" || removed.Link.ID != "docs-code" || removed.Revision == updated.Revision {
		t.Fatalf("canonical remove = %#v, response %#v, %v", config, removed, err)
	}
}

func TestHandlerServesBoundedSnapshotTransitions(t *testing.T) {
	t.Parallel()
	transition := graphdiff.TransitionSet{Nodes: []graphdiff.Transition{{ID: "symbol", Status: "added"}}}
	value := graphdiff.Result{Schema: graphdiff.Schema, Baseline: graphdiff.Identity{Revision: "main", SnapshotDigest: "sha256:a"}, Head: graphdiff.Identity{Revision: "worktree", SnapshotDigest: "sha256:b"}, Transitions: &transition}
	service := &fixtureService{response: application.Response{Command: "diff graph", Diff: &value}}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}})
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(engine, fixtureToken, fixtureOrigin)
	request := httptest.NewRequest(http.MethodPost, fixtureOrigin+"/"+fixtureToken+"/api/diff", strings.NewReader(`{"base":"main","limit":10,"max_depth":2,"max_edges":20}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", fixtureOrigin)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	var decoded graphdiff.Result
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil || decoded.Schema != graphdiff.Schema || decoded.Transitions == nil || decoded.Transitions.Nodes[0].ID != "symbol" {
		t.Fatalf("transition response = %q %#v %v", response.Body.String(), decoded, err)
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
		`api/context`, `api/links`, `renderer.destroy`, `textContent`,
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
