package explorer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/dot"
	"github.com/TheFellow/weave/internal/graph"
	"golang.org/x/net/websocket"
)

// TestBrowserSmoke renders through the actual embedded D3, d3-graphviz, and
// WebAssembly assets when a Chromium-family browser is available. CI images
// without a browser retain the deterministic HTTP/static contract tests.
func TestBrowserSmoke(t *testing.T) {
	chrome := chromeExecutable()
	if chrome == "" {
		t.Skip("Chrome or Chromium is not installed")
	}
	service := browserService{}
	engine, err := New(service, application.Invocation{Arguments: []string{"focus"}, Limit: 20, MaxDepth: 2, MaxEdges: 40})
	if err != nil {
		t.Fatal(err)
	}
	running, err := Start(Config{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	defer running.Close(shutdownCtx)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	profile := t.TempDir()
	command := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--disable-background-networking",
		"--disable-component-update", "--disable-default-apps", "--no-first-run",
		"--window-size=1600,1000",
		"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0",
		"--remote-allow-origins=*", "--user-data-dir="+profile, "about:blank",
	)
	var browserLog bytes.Buffer
	command.Stdout, command.Stderr = &browserLog, &browserLog
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	port, err := devtoolsPort(ctx, filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		t.Fatalf("start browser debugging: %v\n%s", err, browserLog.String())
	}
	webSocketURL, err := pageWebSocket(ctx, port)
	if err != nil {
		t.Fatalf("find browser page: %v\n%s", err, browserLog.String())
	}
	config, err := websocket.NewConfig(webSocketURL, "http://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatalf("connect to browser: %v\n%s", err, browserLog.String())
	}
	defer connection.Close()
	client := &cdpClient{connection: connection}
	if _, err := client.call("Page.enable", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.call("Page.navigate", map[string]any{"url": running.URL}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(20 * time.Second)
	initialReady := false
	for time.Now().Before(deadline) {
		result, err := client.call("Runtime.evaluate", map[string]any{
			"expression":    `(() => ({ready: Boolean(document.querySelector("#graph svg")) && document.querySelector("#loading").hidden, error: document.querySelector("#error")?.textContent || ""}))()`,
			"returnByValue": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		var evaluated struct {
			Result struct {
				Value struct {
					Ready bool   `json:"ready"`
					Error string `json:"error"`
				} `json:"value"`
			} `json:"result"`
		}
		if err := json.Unmarshal(result, &evaluated); err != nil {
			t.Fatal(err)
		}
		if evaluated.Result.Value.Error != "" {
			t.Fatalf("browser explorer error: %s\n%s", evaluated.Result.Value.Error, browserLog.String())
		}
		if evaluated.Result.Value.Ready {
			initialReady = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !initialReady {
		t.Fatalf("browser did not render embedded Graphviz within 20 seconds\n%s", browserLog.String())
	}

	dependencyID := strconv.Quote(dot.NodeSVGID("dependency"))
	if _, err := client.call("Runtime.evaluate", map[string]any{
		"expression": `document.getElementById(` + dependencyID + `).dispatchEvent(new MouseEvent("click", {bubbles: true}))`,
	}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		expression := `(() => ({ready: document.querySelector("#focus-label")?.textContent === "Dependency" && document.querySelector("#loading").hidden && Boolean(document.getElementById(` + strconv.Quote(dot.NodeSVGID("leaf")) + `)) && !document.getElementById(` + strconv.Quote(dot.NodeSVGID("caller")) + `), error: document.querySelector("#error")?.textContent || ""}))()`
		result, err := client.call("Runtime.evaluate", map[string]any{
			"expression": expression, "returnByValue": true,
		})
		if err != nil {
			t.Fatal(err)
		}
		var evaluated struct {
			Result struct {
				Value struct {
					Ready bool   `json:"ready"`
					Error string `json:"error"`
				} `json:"value"`
			} `json:"result"`
		}
		if err := json.Unmarshal(result, &evaluated); err != nil {
			t.Fatal(err)
		}
		if evaluated.Result.Value.Error != "" {
			t.Fatalf("browser explorer transition error: %s\n%s", evaluated.Result.Value.Error, browserLog.String())
		}
		if evaluated.Result.Value.Ready {
			captureBrowserScreenshot(t, client)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("browser did not complete click-to-refocus enter/exit transition within 20 seconds\n%s", browserLog.String())
}

func captureBrowserScreenshot(t *testing.T, client *cdpClient) {
	t.Helper()
	path := os.Getenv("WEAVE_EXPLORER_BROWSER_SCREENSHOT")
	if path == "" {
		return
	}
	result, err := client.call("Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false})
	if err != nil {
		t.Fatal(err)
	}
	var screenshot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &screenshot); err != nil {
		t.Fatal(err)
	}
	content, err := base64.StdEncoding.DecodeString(screenshot.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

type browserService struct{}

func (browserService) Execute(_ context.Context, invocation application.Invocation) (application.Response, error) {
	if len(invocation.Arguments) != 1 {
		return application.Response{}, fmt.Errorf("browser fixture expected one target")
	}
	if invocation.Arguments[0] == "focus" {
		return graphResponse(), nil
	}
	if invocation.Arguments[0] != "dependency" {
		return application.Response{}, fmt.Errorf("browser fixture does not know %q", invocation.Arguments[0])
	}
	return application.Response{
		Schema: application.QuerySchema, Command: "graph",
		Nodes: []string{"dependency", "focus", "leaf"},
		Symbols: []graph.Symbol{
			{ID: "dependency", StableName: "example.Dependency", DisplayName: "Dependency", Kind: "function", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "focus", StableName: "example.Focus", DisplayName: "Focus", Kind: "function", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "leaf", StableName: "example.Leaf", DisplayName: "Leaf", Kind: "function", Provider: "scip:fixture", Evidence: graph.EvidenceExact},
		},
		Edges: []graph.Edge{
			{ID: "retained", From: "focus", To: "dependency", Kind: graph.EdgeCalls, Provider: "scip:fixture", Evidence: graph.EvidenceExact},
			{ID: "entered", From: "dependency", To: "leaf", Kind: graph.EdgeCalls, Provider: "scip:fixture", Evidence: graph.EvidenceExact},
		},
	}, nil
}

func chromeExecutable() string {
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	if runtime.GOOS == "darwin" {
		path := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func devtoolsPort(ctx context.Context, path string) (int, error) {
	for {
		content, err := os.ReadFile(path)
		if err == nil {
			line, _, _ := strings.Cut(string(content), "\n")
			port, err := strconv.Atoi(line)
			if err == nil && port > 0 {
				return port, nil
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func pageWebSocket(ctx context.Context, port int) (string, error) {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/list", port)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var targets []struct {
		Type      string `json:"type"`
		WebSocket string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return "", err
	}
	for _, target := range targets {
		if target.Type == "page" && target.WebSocket != "" {
			return target.WebSocket, nil
		}
	}
	return "", fmt.Errorf("browser exposed no page target")
}

type cdpClient struct {
	connection *websocket.Conn
	nextID     int
}

func (client *cdpClient) call(method string, parameters any) (json.RawMessage, error) {
	client.nextID++
	id := client.nextID
	if err := websocket.JSON.Send(client.connection, map[string]any{"id": id, "method": method, "params": parameters}); err != nil {
		return nil, err
	}
	for {
		var message struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := websocket.JSON.Receive(client.connection, &message); err != nil {
			return nil, err
		}
		if message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("browser %s: %s", method, message.Error.Message)
		}
		return message.Result, nil
	}
}
