package explorer

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	maxRequestBytes = 64 << 10
	// wasm-unsafe-eval permits WebAssembly compilation for the embedded Graphviz
	// runtime without permitting JavaScript eval or remote script execution.
	contentSecurityPolicy = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; child-src blob:; font-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

//go:embed assets/index.html assets/app.css assets/app.js assets/vendor/* assets/licenses/* assets/README.md assets/update-vendor.sh
var assets embed.FS

// Config controls one loopback explorer server.
type Config struct {
	Engine      *Engine
	Output      io.Writer
	OpenBrowser bool
	openURL     func(string) error
}

// Running is a started loopback server.
type Running struct {
	URL      string
	listener net.Listener
	server   *http.Server
	done     chan error
}

// Start binds an ephemeral IPv4 loopback port and starts serving.
func Start(config Config) (*Running, error) {
	if config.Engine == nil {
		return nil, fmt.Errorf("explorer engine is nil")
	}
	token, err := sessionToken()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback: %w", err)
	}
	origin := "http://" + listener.Addr().String()
	server := &http.Server{
		Handler:           newHandler(config.Engine, token, origin),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	running := &Running{
		URL: origin + "/" + token + "/", listener: listener, server: server,
		done: make(chan error, 1),
	}
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		running.done <- err
	}()
	return running, nil
}

// Run starts, announces, and serves the explorer until cancellation.
func Run(ctx context.Context, config Config) error {
	running, err := Start(config)
	if err != nil {
		return err
	}
	if config.Output != nil {
		fmt.Fprintf(config.Output, "Weave explorer: %s\nPress Ctrl-C to stop.\n", running.URL)
	}
	if config.OpenBrowser {
		open := config.openURL
		if open == nil {
			open = openBrowser
		}
		if err := open(running.URL); err != nil && config.Output != nil {
			fmt.Fprintf(config.Output, "Could not open a browser: %v\n", err)
		}
	}
	select {
	case err := <-running.done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := running.Close(shutdownCtx); err != nil {
			return err
		}
		return ctx.Err()
	}
}

// Close gracefully stops a running explorer.
func (running *Running) Close(ctx context.Context) error {
	return running.server.Shutdown(ctx)
}

func sessionToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create explorer session token: %w", err)
	}
	return hex.EncodeToString(value), nil
}

type handler struct {
	engine *Engine
	prefix string
	origin string
	host   string
}

func newHandler(engine *Engine, token, origin string) http.Handler {
	parsed, _ := url.Parse(origin)
	return &handler{engine: engine, prefix: "/" + token + "/", origin: origin, host: parsed.Host}
}

func (handler *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	secureHeaders(writer.Header())
	if !strings.EqualFold(request.Host, handler.host) {
		http.Error(writer, "forbidden host", http.StatusForbidden)
		return
	}
	if request.URL.RawQuery != "" || request.URL.Fragment != "" {
		http.NotFound(writer, request)
		return
	}
	switch request.URL.Path {
	case handler.prefix:
		handler.static(writer, request, "assets/index.html", "text/html; charset=utf-8")
	case handler.prefix + "assets/app.css":
		handler.static(writer, request, "assets/app.css", "text/css; charset=utf-8")
	case handler.prefix + "assets/app.js":
		handler.static(writer, request, "assets/app.js", "text/javascript; charset=utf-8")
	case handler.prefix + "assets/vendor/d3.min.js":
		handler.static(writer, request, "assets/vendor/d3.min.js", "text/javascript; charset=utf-8")
	case handler.prefix + "assets/vendor/graphviz.umd.js":
		handler.static(writer, request, "assets/vendor/graphviz.umd.js", "text/javascript; charset=utf-8")
	case handler.prefix + "assets/vendor/d3-graphviz.min.js":
		handler.static(writer, request, "assets/vendor/d3-graphviz.min.js", "text/javascript; charset=utf-8")
	case handler.prefix + "api/graph":
		handler.graph(writer, request)
	case handler.prefix + "api/config":
		handler.config(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (handler *handler) config(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	initial, err := normalizeRequest(handler.engine.InitialRequest())
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]string{"error": "invalid explorer configuration"})
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Schema  string  `json:"schema"`
		Initial Request `json:"initial"`
	}{Schema: Schema, Initial: initial})
}

func (handler *handler) static(writer http.ResponseWriter, request *http.Request, name, contentType string) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	content, err := assets.ReadFile(name)
	if err != nil {
		http.Error(writer, "asset unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Length", fmt.Sprint(len(content)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func (handler *handler) graph(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if origin := request.Header.Get("Origin"); origin != "" && origin != handler.origin {
		http.Error(writer, "forbidden origin", http.StatusForbidden)
		return
	}
	if site := request.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		http.Error(writer, "forbidden fetch site", http.StatusForbidden)
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		http.Error(writer, "content type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var graphRequest Request
	if err := decoder.Decode(&graphRequest); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(writer, "invalid JSON request", http.StatusBadRequest)
		return
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(writer, "request must contain one JSON object", http.StatusBadRequest)
		return
	}
	result, err := handler.engine.Query(request.Context(), graphRequest)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func secureHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store, max-age=0")
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}

func openBrowser(target string) error {
	var name string
	var arguments []string
	switch runtime.GOOS {
	case "darwin":
		name, arguments = "open", []string{target}
	case "windows":
		name, arguments = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		name, arguments = "xdg-open", []string{target}
	}
	if err := exec.Command(name, arguments...).Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
