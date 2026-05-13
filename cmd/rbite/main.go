package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"errors"
	"regexp"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/joho/godotenv"
	qrterminal "github.com/mdp/qrterminal/v3"
	"gopkg.in/yaml.v3"
)

//go:embed tunnel-art.txt
var tunnelArt string

//go:embed web/index.html
var fileServerHTML string

// errSessionExpired is returned when the refresh token is also invalid, requiring re-login.
var errSessionExpired = errors.New("session expired")

// localhostPattern matches any localhost-style URL origin (scheme + host + optional port).
// Captured text is replaced wholesale with the tunnel's public URL.
var localhostPattern = regexp.MustCompile(`https?://(localhost|127\.0\.0\.1|\[::1\]|0\.0\.0\.0)(:\d+)?`)

// rewriteConfig carries the parameters for localhost-URL rewriting in proxied responses.
// A nil pointer means rewriting is disabled.
type rewriteConfig struct {
	publicURL string // e.g., "https://abc123.t.rbite.dev"
}

// portedPublicURL inserts -{port} after the first subdomain label of publicURL.
// e.g. ("https://foo.t.rbite.dev", "8080") → "https://foo-8080.t.rbite.dev"
// Returns publicURL unchanged when port is empty.
func portedPublicURL(publicURL, port string) string {
	if port == "" {
		return publicURL
	}
	schemeEnd := strings.Index(publicURL, "://")
	if schemeEnd < 0 {
		return publicURL
	}
	rest := publicURL[schemeEnd+3:]
	dotIdx := strings.IndexByte(rest, '.')
	if dotIdx < 0 {
		return publicURL + "-" + port
	}
	return publicURL[:schemeEnd+3] + rest[:dotIdx] + "-" + port + rest[dotIdx:]
}

// isTextContentType reports whether ct is a text-like MIME type whose body
// can safely be scanned for localhost URL references.
func isTextContentType(ct string) bool {
	// Strip parameters (e.g., "; charset=utf-8").
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	ct = strings.ToLower(ct)
	switch ct {
	case "text/html", "text/plain", "text/css", "text/javascript",
		"text/xml", "application/json", "application/xml",
		"application/javascript", "application/xhtml+xml", "image/svg+xml":
		return true
	}
	return strings.HasPrefix(ct, "text/")
}

// errTunnelTerminated is returned when the server explicitly closes the tunnel
// with close code 4000, indicating the client must not attempt to reconnect.
var errTunnelTerminated = errors.New("tunnel terminated by server")

// errTerminated is a typed variant of errTunnelTerminated that carries the
// server's close reason (e.g. "timeout", "max_transfer").  It satisfies
// errors.Is(e, errTunnelTerminated) so existing callers need not change.
type errTerminated struct{ Reason string }

func (e *errTerminated) Error() string {
	if e.Reason != "" {
		return "tunnel terminated: " + e.Reason
	}
	return "tunnel terminated"
}

func (e *errTerminated) Is(target error) bool { return target == errTunnelTerminated }

// Version information — set via ldflags at build time.
var (
	Version             = "dev"
	BuildTime           = "unknown"
	GitCommit           = "unknown"
	DefaultAPIHostname      = ""
	DefaultAPIURL           = ""
	DefaultHQURL            = ""
	DefaultOAuthClientID    = ""
	DefaultOAuthScopes      = "openid email profile"
	DefaultOAuthCallbackURL = "http://localhost:7332/auth/callback"
)

type Config struct {
	ClientID     string `yaml:"clientId"`
	AccessToken  string `yaml:"accessToken,omitempty"`
	RefreshToken string `yaml:"refreshToken,omitempty"`
	AccountID    string `yaml:"accountId,omitempty"`

	// Persistent tunnel resume state
	LastSessionType string `yaml:"lastSessionType,omitempty"` // "ephemeral" or "permanent"
	LastTunnelID    string `yaml:"lastTunnelId,omitempty"`
	LastLocalPort   int    `yaml:"lastLocalPort,omitempty"`
}

// configPath returns the absolute path to ~/.config/rbite/config.yaml.
func configPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "rbite", "config.yaml"), nil
}

// saveConfig persists cfg to the config file with mode 0600.
func saveConfig(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("could not write config file: %w", err)
	}
	// Ensure permissions are 0600 even if the file already existed with looser perms.
	return os.Chmod(path, 0o600)
}

// loadOrCreateConfig reads ~/.config/rbite/config.yaml, creating it with a
// fresh UUIDv4 clientId if it does not already exist.
func loadOrCreateConfig() (*Config, error) {
	cfgPath, err := configPath()
	if err != nil {
		return nil, err
	}
	cfgDir := filepath.Dir(cfgPath)

	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("could not read config file: %w", err)
	}

	var cfg Config
	if os.IsNotExist(err) {
		// Create directory and file with a new clientId.
		if mkErr := os.MkdirAll(cfgDir, 0o755); mkErr != nil {
			return nil, fmt.Errorf("could not create config directory: %w", mkErr)
		}
		cfg.ClientID = uuid.New().String()
		if saveErr := saveConfig(&cfg); saveErr != nil {
			return nil, saveErr
		}
		fmt.Printf("Created default configuration file in ~/.config/rbite/config.yaml\n")
		return &cfg, nil
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("could not parse config file: %w", err)
	}

	// Populate missing clientId and persist.
	if cfg.ClientID == "" {
		cfg.ClientID = uuid.New().String()
		if saveErr := saveConfig(&cfg); saveErr != nil {
			return nil, saveErr
		}
	}

	return &cfg, nil
}

var errSessionConflict = errors.New("session conflict")

type CreateEphemeralRequest struct {
	Port     int    `json:"port"`
	ClientID string `json:"client_id"`
}

type CreateEphemeralResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ActiveSessionResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	Port      int       `json:"port"`
}

// buildDefaultServerURL constructs the default server URL from API_HOSTNAME.
// The compile-time value (set via ldflags) takes precedence, then the runtime
// env var, then falls back to localhost for local development.
func buildDefaultServerURL() string {
	if DefaultAPIHostname != "" {
		if strings.HasPrefix(DefaultAPIHostname, "http://") || strings.HasPrefix(DefaultAPIHostname, "https://") {
			return DefaultAPIHostname
		}
		return "https://" + DefaultAPIHostname
	}
	if h := getEnv("API_HOSTNAME", ""); h != "" {
		if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
			return h
		}
		return "https://" + h
	}
	return "http://localhost:8080"
}

// resolveAPIURL returns the effective REQUESTBITE_API_URL, preferring (in order):
// the runtime env var, the compile-time default, then the tunnel server URL fallback.
func resolveAPIURL(serverURL string) string {
	if v := getEnv("REQUESTBITE_API_URL", ""); v != "" {
		return v
	}
	if DefaultAPIURL != "" {
		return DefaultAPIURL
	}
	return serverURL
}

func printHelp() {
	defaultServer := buildDefaultServerURL()
	fmt.Printf("\n\033[38;5;208mRequestBite RBite CLI\033[0m ⚡ v%s\n\n", Version)
	fmt.Println("Usage:")
	fmt.Printf("  rbite [options]\n\n")
	fmt.Println("Options:")
	fmt.Println("\nAccount Mgmt\n============")
  fmt.Printf("      --login                 Log in via browser\n")
  fmt.Printf("      --switch-accounts       Switch the active account\n")
  fmt.Printf("      --whoami                Show logged-in user and account details\n")
	fmt.Println("\nRequest Views\n=============")
  fmt.Printf("      --views-list            List active inspector views for the current account\n")
  fmt.Printf("      --views-add [name]      Create a new inspector view (name is optional)\n")
  fmt.Printf("      --views-tail [view ID]  Stream live requests for a view (prompts if no ID given)\n")
  fmt.Printf("      --views-open [view ID]  Open a view's capture URL in the browser (prompts if no ID given)\n")
	fmt.Println("\nTunnel Mgmt\n===========")
  fmt.Printf("  -e, --ephemeral int         Port to expose via ephemeral tunnel; overrides dynamic routing for -t\n")
  fmt.Printf("  -r, --resume                Resume the last tunnel session (ephemeral or permanent)\n")
  fmt.Printf("      --localhost-rewrite      Rewrite localhost URLs in responses to the tunnel's public URL (use with -t only)\n")
  fmt.Printf("      --show-qr               Print a QR code of the tunnel URL (use with -e or -r)\n")
  fmt.Printf("      --tunnel-server string  Tunnel server URL (default %q)\n\n", defaultServer)
	fmt.Printf("  -f, --files string          Share a local directory via ephemeral tunnel (built-in file browser, read-only)\n")
	fmt.Printf(" -fw, --files-write string    Share a local directory via ephemeral tunnel with upload support (read/write)\n")
	fmt.Printf("  -p, --passphrase string     Set a passphrase for Basic Auth on file server (use with -f or -fw)\n\n")
	fmt.Printf("  -w, --web-server string     Serve a local directory via ephemeral tunnel (through built-in web server)\n")
	fmt.Printf("      --spa [index-file]      Enable SPA mode: serve index-file for all unmatched paths (default: index.html)\n\n")
  fmt.Printf("  -t, --tunnels string        Connect a permanent tunnel by name\n")
  fmt.Printf("      --tunnels-list          List tunnels for the current account\n")
	fmt.Println("\nOther\n=====")
  fmt.Printf("      --no-upgrade-check      Disable automatic upgrade check\n")
  fmt.Printf("      --uninstall             Uninstall rbite\n")
	fmt.Printf("  -h, --help                  Show help information\n")
	fmt.Printf("  -v, --version               Show version information\n")
}

// safePath validates that relPath stays within rootPath and returns the absolute path.
func safePath(rootPath, relPath string) (string, error) {
	if strings.ContainsRune(relPath, 0) {
		return "", fmt.Errorf("invalid path")
	}
	clean := filepath.Clean(relPath)
	joined := filepath.Join(rootPath, clean)
	rel, err := filepath.Rel(rootPath, joined)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path traversal not allowed")
	}
	return joined, nil
}

type fileEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

func fileListHandler(rootPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.URL.Query().Get("path")
		abs, err := safePath(rootPath, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		info, err := os.Stat(abs)
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil || !info.IsDir() {
			http.Error(w, "not a directory", http.StatusBadRequest)
			return
		}
		dirEntries, err := os.ReadDir(abs)
		if err != nil {
			http.Error(w, "cannot read directory", http.StatusInternalServerError)
			return
		}
		entries := make([]fileEntry, 0, len(dirEntries))
		for _, de := range dirEntries {
			fi, err := de.Info()
			if err != nil {
				continue
			}
			entries = append(entries, fileEntry{
				Name:    de.Name(),
				IsDir:   de.IsDir(),
				Size:    fi.Size(),
				ModTime: fi.ModTime(),
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"path": rel, "entries": entries})
	}
}

func fileUploadHandler(rootPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.URL.Query().Get("path")
		abs, err := safePath(rootPath, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		info, err := os.Stat(abs)
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil || !info.IsDir() {
			http.Error(w, "not a directory", http.StatusBadRequest)
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "could not parse form: "+err.Error(), http.StatusBadRequest)
			return
		}
		fileHeaders := r.MultipartForm.File["file"]
		if len(fileHeaders) == 0 {
			http.Error(w, "no file provided", http.StatusBadRequest)
			return
		}
		saved := make([]string, 0, len(fileHeaders))
		for _, fh := range fileHeaders {
			name := filepath.Base(fh.Filename)
			if name == "." || name == ".." || name == "/" {
				http.Error(w, "invalid filename", http.StatusBadRequest)
				return
			}
			dst, err := safePath(abs, name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			src, err := fh.Open()
			if err != nil {
				http.Error(w, "could not open uploaded file", http.StatusInternalServerError)
				return
			}
			out, err := os.Create(dst)
			if err != nil {
				src.Close()
				http.Error(w, "could not create file", http.StatusInternalServerError)
				return
			}
			_, cpErr := io.Copy(out, src)
			src.Close()
			out.Close()
			if cpErr != nil {
				http.Error(w, "could not write file", http.StatusInternalServerError)
				return
			}
			saved = append(saved, name)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"saved": saved})
	}
}

func fileDownloadHandler(rootPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.URL.Query().Get("path")
		abs, err := safePath(rootPath, rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		info, err := os.Stat(abs)
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil || info.IsDir() {
			http.Error(w, "not a file", http.StatusBadRequest)
			return
		}
		f, err := os.Open(abs)
		if err != nil {
			http.Error(w, "cannot open file", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(abs)+`"`)
		http.ServeContent(w, r, filepath.Base(abs), info.ModTime(), f)
	}
}

// basicAuthMiddleware wraps an http.Handler with Basic Authentication.
// Note: We don't set WWW-Authenticate header to prevent browser's built-in auth dialog.
// The web client handles auth with its own modal.
func basicAuthMiddleware(handler http.Handler, username, password string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, hasAuth := r.BasicAuth()
		if !hasAuth || user != username || pass != password {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}
}

func startFileHTTPServer(rootPath string, writable bool, passphrase string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ls", fileListHandler(rootPath))
	mux.HandleFunc("/api/download", fileDownloadHandler(rootPath))
	if writable {
		mux.HandleFunc("/api/upload", fileUploadHandler(rootPath))
	}
	writableStr := "false"
	if writable {
		writableStr = "true"
	}
	hasAuth := passphrase != ""
	authStr := "false"
	if hasAuth {
		authStr = "true"
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := strings.Replace(fileServerHTML, "{{VERSION}}", Version, 1)
		html = strings.Replace(html, "{{WRITABLE}}", writableStr, 1)
		html = strings.Replace(html, "{{AUTH_REQUIRED}}", authStr, 1)
		fmt.Fprint(w, html)
	})
	
	var handler http.Handler = mux
	if hasAuth {
		// Apply Basic Auth only to API endpoints, not the root page
		// The root page loads without auth, then JS on the page handles API auth
		apiMux := http.NewServeMux()
		apiMux.HandleFunc("/api/ls", basicAuthMiddleware(http.HandlerFunc(fileListHandler(rootPath)), "", passphrase))
		apiMux.HandleFunc("/api/download", basicAuthMiddleware(http.HandlerFunc(fileDownloadHandler(rootPath)), "", passphrase))
		if writable {
			apiMux.HandleFunc("/api/upload", basicAuthMiddleware(http.HandlerFunc(fileUploadHandler(rootPath)), "", passphrase))
		}
		// For non-API routes, use the original mux (which has the root handler)
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				apiMux.ServeHTTP(w, r)
			} else {
				mux.ServeHTTP(w, r)
			}
		})
	}
	
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln) //nolint:errcheck
	return ln, nil
}

func runFileServer(rootPath string, showQR bool, serverURL string, writable bool, passphrase string) error {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(absRoot)
	if os.IsNotExist(err) {
		return fmt.Errorf("path does not exist: %s", absRoot)
	}
	if err != nil {
		return fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is a file, not a directory; pass a directory path", absRoot)
	}

	cfg, err := loadOrCreateConfig()
	if err != nil {
		return err
	}

	ln, err := startFileHTTPServer(absRoot, writable, passphrase)
	if err != nil {
		return fmt.Errorf("could not start file server: %w", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	ephemeralResp, err := createEphemeralTunnel(serverURL, port, cfg.ClientID)
	if err != nil {
		if errors.Is(err, errSessionConflict) {
			return fmt.Errorf("this client already has a session open. Only 1 ephemeral session is possible at once")
		}
		return fmt.Errorf("failed to create ephemeral tunnel: %w", err)
	}

	tunnelURL := ephemeralResp.URL
	expiresAt := ephemeralResp.ExpiresAt
	expiresIn := int(time.Until(expiresAt).Minutes())
	if time.Until(expiresAt) > time.Duration(expiresIn)*time.Minute {
		expiresIn++
	}

	fmt.Printf("\n%s\n", tunnelArt)
	fmt.Printf("Sharing directory: %s\n", absRoot)
	if writable {
		fmt.Printf("Mode: read/write (uploads enabled)\n")
	}
	fmt.Printf("Ephemeral tunnel created. Expires at %s (in %d minutes).\n", expiresAt.Local().Format("15:04:05"), expiresIn)
	fmt.Printf("> File browser: https://%s\n", tunnelURL)
	fmt.Printf("Press Ctrl+C to stop\n\n")

	if showQR {
		printQR("https://" + tunnelURL)
	}

	cfg.LastSessionType = "ephemeral"
	cfg.LastTunnelID = ""
	cfg.LastLocalPort = port
	_ = saveConfig(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Println()
		cancel()
	}()

	localAddr := fmt.Sprintf("localhost:%d", port)
	connectEphemeralWithReconnect(ctx, serverURL, cfg.ClientID, localAddr, expiresAt, nil)
	printSessionStats(serverURL, cfg.ClientID)
	return nil
}

// spaHandler wraps a file server so that requests to non-existent paths fall
// back to indexFile (e.g. "index.html") instead of returning a 404.
func spaHandler(rootPath, indexFile string, fs http.Handler) http.Handler {
	indexPath := filepath.Join(rootPath, indexFile)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		absPath, err := safePath(rootPath, r.URL.Path)
		if err != nil {
			fs.ServeHTTP(w, r)
			return
		}
		if _, err := os.Stat(absPath); os.IsNotExist(err) {
			http.ServeFile(w, r, indexPath)
			return
		}
		fs.ServeHTTP(w, r)
	})
}

func startWebHTTPServer(rootPath, spaIndex string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	fs := http.FileServer(http.Dir(rootPath))
	var handler http.Handler
	if spaIndex == "" {
		handler = fs
	} else {
		handler = spaHandler(rootPath, spaIndex, fs)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln) //nolint:errcheck
	return ln, nil
}

func runWebServer(rootPath string, showQR bool, serverURL, spaIndex string) error {
	absRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}
	info, err := os.Stat(absRoot)
	if os.IsNotExist(err) {
		return fmt.Errorf("path does not exist: %s", absRoot)
	}
	if err != nil {
		return fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is a file, not a directory; pass a directory path", absRoot)
	}

	cfg, err := loadOrCreateConfig()
	if err != nil {
		return err
	}

	ln, err := startWebHTTPServer(absRoot, spaIndex)
	if err != nil {
		return fmt.Errorf("could not start web server: %w", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	ephemeralResp, err := createEphemeralTunnel(serverURL, port, cfg.ClientID)
	if err != nil {
		if errors.Is(err, errSessionConflict) {
			return fmt.Errorf("this client already has a session open. Only 1 ephemeral session is possible at once")
		}
		return fmt.Errorf("failed to create ephemeral tunnel: %w", err)
	}

	tunnelURL := ephemeralResp.URL
	expiresAt := ephemeralResp.ExpiresAt
	expiresIn := int(time.Until(expiresAt).Minutes())
	if time.Until(expiresAt) > time.Duration(expiresIn)*time.Minute {
		expiresIn++
	}

	fmt.Printf("\n%s\n", tunnelArt)
	fmt.Printf("Serving directory: %s\n", absRoot)
	if spaIndex != "" {
		fmt.Printf("Mode: SPA (fallback to %s)\n", spaIndex)
	}
	fmt.Printf("Ephemeral tunnel created. Expires at %s (in %d minutes).\n", expiresAt.Local().Format("15:04:05"), expiresIn)
	fmt.Printf("> Web server: https://%s\n", tunnelURL)
	fmt.Printf("Press Ctrl+C to stop\n\n")

	if showQR {
		printQR("https://" + tunnelURL)
	}

	cfg.LastSessionType = "ephemeral"
	cfg.LastTunnelID = ""
	cfg.LastLocalPort = port
	_ = saveConfig(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Println()
		cancel()
	}()

	localAddr := fmt.Sprintf("localhost:%d", port)
	connectEphemeralWithReconnect(ctx, serverURL, cfg.ClientID, localAddr, expiresAt, nil)
	printSessionStats(serverURL, cfg.ClientID)
	return nil
}

func main() {
	// Load .env file for local development only.
	// In production builds the compile-time defaults are baked in via ldflags,
	// so we skip .env loading to prevent local files from overriding them.
	if DefaultAPIHostname == "" && DefaultAPIURL == "" {
		_ = godotenv.Load()
	}

	// Command line flags
	var (
		ephemeralPort  int
		tunnelID       string
		showVersion    bool
		showHelp       bool
		resume         bool
		serverURL      string
		noUpgradeCheck bool
		uninstall      bool
		loginMode      bool
		switchAccounts bool
		whoami         bool
		listViews      bool
		tailViewID     string
		openViewID     string
		addViewName    string
		showQR           bool
		filesPath        string
		filesWritePath   string
		webServerPath    string
		spaIndexFile     string
		listTunnels      bool
		localhostRewrite bool
		passphrase       string
	)
	defaultServer := buildDefaultServerURL()

	flag.IntVar(&ephemeralPort, "e", 0, "")
	flag.IntVar(&ephemeralPort, "ephemeral", 0, "")
	flag.StringVar(&tunnelID, "t", "", "")
	flag.StringVar(&tunnelID, "tunnels", "", "")
	flag.BoolVar(&showVersion, "v", false, "")
	flag.BoolVar(&showVersion, "version", false, "")
	flag.BoolVar(&showHelp, "h", false, "")
	flag.BoolVar(&showHelp, "help", false, "")
	flag.BoolVar(&resume, "r", false, "")
	flag.BoolVar(&resume, "resume", false, "")
	flag.StringVar(&serverURL, "tunnel-server", defaultServer, "")
	flag.BoolVar(&noUpgradeCheck, "no-upgrade-check", false, "")
	flag.BoolVar(&uninstall, "uninstall", false, "")
	flag.BoolVar(&showQR, "show-qr", false, "")
	flag.StringVar(&filesPath, "f", "", "")
	flag.StringVar(&filesPath, "files", "", "")
	flag.StringVar(&filesWritePath, "fw", "", "")
	flag.StringVar(&filesWritePath, "files-write", "", "")
	flag.BoolVar(&loginMode, "login", false, "")
	flag.BoolVar(&switchAccounts, "switch-accounts", false, "")
	flag.BoolVar(&whoami, "whoami", false, "")
	flag.BoolVar(&listViews, "views-list", false, "")
	flag.BoolVar(&listTunnels, "tunnels-list", false, "")
	flag.StringVar(&tailViewID, "views-tail", "", "")
	flag.StringVar(&openViewID, "views-open", "", "")
	flag.StringVar(&addViewName, "views-add", "", "")
	flag.BoolVar(&localhostRewrite, "localhost-rewrite", false, "")
	flag.StringVar(&passphrase, "p", "", "")
	flag.StringVar(&passphrase, "passphrase", "", "")
	flag.StringVar(&webServerPath, "w", "", "")
	flag.StringVar(&webServerPath, "web-server", "", "")
	flag.StringVar(&spaIndexFile, "spa", "", "")
	flag.Usage = printHelp

	// Pre-scan os.Args to detect --views-tail / --views-open / --views-add without a
	// value, since flag.StringVar requires a value. Strip bare flags and record intent.
	viewTailNoID := false
	viewOpenNoID := false
	viewAddNoName := false
	spaNoFile := false
	filteredArgs := make([]string, 0, len(os.Args)-1)
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		hasValue := i+1 < len(os.Args) && !strings.HasPrefix(os.Args[i+1], "-")
		switch {
		case arg == "--views-tail" || arg == "-views-tail":
			if hasValue {
				filteredArgs = append(filteredArgs, arg)
			} else {
				viewTailNoID = true
			}
		case arg == "--views-open" || arg == "-views-open":
			if hasValue {
				filteredArgs = append(filteredArgs, arg)
			} else {
				viewOpenNoID = true
			}
		case arg == "--views-add" || arg == "-views-add":
			if hasValue {
				filteredArgs = append(filteredArgs, arg)
			} else {
				viewAddNoName = true
			}
		case arg == "--spa" || arg == "-spa":
			if hasValue {
				filteredArgs = append(filteredArgs, arg)
			} else {
				spaNoFile = true
			}
		default:
			filteredArgs = append(filteredArgs, arg)
		}
	}
	flag.CommandLine.Parse(filteredArgs)

	// Show version
	if showVersion {
		fmt.Printf("rbite v%s\n", Version)
		if BuildTime != "unknown" {
			fmt.Printf("Built: %s\n", BuildTime)
		}
		if GitCommit != "unknown" {
			fmt.Printf("Commit: %s\n", GitCommit)
		}
		os.Exit(0)
	}

	// Show help
	if showHelp {
		printHelp()
		os.Exit(0)
	}

	// Uninstall flow
	if uninstall {
		runUninstall()
		os.Exit(0)
	}

	// Login flow
	if loginMode {
		apiURL := resolveAPIURL(serverURL)
		if err := runLogin(apiURL); err != nil {
			log.Fatalf("Login failed: %v", err)
		}
		os.Exit(0)
	}

	// Tail view
	if tailViewID != "" || viewTailNoID {
		apiURL := resolveAPIURL(serverURL)
		var id string
		if err := runWithAutoRelogin(apiURL, func() error {
			if tailViewID != "" {
				id = tailViewID
				return nil
			}
			var e error
			id, e = selectView(apiURL, "tail")
			return e
		}); err != nil {
			log.Fatalf("View selection failed: %v", err)
		}
		if err := runWithAutoRelogin(apiURL, func() error { return runTailView(apiURL, id) }); err != nil {
			log.Fatalf("Tail view failed: %v", err)
		}
		os.Exit(0)
	}

	// Open view in browser
	if openViewID != "" || viewOpenNoID {
		apiURL := resolveAPIURL(serverURL)
		var id string
		if err := runWithAutoRelogin(apiURL, func() error {
			if openViewID != "" {
				id = openViewID
				return nil
			}
			var e error
			id, e = selectView(apiURL, "open")
			return e
		}); err != nil {
			log.Fatalf("View selection failed: %v", err)
		}
		if err := runWithAutoRelogin(apiURL, func() error { return runOpenView(apiURL, id) }); err != nil {
			log.Fatalf("Open view failed: %v", err)
		}
		os.Exit(0)
	}

	// Add view
	if addViewName != "" || viewAddNoName {
		apiURL := resolveAPIURL(serverURL)
		name := addViewName // may be empty — runAddView handles that
		if err := runWithAutoRelogin(apiURL, func() error { return runAddView(apiURL, name) }); err != nil {
			log.Fatalf("Add view failed: %v", err)
		}
		os.Exit(0)
	}

	// List views
	if listViews {
		apiURL := resolveAPIURL(serverURL)
		if err := runWithAutoRelogin(apiURL, func() error { return runListViews(apiURL) }); err != nil {
			log.Fatalf("List views failed: %v", err)
		}
		os.Exit(0)
	}

	// List tunnels
	if listTunnels {
		apiURL := resolveAPIURL(serverURL)
		if err := runWithAutoRelogin(apiURL, func() error { return runListTunnels(apiURL) }); err != nil {
			log.Fatalf("List tunnels failed: %v", err)
		}
		os.Exit(0)
	}

	// Switch accounts
	if switchAccounts {
		apiURL := resolveAPIURL(serverURL)
		if err := runWithAutoRelogin(apiURL, func() error { return runSwitchAccounts(apiURL) }); err != nil {
			log.Fatalf("Switch accounts failed: %v", err)
		}
		os.Exit(0)
	}

	// Whoami
	if whoami {
		apiURL := resolveAPIURL(serverURL)
		if err := runWhoami(apiURL); err != nil {
			log.Fatalf("whoami failed: %v", err)
		}
		os.Exit(0)
	}

	// File share (read-only)
	if filesPath != "" {
		if ephemeralPort != 0 || resume {
			log.Fatal("Error: -f/--files cannot be used together with -e/--ephemeral or --resume")
		}
		if !noUpgradeCheck && !isRunningInDevelopment() {
			checkForUpdates()
		}
		if err := runFileServer(filesPath, showQR, serverURL, false, passphrase); err != nil {
			log.Fatalf("File server failed: %v", err)
		}
		os.Exit(0)
	}

	// File share (read/write — uploads enabled)
	if filesWritePath != "" {
		if ephemeralPort != 0 || resume {
			log.Fatal("Error: -fw/--files-write cannot be used together with -e/--ephemeral or --resume")
		}
		if !noUpgradeCheck && !isRunningInDevelopment() {
			checkForUpdates()
		}
		if err := runFileServer(filesWritePath, showQR, serverURL, true, passphrase); err != nil {
			log.Fatalf("File server failed: %v", err)
		}
		os.Exit(0)
	}

	// Plain web server (no built-in UI)
	if webServerPath != "" {
		if ephemeralPort != 0 || resume {
			log.Fatal("Error: -w/--web-server cannot be used together with -e/--ephemeral or --resume")
		}
		effectiveSPAIndex := ""
		if spaNoFile {
			effectiveSPAIndex = "index.html"
		} else if spaIndexFile != "" {
			effectiveSPAIndex = spaIndexFile
		}
		if !noUpgradeCheck && !isRunningInDevelopment() {
			checkForUpdates()
		}
		if err := runWebServer(webServerPath, showQR, serverURL, effectiveSPAIndex); err != nil {
			log.Fatalf("Web server failed: %v", err)
		}
		os.Exit(0)
	}

	if spaNoFile || spaIndexFile != "" {
		log.Fatal("Error: --spa can only be used with -w/--web-server")
	}

	// Check for updates (unless disabled or running in development)
	if !noUpgradeCheck && !isRunningInDevelopment() {
		checkForUpdates()
	}

	cfg, err := loadOrCreateConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	apiURL := resolveAPIURL(serverURL)

	if localhostRewrite && tunnelID == "" {
		log.Fatal("Error: --localhost-rewrite can only be used with -t/--tunnels")
	}

	// ── Permanent tunnel ──────────────────────────────────────────────────────
	if tunnelID != "" {
		if resume {
			log.Fatal("Error: --tunnel and --resume cannot be used together; use --resume alone to reconnect the last session")
		}
		runPermanentTunnel(serverURL, apiURL, tunnelID, ephemeralPort, showQR, localhostRewrite, cfg)
		os.Exit(0)
	}

	// ── Resume ────────────────────────────────────────────────────────────────
	if resume {
		if cfg.LastSessionType == "permanent" && cfg.LastTunnelID != "" && cfg.LastLocalPort != 0 {
			runPermanentTunnel(serverURL, apiURL, cfg.LastTunnelID, cfg.LastLocalPort, showQR, false, cfg)
			os.Exit(0)
		}
		// Fall through to ephemeral resume.
		if ephemeralPort != 0 {
			log.Fatal("Error: --resume and --ephemeral cannot be used together")
		}
	}

	// ── Ephemeral tunnel ──────────────────────────────────────────────────────
	if !resume && ephemeralPort == 0 {
		printHelp()
		os.Exit(0)
	}

	clientID := cfg.ClientID

	var tunnelURL string
	var expiresAt time.Time

	if resume {
		activeResp, err := getActiveSession(serverURL, clientID)
		if err != nil {
			if errors.Is(err, errSessionConflict) {
				fmt.Fprintln(os.Stderr, "Session is already active — no need to resume.")
			} else {
				fmt.Fprintf(os.Stderr, "Cannot resume: %v\n", err)
			}
			os.Exit(1)
		}
		ephemeralPort = activeResp.Port
		tunnelURL = activeResp.URL
		expiresAt = activeResp.ExpiresAt

		expiresIn := int(time.Until(expiresAt).Minutes())
		if time.Until(expiresAt) > time.Duration(expiresIn)*time.Minute {
			expiresIn++
		}
		fmt.Printf("Resuming previous session. Expires at %s (in %d minutes).\n", expiresAt.Local().Format("15:04:05"), expiresIn)
		fmt.Printf("> Internet endpoint: https://%s\n", tunnelURL)
		fmt.Printf("> Local service: http://localhost:%d\n", ephemeralPort)
		fmt.Printf("Press Ctrl+C to stop\n\n")
		if showQR {
			printQR("https://" + tunnelURL)
		} else {
			fmt.Printf("Want a QR code to easily open the tunnel endpoint on your phone?\n")
			fmt.Printf(" - Hit Ctrl+C and paste \"rbite --resume --show-qr\" in your terminal.\n\n")
		}
	} else {
		ephemeralResp, err := createEphemeralTunnel(serverURL, ephemeralPort, clientID)
		if err != nil {
			if errors.Is(err, errSessionConflict) {
				fmt.Fprintln(os.Stderr, "This client already has a session open. Only 1 ephemeral session is possible at once.")
			} else {
				fmt.Fprintf(os.Stderr, "Failed to create ephemeral tunnel: %v\n", err)
			}
			os.Exit(1)
		}
		tunnelURL = ephemeralResp.URL
		expiresAt = ephemeralResp.ExpiresAt

		expiresIn := int(time.Until(expiresAt).Minutes())
		if time.Until(expiresAt) > time.Duration(expiresIn)*time.Minute {
			expiresIn++
		}
		fmt.Printf("\n%s\n", tunnelArt)
		fmt.Printf("Ephemeral tunnel created. Expires at %s (in %d minutes).\n", expiresAt.Local().Format("15:04:05"), expiresIn)
		fmt.Printf("> Internet endpoint: https://%s\n", tunnelURL)
		fmt.Printf("> Local service: http://localhost:%d\n", ephemeralPort)
		fmt.Printf("Press Ctrl+C to stop\n\n")
		if showQR {
			printQR("https://" + tunnelURL)
		} else {
			fmt.Printf("Want a QR code to easily open the tunnel endpoint on your phone?\n")
			fmt.Printf(" - Hit Ctrl+C and paste \"rbite --resume --show-qr\" in your terminal.\n\n")
		}

		// Persist ephemeral as last session so --resume works next time.
		cfg.LastSessionType = "ephemeral"
		cfg.LastTunnelID = ""
		cfg.LastLocalPort = ephemeralPort
		_ = saveConfig(cfg)
	}

	// Cancel the context on Ctrl-C so connectToTunnelServer returns cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Println()
		cancel()
	}()

	// Connect to tunnel server; blocks until the session ends.
	localAddr := fmt.Sprintf("localhost:%d", ephemeralPort)
	connectEphemeralWithReconnect(ctx, serverURL, clientID, localAddr, expiresAt, nil)

	// Fetch and print session stats once the tunnel is done.
	printSessionStats(serverURL, clientID)
}

// isRunningInDevelopment detects if the binary is running in a development environment (e.g., with Air)
func isRunningInDevelopment() bool {
	if os.Getenv("AIR_WATCH") != "" || os.Getenv("AIR_TMP_DIR") != "" {
		return true
	}
	execPath, err := os.Executable()
	if err == nil && strings.Contains(execPath, "tmp") {
		return true
	}
	if Version == "dev" {
		return true
	}
	return false
}

// getRemoteVersion fetches the latest released version from the GitHub releases API.
func getRemoteVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/requestbite/rbite/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	// Strip leading 'v' to match the Version variable format.
	return strings.TrimPrefix(release.TagName, "v"), nil
}

// checkForUpdates checks if a new version is available and prompts the user to install it.
func checkForUpdates() {
	remoteVersion, err := getRemoteVersion()
	if err != nil {
		return
	}

	if remoteVersion == Version || remoteVersion == "" {
		return
	}

	fmt.Printf("\n\033[33mThere is a new version of RequestBite RBite CLI available.\033[0m\n")
	fmt.Printf("You're running v%s and the new version is v%s.\n\n", Version, remoteVersion)

	if runtime.GOOS == "windows" {
		fmt.Println("See https://github.com/requestbite/rbite/ for installation details.\n")
		return
	}

	fmt.Print("Do you want to install (Y/N): ")
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		fmt.Println("\nContinuing with current version...")
		return
	}

	response = strings.TrimSpace(strings.ToLower(response))
	if response == "y" || response == "yes" {
		fmt.Println("\nInstalling update...")
		if err := installUpdate(); err != nil {
			fmt.Printf("\033[31mFailed to install update: %v\033[0m\n", err)
			fmt.Println("Please visit https://github.com/requestbite/rbite/ for manual installation.\n")
		} else {
			fmt.Println("\033[32mUpdate installed successfully!\033[0m")
			fmt.Println("Please restart rbite to use the new version.\n")
			os.Exit(0)
		}
	} else {
		fmt.Println("\nContinuing with current version...")
	}
	fmt.Println()
}

// installUpdate runs the installation script.
func installUpdate() error {
	cmd := exec.Command("bash", "-c", "curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runUninstall removes the rbite binary and optionally the config directory,
// shell completions, and man page.
func runUninstall() {
	reader := bufio.NewReader(os.Stdin)
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("\033[31mCould not determine home directory: %v\033[0m\n", err)
		os.Exit(1)
	}

	// --- Binary ---
	execPath, err := os.Executable()
	if err != nil {
		fmt.Printf("\033[31mCould not determine binary path: %v\033[0m\n", err)
		os.Exit(1)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		fmt.Printf("\033[31mCould not resolve binary path: %v\033[0m\n", err)
		os.Exit(1)
	}

	fmt.Printf("Binary to remove: %s\n", execPath)
	fmt.Print("Remove binary? (Y/n): ")
	resp, _ := reader.ReadString('\n')
	resp = strings.TrimSpace(strings.ToLower(resp))
	if resp == "" || resp == "y" || resp == "yes" {
		if err := os.Remove(execPath); err != nil {
			fmt.Printf("\033[31mFailed to remove binary: %v\033[0m\n", err)
			os.Exit(1)
		}
		fmt.Println("\033[32mBinary removed.\033[0m")
	} else {
		fmt.Println("Skipped binary removal.")
	}

	// --- Config directory ---
	cfgPath, err := configPath()
	if err != nil {
		fmt.Printf("\033[31mCould not determine config path: %v\033[0m\n", err)
		os.Exit(1)
	}
	cfgDir := filepath.Dir(cfgPath)
	fmt.Printf("\nConfig directory: %s\n", cfgDir)
	fmt.Print("Remove config directory and config file? (y/N): ")
	resp, _ = reader.ReadString('\n')
	resp = strings.TrimSpace(strings.ToLower(resp))
	if resp == "y" || resp == "yes" {
		if err := os.RemoveAll(cfgDir); err != nil {
			fmt.Printf("\033[31mFailed to remove config directory: %v\033[0m\n", err)
			os.Exit(1)
		}
		fmt.Println("\033[32mConfig directory removed.\033[0m")
	} else {
		fmt.Println("Skipped config directory removal.")
	}

	// --- Shell completions ---
	completionFiles := []string{
		filepath.Join(home, ".config", "fish", "completions", "rbite.fish"),
		filepath.Join(home, ".local", "share", "bash-completion", "completions", "rbite"),
		filepath.Join(home, ".local", "share", "zsh", "site-functions", "_rbite"),
	}
	// On macOS, also check the Homebrew completion directories.
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("brew", "--prefix").Output(); err == nil {
			brewPrefix := strings.TrimSpace(string(out))
			completionFiles = append(completionFiles,
				filepath.Join(brewPrefix, "share", "bash-completion", "completions", "rbite"),
				filepath.Join(brewPrefix, "share", "zsh", "site-functions", "_rbite"),
			)
		}
	}
	var foundCompletions []string
	for _, f := range completionFiles {
		if _, err := os.Stat(f); err == nil {
			foundCompletions = append(foundCompletions, f)
		}
	}
	if len(foundCompletions) > 0 {
		fmt.Println("\nShell completion files found:")
		for _, f := range foundCompletions {
			fmt.Printf("  %s\n", f)
		}
		fmt.Print("Remove shell completion files? (y/N): ")
		resp, _ = reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp == "y" || resp == "yes" {
			for _, f := range foundCompletions {
				if err := os.Remove(f); err != nil {
					fmt.Printf("\033[31mFailed to remove %s: %v\033[0m\n", f, err)
				} else {
					fmt.Printf("\033[32mRemoved %s\033[0m\n", f)
				}
			}
		} else {
			fmt.Println("Skipped shell completion removal.")
		}
	}

	// --- Man page ---
	manPage := filepath.Join(home, ".local", "share", "man", "man1", "rbite.1")
	if _, err := os.Stat(manPage); err == nil {
		fmt.Printf("\nMan page: %s\n", manPage)
		fmt.Print("Remove man page? (y/N): ")
		resp, _ = reader.ReadString('\n')
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp == "y" || resp == "yes" {
			if err := os.Remove(manPage); err != nil {
				fmt.Printf("\033[31mFailed to remove man page: %v\033[0m\n", err)
			} else {
				fmt.Println("\033[32mMan page removed.\033[0m")
			}
		} else {
			fmt.Println("Skipped man page removal.")
		}
	}

	fmt.Println("\nUninstall complete.")
}

func createEphemeralTunnel(serverURL string, port int, clientID string) (*CreateEphemeralResponse, error) {
	// Send POST request to create ephemeral tunnel
	body, err := json.Marshal(CreateEphemeralRequest{Port: port, ClientID: clientID})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}
	resp, err := http.Post(serverURL+"/v1/ephemeral", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create ephemeral tunnel: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, errSessionConflict
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var ephemeralResp CreateEphemeralResponse
	if err := json.NewDecoder(resp.Body).Decode(&ephemeralResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}

	return &ephemeralResp, nil
}

func getActiveSession(serverURL, clientID string) (*ActiveSessionResponse, error) {
	resp, err := http.Get(serverURL + "/v1/ephemeral/active?client_id=" + clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to reach server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return nil, errSessionConflict
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no previous session found (it may have expired)")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(body))
	}

	var active ActiveSessionResponse
	if err := json.NewDecoder(resp.Body).Decode(&active); err != nil {
		return nil, fmt.Errorf("failed to decode response: %v", err)
	}
	return &active, nil
}

func toWSURL(serverURL string) string {
	switch {
	case len(serverURL) >= 8 && serverURL[:8] == "https://":
		return "wss://" + serverURL[8:]
	case len(serverURL) >= 7 && serverURL[:7] == "http://":
		return "ws://" + serverURL[7:]
	}
	return serverURL
}

// reconnectBackoff is the sequence of delays between reconnect attempts.
// The last value is reused for all subsequent attempts.
var reconnectBackoff = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	30 * time.Second,
}

const (
	// reconnectBudget is the maximum total time spent attempting to reconnect
	// after an unexpected disconnect before giving up entirely.
	reconnectBudget = 3 * time.Minute
	// reconnectResetThreshold resets the attempt counter and deadline when a
	// connection lasted longer than this before dropping, so a healthy long-lived
	// tunnel that eventually drops gets a fresh reconnect budget.
	reconnectResetThreshold = 30 * time.Second
)

// connectEphemeralOnce opens a single yamux-over-WebSocket connection for an
// ephemeral tunnel.  It returns:
//   - nil when the expiry timer fires or the context is cancelled (clean exit)
//   - *errTerminated when the server sends close code 4000 (do not reconnect)
//   - any other error for unexpected disconnects (caller may reconnect)
func connectEphemeralOnce(ctx context.Context, serverURL, clientID, localAddr string, expiresAt time.Time, rw *rewriteConfig) error {
	muxURL := toWSURL(serverURL) + "/tunnel/mux?client_id=" + clientID
	ws, resp, err := websocket.DefaultDialer.Dial(muxURL, nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("mux dial failed (HTTP %d): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("mux dial failed: %w", err)
	}
	defer ws.Close()

	conn := newWSConn(ws)

	// The tunnel client accepts streams opened by the server → yamux.Server role.
	session, err := yamux.Server(conn, nil)
	if err != nil {
		return fmt.Errorf("yamux session failed: %w", err)
	}
	defer session.Close()

	// Keep the WebSocket alive so proxies don't kill idle connections.
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := conn.ping(); err != nil {
					return
				}
			}
		}
	}()

	log.Printf("Connected to tunnel server, waiting for connections...")

	expiry := time.NewTimer(time.Until(expiresAt))
	defer expiry.Stop()

	streamCh := make(chan net.Conn)
	errCh := make(chan error, 1)
	go func() {
		for {
			stream, err := session.Accept()
			if err != nil {
				errCh <- err
				return
			}
			streamCh <- stream
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-expiry.C:
			log.Printf("Tunnel expired. Disconnecting.")
			return nil
		case err := <-errCh:
			// Prefer the close reason stored on the connection — yamux wraps the
			// original error and errors.Is checks fail without this fallback.
			if conn.closeErr != nil {
				return conn.closeErr
			}
			if errors.Is(err, errTunnelTerminated) {
				return errTunnelTerminated
			}
			return fmt.Errorf("mux session closed: %w", err)
		case stream := <-streamCh:
			go handleTunneledConnection(stream, localAddr, rw)
		}
	}
}

// connectEphemeralWithReconnect wraps connectEphemeralOnce with exponential-backoff
// reconnect logic.  It reconnects on unexpected disconnects for up to reconnectBudget,
// and stops immediately on a clean exit, context cancellation, or errTunnelTerminated.
func connectEphemeralWithReconnect(ctx context.Context, serverURL, clientID, localAddr string, expiresAt time.Time, rw *rewriteConfig) {
	attempt := 0
	deadline := time.Now().Add(reconnectBudget)

	for {
		connectedAt := time.Now()
		err := connectEphemeralOnce(ctx, serverURL, clientID, localAddr, expiresAt, rw)

		if ctx.Err() != nil {
			return
		}
		if err == nil {
			return
		}
		if errors.Is(err, errTunnelTerminated) {
			var term *errTerminated
			if errors.As(err, &term) {
				switch term.Reason {
				case "timeout":
					log.Printf("Session expired (timeout). Disconnecting.")
				case "max_transfer":
					log.Printf("Session terminated: transfer limit reached.")
				default:
					log.Printf("Tunnel terminated by server.")
				}
			} else {
				log.Printf("Tunnel terminated by server.")
			}
			return
		}

		// Unexpected disconnect. Reset the budget if this connection was healthy.
		if time.Since(connectedAt) > reconnectResetThreshold {
			attempt = 0
			deadline = time.Now().Add(reconnectBudget)
		}

		if time.Now().After(deadline) {
			log.Printf("Could not reconnect within %v, giving up: %v", reconnectBudget, err)
			return
		}

		delay := reconnectBackoff[min(attempt, len(reconnectBackoff)-1)]
		log.Printf("Connection lost (%v), reconnecting in %v... (attempt %d)", err, delay, attempt+1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		attempt++
	}
}

// handleTunneledConnection proxies one HTTP request/response between an inbound
// yamux stream and the local service, logging the method, path, status, and duration.
// For WebSocket upgrades (101) it falls back to a raw bidirectional copy after
// forwarding the handshake, preserving any bytes already buffered by the readers.
//
// When localAddr has no port (e.g. "localhost"), the target port is derived
// dynamically from the X-RBite-Port header injected by the tunnel server.
//
// When rw is non-nil, localhost-style URLs in text response bodies and Location
// headers are rewritten to the tunnel's public URL.
func handleTunneledConnection(stream net.Conn, localAddr string, rw *rewriteConfig) {
	defer stream.Close()

	start := time.Now()

	// Keep buffered readers in scope — needed for the WebSocket fallback so that
	// any bytes read ahead past the HTTP headers are not lost.
	streamBuf := bufio.NewReader(stream)

	req, err := http.ReadRequest(streamBuf)
	if err != nil {
		log.Printf("failed to read request: %v", err)
		return
	}

	// Dynamic port mode: localAddr has no port component (e.g. "localhost").
	// Derive the target port from the X-RBite-Port header injected by the tunnel
	// server and strip it so it doesn't reach the local service.
	targetAddr := localAddr
	if !strings.Contains(localAddr, ":") {
		port := req.Header.Get("X-RBite-Port")
		req.Header.Del("X-RBite-Port")
		if port == "" {
			log.Printf("dynamic port: no X-RBite-Port header for %s %s", req.Method, req.URL.RequestURI())
			return
		}
		targetAddr = localAddr + ":" + port
	}

	// When rewriting is enabled, strip Accept-Encoding so the local service sends
	// an uncompressed body — compressed bytes cannot be text-searched for URLs.
	if rw != nil {
		req.Header.Del("Accept-Encoding")
	}

	localConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("local dial failed (%s): %v", targetAddr, err)
		return
	}
	defer localConn.Close()

	localBuf := bufio.NewReader(localConn)

	if err := req.Write(localConn); err != nil {
		log.Printf("failed to forward request: %v", err)
		return
	}

	resp, err := http.ReadResponse(localBuf, req)
	if err != nil {
		log.Printf("failed to read response: %v", err)
		return
	}
	defer resp.Body.Close()

	// SSE responses require unbuffered streaming; treat them like WebSocket upgrades.
	isSSE := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")

	// Localhost rewriting: mutate the response before forwarding it to the caller.
	// Skip for WebSocket upgrades and SSE — both are unbounded streaming protocols.
	if rw != nil && resp.StatusCode != http.StatusSwitchingProtocols && !isSSE {
		extractPort := func(match string) string {
			sub := localhostPattern.FindStringSubmatch(match)
			if len(sub) > 2 && len(sub[2]) > 1 {
				return sub[2][1:] // strip leading ":"
			}
			return ""
		}

		// Rewrite the Location header (HTTP redirects).
		if loc := resp.Header.Get("Location"); loc != "" {
			resp.Header.Set("Location", localhostPattern.ReplaceAllStringFunc(loc, func(match string) string {
				return portedPublicURL(rw.publicURL, extractPort(match))
			}))
		}

		// Rewrite text body: buffer fully, replace, update Content-Length.
		if isTextContentType(resp.Header.Get("Content-Type")) {
			raw, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil {
				newBody := localhostPattern.ReplaceAllFunc(raw, func(match []byte) []byte {
					sub := localhostPattern.FindSubmatch(match)
					port := ""
					if len(sub) > 2 && len(sub[2]) > 1 {
						port = string(sub[2][1:])
					}
					return []byte(portedPublicURL(rw.publicURL, port))
				})
				resp.Body = io.NopCloser(bytes.NewReader(newBody))
				resp.ContentLength = int64(len(newBody))
				resp.TransferEncoding = nil
				resp.Header.Del("Transfer-Encoding")
				resp.Header.Del("Content-Encoding")
			} else {
				// On read error, restore original body so we still forward what we have.
				resp.Body = io.NopCloser(bytes.NewReader(raw))
			}
		}
	}

	// SSE: write headers immediately then relay body chunk-by-chunk so each event
	// reaches the client without waiting for resp.Write's internal 4 KiB bufio flush.
	if isSSE {
		if err := streamSSEResponse(stream, resp); err != nil {
			log.Printf("SSE stream error: %v", err)
		}
		log.Printf("%s %s %d %s [SSE closed]", req.Method, req.URL.RequestURI(), resp.StatusCode, time.Since(start).Round(time.Millisecond))
		return
	}

	if err := resp.Write(stream); err != nil {
		log.Printf("failed to write response: %v", err)
		return
	}

	// WebSocket upgrade: protocol switches to raw framing after the 101 headers.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		done := make(chan struct{}, 2)
		go func() { io.Copy(localConn, streamBuf); done <- struct{}{} }()
		go func() { io.Copy(stream, localBuf); done <- struct{}{} }()
		<-done
		return
	}

	log.Printf("%s %s %d %s", req.Method, req.URL.RequestURI(), resp.StatusCode, time.Since(start).Round(time.Millisecond))
}

// streamSSEResponse writes the HTTP status line + headers to w immediately (no
// bufio batching), then streams resp.Body as chunked-encoded data so each SSE
// event is delivered without waiting for an internal 4 KiB buffer to fill.
// resp.Write cannot be used here because it wraps w in a bufio.Writer whose
// Flush is only called after the body copy returns — which never happens for SSE.
func streamSSEResponse(w io.Writer, resp *http.Response) error {
	h := resp.Header.Clone()
	h.Del("Content-Length")
	h.Set("Transfer-Encoding", "chunked")

	var hdr bytes.Buffer
	status := resp.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	fmt.Fprintf(&hdr, "HTTP/1.1 %s\r\n", status)
	_ = h.Write(&hdr)
	hdr.WriteString("\r\n")
	if _, err := w.Write(hdr.Bytes()); err != nil {
		return err
	}

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			// Assemble the full chunk (header + data + CRLF) in one Write so it
			// lands in a single yamux DATA frame rather than three separate ones.
			var chunk bytes.Buffer
			fmt.Fprintf(&chunk, "%x\r\n", n)
			chunk.Write(buf[:n])
			chunk.WriteString("\r\n")
			if _, err := w.Write(chunk.Bytes()); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			_, err := io.WriteString(w, "0\r\n\r\n")
			return err
		}
		if readErr != nil {
			return readErr
		}
	}
}

// printSessionStats fetches session statistics from the server and prints them.
func printSessionStats(serverURL, clientID string) {
	resp, err := http.Get(serverURL + "/v1/ephemeral/" + clientID)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return
	}

	var s struct {
		TransferSizeMb float64 `json:"transferSizeMb"`
		Requests       int64   `json:"requests"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return
	}

	fmt.Printf("\n--- Session summary ---\n")
	fmt.Printf("Requests served:  %d\n", s.Requests)
	fmt.Printf("Data transferred: %.2f MB\n", s.TransferSizeMb)
}

// wsConn wraps *websocket.Conn as an io.ReadWriter so io.Copy can drive it.
// This is a minimal client-side version (no net.Conn deadline methods needed).
type wsConn struct {
	ws       *websocket.Conn
	reader   io.Reader
	mu       sync.Mutex // serialises all WebSocket writes
	closeErr *errTerminated // set when server sends close code 4000
}

func newWSConn(ws *websocket.Conn) *wsConn {
	return &wsConn{ws: ws}
}

func (c *wsConn) ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.PingMessage, nil)
}

func (c *wsConn) Read(b []byte) (int, error) {
	for {
		if c.reader != nil {
			n, err := c.reader.Read(b)
			if err == io.EOF {
				c.reader = nil
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}
		msgType, r, err := c.ws.NextReader()
		if err != nil {
			// Close code 4000: server is intentionally terminating the tunnel.
			// Store the reason so connectEphemeralOnce can retrieve it after yamux
			// wraps the error, then return it so yamux closes its session.
			var ce *websocket.CloseError
			if errors.As(err, &ce) && ce.Code == 4000 {
				c.closeErr = &errTerminated{Reason: ce.Text}
				return 0, c.closeErr
			}
			return 0, err
		}
		if msgType == websocket.CloseMessage {
			return 0, io.EOF
		}
		c.reader = r
	}
}

func (c *wsConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.ws.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *wsConn) Close() error {
	return c.ws.Close()
}

// serverHostname strips the scheme from a URL, returning just the host.
func serverHostname(serverURL string) string {
	switch {
	case len(serverURL) >= 8 && serverURL[:8] == "https://":
		return serverURL[8:]
	case len(serverURL) >= 7 && serverURL[:7] == "http://":
		return serverURL[7:]
	}
	return serverURL
}

// printQR prints a QR code of rawURL to stdout for easy phone scanning.
func printQR(rawURL string) {
	fmt.Println("Scan to open tunnel endpoint on your phone:\n")
	qrterminal.GenerateWithConfig(rawURL, qrterminal.Config{
		Level:     qrterminal.L,
		Writer:    os.Stdout,
		BlackChar: qrterminal.BLACK,
		WhiteChar: qrterminal.WHITE,
		QuietZone: 1,
	})
	fmt.Println()
}

// printPortRewriteWarning prints a red bordered warning when --localhost-rewrite
// is active on a tunnel that restricts accessible ports.
func printPortRewriteWarning() {
	const inner = 73 // number of ═ between the corner pieces
	top    := "╔" + strings.Repeat("═", inner) + "╗"
	bottom := "╚" + strings.Repeat("═", inner) + "╝"
	empty  := "║" + strings.Repeat(" ", inner) + "║"

	center := func(text string) string {
		space := inner - len(text)
		if space < 0 {
			space = 0
		}
		left := space / 2
		right := space - left
		return "║" + strings.Repeat(" ", left) + text + strings.Repeat(" ", right) + "║"
	}

	title    := "Please note!"
	underline := strings.Repeat("=", len(title))
	bodyLines := []string{
		"The tunnel you have enabled localhost rewrites for has",
		"restrictions on what ports it will allow access to.",
		"This might cause usability issues if a rewritten port",
		"is not accepted by the tunnel.",
	}

	fmt.Printf("\n%s", ansiRed)
	fmt.Println(top)
	fmt.Println(empty)
	fmt.Println(center(title))
	fmt.Println(center(underline))
	fmt.Println(empty)
	for _, l := range bodyLines {
		fmt.Println(center(l))
	}
	fmt.Println(empty)
	fmt.Print(bottom)
	fmt.Printf("%s\n\n", ansiReset)
}

// getEnv retrieves environment variable with fallback default value
func getEnv(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

// Ensure wsConn satisfies io.ReadWriteCloser (required by yamux).
var _ io.ReadWriteCloser = (*wsConn)(nil)

// oidcConfig holds the subset of fields from a .well-known/openid-configuration document.
type oidcConfig struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

// fetchOIDCConfig retrieves the OIDC discovery document from apiURL/.well-known/openid-configuration.
func fetchOIDCConfig(apiURL string) (*oidcConfig, error) {
	discoveryURL := strings.TrimRight(apiURL, "/") + "/.well-known/openid-configuration"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", discoveryURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach OIDC discovery endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery returned status %d", resp.StatusCode)
	}

	var cfg oidcConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("could not parse OIDC discovery document: %w", err)
	}
	if cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" {
		return nil, errors.New("OIDC discovery document is missing required endpoints")
	}
	return &cfg, nil
}

// generatePKCE returns a code_verifier and its S256 code_challenge.
func generatePKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("could not generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// openBrowser attempts to open url in the system default browser.
// Errors are ignored — the URL is always printed separately.
func openBrowser(rawURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	_ = cmd.Start()
}

// exchangeCodeForToken exchanges an authorization code for an access token and optional refresh token.
func exchangeCodeForToken(tokenEndpoint, code, codeVerifier, redirectURI, clientID string) (accessToken, refreshToken string, err error) {
	params := url.Values{}
	params.Set("grant_type", "authorization_code")
	params.Set("code", code)
	params.Set("redirect_uri", redirectURI)
	params.Set("client_id", clientID)
	params.Set("code_verifier", codeVerifier)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", tokenEndpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("token endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", "", fmt.Errorf("could not parse token response: %w", err)
	}
	if tokenResp.Error != "" {
		return "", "", fmt.Errorf("token error %q: %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	if tokenResp.AccessToken == "" {
		return "", "", errors.New("token response did not contain an access_token")
	}
	return tokenResp.AccessToken, tokenResp.RefreshToken, nil
}

// runWithAutoRelogin runs fn. If it returns an errSessionExpired error, it
// prompts the user to log in again and retries fn once after a successful login.
func runWithAutoRelogin(apiURL string, fn func() error) error {
	err := fn()
	if err == nil || !errors.Is(err, errSessionExpired) {
		return err
	}
	fmt.Print("\nYou've been logged out and need to login again, continue? (Y/n): ")
	reader := bufio.NewReader(os.Stdin)
	response, readErr := reader.ReadString('\n')
	if readErr != nil || strings.EqualFold(strings.TrimSpace(response), "n") {
		return errors.New("login cancelled")
	}
	if loginErr := runLogin(apiURL); loginErr != nil {
		return fmt.Errorf("login failed: %w", loginErr)
	}
	return fn()
}

// runLogin performs the OIDC Authorization Code + PKCE login flow.
func runLogin(apiURL string) error {
	clientID := getEnv("OAUTH_CLIENT_ID", DefaultOAuthClientID)
	if clientID == "" {
		return errors.New("OAUTH_CLIENT_ID is not set")
	}
	scopes := getEnv("OAUTH_SCOPES", DefaultOAuthScopes)
	callbackURL := getEnv("OAUTH_CALLBACK_URL", DefaultOAuthCallbackURL)

	// Parse callback URL to determine where to listen.
	parsedCB, err := url.Parse(callbackURL)
	if err != nil {
		return fmt.Errorf("invalid OAUTH_CALLBACK_URL: %w", err)
	}
	listenAddr := parsedCB.Host
	callbackPath := parsedCB.Path

	// Discover OIDC endpoints.
	oidc, err := fetchOIDCConfig(apiURL)
	if err != nil {
		return err
	}

	// Generate PKCE and state.
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return err
	}
	state := uuid.New().String()

	// Build the authorization URL.
	authParams := url.Values{}
	authParams.Set("response_type", "code")
	authParams.Set("client_id", clientID)
	authParams.Set("redirect_uri", callbackURL)
	authParams.Set("scope", scopes)
	authParams.Set("state", state)
	authParams.Set("code_challenge", challenge)
	authParams.Set("code_challenge_method", "S256")
	authURL := oidc.AuthorizationEndpoint + "?" + authParams.Encode()

	// Start local callback server before opening the browser.
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- errors.New("state mismatch in callback")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			if e := r.URL.Query().Get("error"); e != "" {
				desc := r.URL.Query().Get("error_description")
				http.Error(w, "authorization denied", http.StatusBadRequest)
				errCh <- fmt.Errorf("authorization error %q: %s", e, desc)
				return
			}
			http.Error(w, "missing authorization code", http.StatusBadRequest)
			errCh <- errors.New("callback did not contain an authorization code")
			return
		}
		fmt.Fprintln(w, "Login successful. You may close this tab.")
		codeCh <- code
	})

	srv := &http.Server{Addr: listenAddr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("callback server error: %w", err)
		}
	}()
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	fmt.Printf("Opening browser for login...\n\n")
	fmt.Printf("If the browser does not open, visit this URL manually:\n\n  %s\n\n", authURL)
	openBrowser(authURL)
	fmt.Println("Waiting for authorization...")

	// Wait up to 5 minutes for the callback.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var code string
	select {
	case <-ctx.Done():
		return errors.New("login timed out waiting for browser callback")
	case err := <-errCh:
		return err
	case code = <-codeCh:
	}

	// Exchange code for token.
	accessToken, refreshToken, err := exchangeCodeForToken(oidc.TokenEndpoint, code, verifier, callbackURL, clientID)
	if err != nil {
		return err
	}

	// Persist tokens in config.
	cfg, err := loadOrCreateConfig()
	if err != nil {
		return err
	}
	cfg.AccessToken = accessToken
	cfg.RefreshToken = refreshToken

	// Fetch user info.
	userInfo, err := fetchUserInfo(apiURL, accessToken)
	if err != nil {
		return err
	}

	// Fetch accounts and pick the first one.
	accountID, accountName, err := fetchFirstAccount(apiURL, accessToken)
	if err != nil {
		return err
	}
	cfg.AccountID = accountID

	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Println("Login successful.")
	if userInfo.GivenName != "" && userInfo.FamilyName != "" {
		fmt.Printf("Hello %s %s!\n", userInfo.GivenName, userInfo.FamilyName)
	}
	fmt.Printf("RBite now has access to your account: %s.\n  To switch account, use the --switch-account parameter.\n", accountName)
	return nil
}

type userInfoResponse struct {
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

func fetchUserInfo(apiURL, accessToken string) (*userInfoResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(apiURL, "/")+"/oauth2/userinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned status %d", resp.StatusCode)
	}

	var info userInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("could not parse userinfo response: %w", err)
	}
	return &info, nil
}

func fetchFirstAccount(apiURL, accessToken string) (id, name string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(apiURL, "/")+"/v1/accounts", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("accounts request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("accounts endpoint returned status %d", resp.StatusCode)
	}

	var body struct {
		Accounts []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", fmt.Errorf("could not parse accounts response: %w", err)
	}
	if len(body.Accounts) == 0 {
		return "", "", errors.New("no accounts found for this user")
	}
	return body.Accounts[0].ID, body.Accounts[0].Name, nil
}

// refreshAccessToken uses a refresh token to obtain a new access token (and possibly a new refresh token).
func refreshAccessToken(apiURL, refreshToken, clientID string) (accessToken, newRefreshToken string, err error) {
	params := url.Values{}
	params.Set("grant_type", "refresh_token")
	params.Set("refresh_token", refreshToken)
	params.Set("client_id", clientID)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(apiURL, "/")+"/oauth2/token", strings.NewReader(params.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("token refresh returned status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", "", fmt.Errorf("could not parse token refresh response: %w", err)
	}
	if tokenResp.Error != "" {
		return "", "", fmt.Errorf("token refresh error %q: %s", tokenResp.Error, tokenResp.ErrorDesc)
	}
	if tokenResp.AccessToken == "" {
		return "", "", errors.New("token refresh response did not contain an access_token")
	}
	return tokenResp.AccessToken, tokenResp.RefreshToken, nil
}

// ensureAuthenticated ensures the config has a valid access token, refreshing or
// prompting login as needed. Returns the (possibly updated) config and API URL.
func ensureAuthenticated(apiURL string) (*Config, error) {
	cfg, err := loadOrCreateConfig()
	if err != nil {
		return nil, err
	}

	// No credentials at all — ask user to log in.
	if cfg.AccessToken == "" && cfg.RefreshToken == "" {
		fmt.Print("You are not logged in. Do you want to log in? (Y/n): ")
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(strings.ToLower(response)) == "n" {
			return nil, errors.New("login required")
		}
		if err := runLogin(apiURL); err != nil {
			return nil, err
		}
		// Reload config after login.
		return loadOrCreateConfig()
	}

	// Have a refresh token but no access token — refresh silently.
	if cfg.AccessToken == "" && cfg.RefreshToken != "" {
		clientID := getEnv("OAUTH_CLIENT_ID", DefaultOAuthClientID)
		newAccess, newRefresh, err := refreshAccessToken(apiURL, cfg.RefreshToken, clientID)
		if err != nil {
			return nil, fmt.Errorf("could not refresh credentials: %w", err)
		}
		cfg.AccessToken = newAccess
		if newRefresh != "" {
			cfg.RefreshToken = newRefresh
		}
		if err := saveConfig(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}

	return cfg, nil
}

// authedGet performs a GET request with a Bearer token, retrying once after a
// token refresh if the server returns 401.
func authedGet(apiURL, path, accessToken string, cfg *Config) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	doRequest := func(token string) (*http.Response, error) {
		req, err := http.NewRequest("GET", strings.TrimRight(apiURL, "/")+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return client.Do(req)
	}

	resp, err := doRequest(accessToken)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && cfg.RefreshToken != "" {
		resp.Body.Close()
		clientID := getEnv("OAUTH_CLIENT_ID", DefaultOAuthClientID)
		newAccess, newRefresh, refreshErr := refreshAccessToken(apiURL, cfg.RefreshToken, clientID)
		if refreshErr != nil {
			return nil, fmt.Errorf("%w; token refresh failed: %v", errSessionExpired, refreshErr)
		}
		cfg.AccessToken = newAccess
		if newRefresh != "" {
			cfg.RefreshToken = newRefresh
		}
		if saveErr := saveConfig(cfg); saveErr != nil {
			return nil, saveErr
		}
		return doRequest(newAccess)
	}

	return resp, nil
}

// authedPost performs a POST request with a Bearer token, retrying once after a
// token refresh if the server returns 401.
func authedPost(apiURL, path, accessToken string, body []byte, cfg *Config) (*http.Response, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	doRequest := func(token string) (*http.Response, error) {
		req, err := http.NewRequest("POST", strings.TrimRight(apiURL, "/")+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	}

	resp, err := doRequest(accessToken)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && cfg.RefreshToken != "" {
		resp.Body.Close()
		clientID := getEnv("OAUTH_CLIENT_ID", DefaultOAuthClientID)
		newAccess, newRefresh, refreshErr := refreshAccessToken(apiURL, cfg.RefreshToken, clientID)
		if refreshErr != nil {
			return nil, fmt.Errorf("%w; token refresh failed: %v", errSessionExpired, refreshErr)
		}
		cfg.AccessToken = newAccess
		if newRefresh != "" {
			cfg.RefreshToken = newRefresh
		}
		if saveErr := saveConfig(cfg); saveErr != nil {
			return nil, saveErr
		}
		return doRequest(newAccess)
	}

	return resp, nil
}

// runSwitchAccounts lists the user's accounts and lets them pick one to store as active.
func runSwitchAccounts(apiURL string) error {
	cfg, err := ensureAuthenticated(apiURL)
	if err != nil {
		return err
	}

	resp, err := authedGet(apiURL, "/v1/accounts", cfg.AccessToken, cfg)
	if err != nil {
		return fmt.Errorf("accounts request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("accounts endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Accounts []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return fmt.Errorf("could not parse accounts response: %w", err)
	}
	if len(payload.Accounts) == 0 {
		return errors.New("no accounts found for this user")
	}

	fmt.Println("Pick one of your accounts:")
	fmt.Println()
	for i, a := range payload.Accounts {
		fmt.Printf("  %d. %s\n", i+1, a.Name)
	}
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("Enter number (1-%d): ", len(payload.Accounts))
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("could not read input: %w", err)
		}
		line = strings.TrimSpace(line)
		var choice int
		if _, err := fmt.Sscanf(line, "%d", &choice); err != nil || choice < 1 || choice > len(payload.Accounts) {
			fmt.Printf("Please enter a number between 1 and %d.\n", len(payload.Accounts))
			continue
		}
		selected := payload.Accounts[choice-1]
		cfg.AccountID = selected.ID
		if err := saveConfig(cfg); err != nil {
			return err
		}
		fmt.Printf("Active account set to: %s\n", selected.Name)
		return nil
	}
}

// runWhoami prints the authenticated user's name, email, and active account.
func runWhoami(apiURL string) error {
	cfg, err := loadOrCreateConfig()
	if err != nil {
		return err
	}
	if cfg.AccessToken == "" && cfg.RefreshToken == "" {
		fmt.Println("You are not logged in. Run 'rbite --login' to authenticate.")
		return nil
	}

	// Silently refresh if we only have a refresh token.
	if cfg.AccessToken == "" {
		clientID := getEnv("OAUTH_CLIENT_ID", DefaultOAuthClientID)
		newAccess, newRefresh, refreshErr := refreshAccessToken(apiURL, cfg.RefreshToken, clientID)
		if refreshErr != nil {
			fmt.Println("Your session has expired. Run 'rbite --login' to re-authenticate.")
			return nil
		}
		cfg.AccessToken = newAccess
		if newRefresh != "" {
			cfg.RefreshToken = newRefresh
		}
		_ = saveConfig(cfg)
	}

	// Fetch user info.
	profileResp, err := authedGet(apiURL, "/oauth2/userinfo", cfg.AccessToken, cfg)
	if err != nil {
		if errors.Is(err, errSessionExpired) {
			fmt.Println("Your session has expired. Run 'rbite --login' to re-authenticate.")
			return nil
		}
		return fmt.Errorf("userinfo request failed: %w", err)
	}
	defer profileResp.Body.Close()

	if profileResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(profileResp.Body)
		return fmt.Errorf("userinfo endpoint returned status %d: %s", profileResp.StatusCode, string(body))
	}

	var profile struct {
		Email      string `json:"email"`
		GivenName  string `json:"given_name"`
		FamilyName string `json:"family_name"`
	}
	if err := json.NewDecoder(profileResp.Body).Decode(&profile); err != nil {
		return fmt.Errorf("could not parse userinfo response: %w", err)
	}

	// Fetch account name if an account is selected.
	var accountName string
	if cfg.AccountID != "" {
		acctResp, acctErr := authedGet(apiURL, "/v1/accounts/"+cfg.AccountID, cfg.AccessToken, cfg)
		if acctErr == nil {
			defer acctResp.Body.Close()
			if acctResp.StatusCode == http.StatusOK {
				var acctPayload struct {
					Account struct {
						Name string `json:"name"`
					} `json:"account"`
				}
				if jsonErr := json.NewDecoder(acctResp.Body).Decode(&acctPayload); jsonErr == nil {
					accountName = acctPayload.Account.Name
				}
			}
		}
	}

	fmt.Println("You're logged in as:")
	fmt.Println()
	name := strings.TrimSpace(profile.GivenName + " " + profile.FamilyName)
	if name != "" {
		fmt.Printf("- Name:    %s\n", name)
	}
	fmt.Printf("- Email:   %s\n", profile.Email)
	if cfg.AccountID != "" {
		if accountName != "" {
			fmt.Printf("- Account: %s (%s)\n", accountName, cfg.AccountID)
		} else {
			fmt.Printf("- Account: %s\n", cfg.AccountID)
		}
	}
	return nil
}

// runListViews lists active inspector views for the current account and shows details for a chosen one.
// activeView holds the fields we care about for an active inspector view.
type activeView struct {
	ID         string
	Name       string
	CaptureURL string
}

// fetchActiveViews retrieves the list of active inspector views for the account.
func fetchActiveViews(apiURL string, cfg *Config) ([]activeView, error) {
	resp, err := authedGet(apiURL, "/v1/accounts/"+cfg.AccountID+"/inspector/views", cfg.AccessToken, cfg)
	if err != nil {
		return nil, fmt.Errorf("views request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("views endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var payload struct {
		Views []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			IsActive   bool   `json:"isActive"`
			CaptureURL string `json:"captureUrl"`
		} `json:"views"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("could not parse views response: %w", err)
	}

	var active []activeView
	for _, v := range payload.Views {
		if v.IsActive {
			active = append(active, activeView{v.ID, v.Name, v.CaptureURL})
		}
	}
	return active, nil
}

// selectView lists active views and prompts the user to pick one, returning its ID.
// verb is used in the prompt, e.g. "tail" or "open".
func selectView(apiURL, verb string) (string, error) {
	cfg, err := ensureAuthenticated(apiURL)
	if err != nil {
		return "", err
	}
	if cfg.AccountID == "" {
		return "", errors.New("no account selected; run rbite --switch-accounts first")
	}

	active, err := fetchActiveViews(apiURL, cfg)
	if err != nil {
		return "", err
	}

	if len(active) == 0 {
		return "", errors.New("no active views found for this account")
	}

	for i, v := range active {
		fmt.Printf("  %d. %s\n", i+1, v.Name)
	}
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	var choice int
	for {
		fmt.Printf("Select a view to %s (1-%d): ", verb, len(active))
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("could not read input: %w", err)
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &choice); err != nil || choice < 1 || choice > len(active) {
			fmt.Printf("Please enter a number between 1 and %d.\n", len(active))
			continue
		}
		break
	}

	return active[choice-1].ID, nil
}

func runListViews(apiURL string) error {
	cfg, err := ensureAuthenticated(apiURL)
	if err != nil {
		return err
	}
	if cfg.AccountID == "" {
		return errors.New("no account selected; run rbite --switch-accounts first")
	}

	active, err := fetchActiveViews(apiURL, cfg)
	if err != nil {
		return err
	}

	if len(active) == 0 {
		fmt.Println("No active views found for this account.")
		return nil
	}

	for i, v := range active {
		fmt.Printf("  %d. %s\n", i+1, v.Name)
	}
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	var choice int
	for {
		fmt.Printf("Get details about view (1-%d): ", len(active))
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("could not read input: %w", err)
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &choice); err != nil || choice < 1 || choice > len(active) {
			fmt.Printf("Please enter a number between 1 and %d.\n", len(active))
			continue
		}
		break
	}

	selected := active[choice-1]
	fmt.Println()
	hqURL := strings.TrimRight(getEnv("HQ_URL", DefaultHQURL), "/")
	fmt.Printf("Name:        %s\n", selected.Name)
	fmt.Printf("Capture URL: %s\n", selected.CaptureURL)
	fmt.Printf("Browser URL: %s\n", hqURL+"/views/"+selected.ID+"/capture")
	fmt.Printf("ID:          %s\n", selected.ID)
	fmt.Println()
	fmt.Printf("To tail request info, run \"rbite --views-tail %s\"\n", selected.ID)
	return nil
}

func runAddView(apiURL, name string) error {
	cfg, err := ensureAuthenticated(apiURL)
	if err != nil {
		return err
	}
	if cfg.AccountID == "" {
		return errors.New("no account selected; run rbite --switch-accounts first")
	}

	var body []byte
	if name != "" {
		body, err = json.Marshal(map[string]string{"name": name})
		if err != nil {
			return fmt.Errorf("could not encode request body: %w", err)
		}
	} else {
		body = []byte("{}")
	}

	resp, err := authedPost(apiURL, "/v1/accounts/"+cfg.AccountID+"/inspector/views", cfg.AccessToken, body, cfg)
	if err != nil {
		return fmt.Errorf("create view request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create view endpoint returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var created struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		CaptureURL string `json:"captureUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return fmt.Errorf("could not parse create view response: %w", err)
	}

	fmt.Printf("View with name %q created.\n", created.Name)
	fmt.Printf(" - Send requests to it at %s\n", created.CaptureURL)
	fmt.Printf(" - Open it in your browser by running \"rbite --views-open %s\"\n", created.ID)
	fmt.Printf(" - Tail it in your terminal by running \"rbite --views-tail %s\"\n", created.ID)
	return nil
}

func runOpenView(apiURL, viewID string) error {
	cfg, err := ensureAuthenticated(apiURL)
	if err != nil {
		return err
	}
	if cfg.AccountID == "" {
		return errors.New("no account selected; run rbite --switch-accounts first")
	}

	hqURL := strings.TrimRight(getEnv("HQ_URL", DefaultHQURL), "/")
	if hqURL == "" {
		return errors.New("HQ_URL is not set in .env")
	}
	target := hqURL + "/views/" + viewID + "/capture"

	if tryOpenBrowser(target) {
		fmt.Printf("Opened %s in your browser.\n", target)
	} else {
		fmt.Printf("Could not open browser. Visit this URL manually:\n\n  %s\n", target)
	}
	return nil
}

// tryOpenBrowser attempts to open rawURL in the default browser and reports success.
func tryOpenBrowser(rawURL string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Run() == nil
}

// ANSI colour constants used by the JSON pretty-printer.
const (
	ansiReset   = "\033[0m"
	ansiBold    = "\033[1m"
	ansiCyan    = "\033[36m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiMagenta = "\033[35m"
	ansiRed     = "\033[31m"
	ansiBlue    = "\033[34m"
	ansiGray    = "\033[90m"
)

// colorizeJSON unmarshals raw JSON and returns an indented, coloured string.
func colorizeJSON(raw string) string {
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw // not valid JSON — return as-is
	}
	return colorizeValue(v, 0)
}

func colorizeValue(v interface{}, depth int) string {
	pad := strings.Repeat("  ", depth)
	inner := strings.Repeat("  ", depth+1)

	switch val := v.(type) {
	case map[string]interface{}:
		if len(val) == 0 {
			return "{}"
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var sb strings.Builder
		sb.WriteString("{\n")
		for i, k := range keys {
			sb.WriteString(inner)
			sb.WriteString(ansiCyan + `"` + k + `"` + ansiReset)
			sb.WriteString(": ")
			sb.WriteString(colorizeValue(val[k], depth+1))
			if i < len(keys)-1 {
				sb.WriteByte(',')
			}
			sb.WriteByte('\n')
		}
		sb.WriteString(pad + "}")
		return sb.String()

	case []interface{}:
		if len(val) == 0 {
			return "[]"
		}
		var sb strings.Builder
		sb.WriteString("[\n")
		for i, item := range val {
			sb.WriteString(inner)
			sb.WriteString(colorizeValue(item, depth+1))
			if i < len(val)-1 {
				sb.WriteByte(',')
			}
			sb.WriteByte('\n')
		}
		sb.WriteString(pad + "]")
		return sb.String()

	case string:
		return ansiGreen + `"` + val + `"` + ansiReset

	case float64:
		if val == float64(int64(val)) {
			return ansiYellow + fmt.Sprintf("%d", int64(val)) + ansiReset
		}
		return ansiYellow + fmt.Sprintf("%g", val) + ansiReset

	case bool:
		return ansiMagenta + fmt.Sprintf("%t", val) + ansiReset

	case nil:
		return ansiRed + "null" + ansiReset

	default:
		return fmt.Sprintf("%v", val)
	}
}

// printRequestEvent formats a single SSE data payload into labelled sections.
func printRequestEvent(raw string) {
	var evt map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		// Not valid JSON — fall back to raw output.
		fmt.Println(raw)
		return
	}

	sectionHeader := func(title string) {
		fmt.Printf("%s%s%s\n", ansiBold, title, ansiReset)
		fmt.Println(strings.Repeat("=", len(title)))
	}

	// Skip empty/keepalive events that carry no request data.
	addr, hasAddr := evt["remoteAddr"].(string)
	if !hasAddr || addr == "" {
		return
	}

	ts := time.Now().Format("15:04:05")
	fmt.Printf("%s--- %s ---%s\n\n", ansiGray, ts, ansiReset)

	// ── Request Details ──────────────────────────────────────────────────────
	sectionHeader("Request Details")
	fmt.Printf("Host:    %s\n", addr)
	if method, ok := evt["method"].(string); ok && method != "" {
		fmt.Printf("Method:  %s\n", method)
	}
	fmt.Println()

	// ── Request Headers ──────────────────────────────────────────────────────
	sectionHeader("Request Headers")
	if hdrs, ok := evt["headers"].(map[string]interface{}); ok && len(hdrs) > 0 {
		// Find longest key for alignment.
		maxLen := 0
		keys := make([]string, 0, len(hdrs))
		for k := range hdrs {
			keys = append(keys, k)
			if len(k) > maxLen {
				maxLen = len(k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			var v string
			switch val := hdrs[k].(type) {
			case []interface{}:
				parts := make([]string, len(val))
				for i, item := range val {
					parts[i] = fmt.Sprintf("%v", item)
				}
				v = strings.Join(parts, ", ")
			default:
				v = fmt.Sprintf("%v", val)
			}
			fmt.Printf("%-*s  %s\n", maxLen, k, v)
		}
	}
	fmt.Println()

	// ── Query-string Parameters (optional) ──────────────────────────────────
	if qs, ok := evt["queryString"].(string); ok && qs != "{}" && qs != "" {
		sectionHeader("Query-string Parameters")
		var qsMap map[string]interface{}
		if err := json.Unmarshal([]byte(qs), &qsMap); err == nil && len(qsMap) > 0 {
			maxLen := 0
			keys := make([]string, 0, len(qsMap))
			for k := range qsMap {
				keys = append(keys, k)
				if len(k) > maxLen {
					maxLen = len(k)
				}
			}
			sort.Strings(keys)
			for _, k := range keys {
				var v string
				switch val := qsMap[k].(type) {
				case []interface{}:
					parts := make([]string, len(val))
					for i, item := range val {
						parts[i] = fmt.Sprintf("%v", item)
					}
					v = strings.Join(parts, ", ")
				default:
					v = fmt.Sprintf("%v", val)
				}
				fmt.Printf("%-*s  %s\n", maxLen, k, v)
			}
		} else {
			fmt.Println(qs)
		}
		fmt.Println()
	}

	// ── Form Fields (optional) ───────────────────────────────────────────────
	if fields, ok := evt["formFields"].(map[string]interface{}); ok && len(fields) > 0 {
		sectionHeader("Form Fields")
		maxLen := 0
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
			if len(k) > maxLen {
				maxLen = len(k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			var v string
			switch val := fields[k].(type) {
			case []interface{}:
				parts := make([]string, len(val))
				for i, item := range val {
					parts[i] = fmt.Sprintf("%v", item)
				}
				v = strings.Join(parts, ", ")
			default:
				v = fmt.Sprintf("%v", val)
			}
			fmt.Printf("%-*s  %s\n", maxLen, k, v)
		}
		fmt.Println()
	}

	// ── Files (optional) ─────────────────────────────────────────────────────
	if filesRaw, ok := evt["files"].([]interface{}); ok && len(filesRaw) > 0 {
		sectionHeader("Files")
		for i, f := range filesRaw {
			file, ok := f.(map[string]interface{})
			if !ok {
				continue
			}
			if i > 0 {
				fmt.Println()
			}
			if v, ok := file["fieldName"].(string); ok {
				fmt.Printf("Field name:   %s\n", v)
			}
			if v, ok := file["fileName"].(string); ok {
				fmt.Printf("File name:    %s\n", v)
			}
			if v, ok := file["contentType"].(string); ok {
				fmt.Printf("Content type: %s\n", v)
			}
			if v, ok := file["size"].(float64); ok {
				fmt.Printf("Size:         %d\n", int64(v))
			}
		}
		fmt.Println()
	}

	// ── Body Payload (optional) ──────────────────────────────────────────────
	if body, ok := evt["body"].(string); ok && body != "" {
		sectionHeader("Body Payload")
		// Try to pretty-print as JSON; fall back to plain text.
		var bodyVal interface{}
		if err := json.Unmarshal([]byte(body), &bodyVal); err == nil {
			fmt.Println(colorizeValue(bodyVal, 0))
		} else {
			fmt.Println(body)
		}
		fmt.Println()
	}
}

// runTailView streams SSE events from the inspector view and pretty-prints each payload.
func runTailView(apiURL, viewID string) error {
	cfg, err := ensureAuthenticated(apiURL)
	if err != nil {
		return err
	}
	if cfg.AccountID == "" {
		return errors.New("no account selected; run rbite --switch-accounts first")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Println()
		cancel()
	}()

	fmt.Printf("Tailing view %s (press Ctrl+C to stop)...\n\n", viewID)

	path := "/v1/accounts/" + cfg.AccountID + "/inspector/views/" + viewID + "/sse"
	return streamSSE(ctx, apiURL, path, cfg)
}

// streamSSE connects to an SSE endpoint and prints each data event as coloured JSON.
// It transparently refreshes the access token and reconnects once on a 401.
func streamSSE(ctx context.Context, apiURL, path string, cfg *Config) error {
	sseURL := strings.TrimRight(apiURL, "/") + path

	req, err := http.NewRequestWithContext(ctx, "GET", sseURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("SSE request failed: %w", err)
	}
	defer resp.Body.Close()

	// Transparent token refresh on 401.
	if resp.StatusCode == http.StatusUnauthorized && cfg.RefreshToken != "" {
		resp.Body.Close()
		clientID := getEnv("OAUTH_CLIENT_ID", DefaultOAuthClientID)
		newAccess, newRefresh, refreshErr := refreshAccessToken(apiURL, cfg.RefreshToken, clientID)
		if refreshErr != nil {
			return fmt.Errorf("%w; token refresh failed: %v", errSessionExpired, refreshErr)
		}
		cfg.AccessToken = newAccess
		if newRefresh != "" {
			cfg.RefreshToken = newRefresh
		}
		if saveErr := saveConfig(cfg); saveErr != nil {
			return saveErr
		}
		return streamSSE(ctx, apiURL, path, cfg)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("SSE endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := scanner.Text()

		// Skip comments and non-data lines.
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		printRequestEvent(data)
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("SSE stream error: %w", err)
	}
	return nil
}

// ── Tunnel list ───────────────────────────────────────────────────────────────

type tunnelListItem struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	Active      bool      `json:"active"`
	Ports       []int     `json:"ports"`
	DefaultPort int       `json:"defaultPort"`
	PublicURL   string    `json:"publicUrl"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func runListTunnels(apiURL string) error {
	cfg, err := ensureAuthenticated(apiURL)
	if err != nil {
		return err
	}
	if cfg.AccountID == "" {
		return errors.New("no account selected; run rbite --switch-accounts first")
	}

	resp, err := authedGet(apiURL, "/v1/accounts/"+cfg.AccountID+"/tunnels", cfg.AccessToken, cfg)
	if err != nil {
		return fmt.Errorf("tunnels request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("tunnels endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var tunnels []tunnelListItem
	if err := json.NewDecoder(resp.Body).Decode(&tunnels); err != nil {
		return fmt.Errorf("could not parse tunnels response: %w", err)
	}

	if len(tunnels) == 0 {
		fmt.Println("No tunnels found for this account.")
		return nil
	}

	for i, t := range tunnels {
		fmt.Printf("  %d. %s\n", i+1, t.Name)
	}
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	var choice int
	for {
		fmt.Printf("Get details about tunnel (1-%d): ", len(tunnels))
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("could not read input: %w", err)
		}
		if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d", &choice); err != nil || choice < 1 || choice > len(tunnels) {
			fmt.Printf("Please enter a number between 1 and %d.\n", len(tunnels))
			continue
		}
		break
	}

	t := tunnels[choice-1]
	fmt.Println()

	var portsStr string
	if len(t.Ports) == 0 {
		portsStr = "All"
	} else {
		parts := make([]string, len(t.Ports))
		for i, p := range t.Ports {
			parts[i] = fmt.Sprintf("%d", p)
		}
		portsStr = strings.Join(parts, ", ")
	}

	fmt.Printf("Name:          %s\n", t.Name)
	fmt.Printf("Default port:  %d\n", t.DefaultPort)
	fmt.Printf("Allowed ports: %s\n", portsStr)
	fmt.Printf("Enabled:       %v\n", t.Enabled)
	fmt.Printf("Public URL:    %s\n", t.PublicURL)
	fmt.Println()
	fmt.Printf("To connect this tunnel, run \"rbite -t %s\"\n", t.Name)
	return nil
}

// ── Permanent tunnel support ──────────────────────────────────────────────────

// tunnelDetails holds the fields returned by the API for a permanent tunnel.
type tunnelDetails struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicURL string `json:"publicUrl"`
	Token     string `json:"token"`
	Ports     []int  `json:"ports"`
	Enabled   bool   `json:"enabled"`
}

// fetchTunnelDetails retrieves a permanent tunnel's config from the requestbite API.
func fetchTunnelDetails(apiURL, accountID, tID string, cfg *Config) (*tunnelDetails, error) {
	resp, err := authedGet(apiURL, "/v1/accounts/"+accountID+"/tunnels/"+tID, cfg.AccessToken, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tunnel: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("tunnel %s not found (check the tunnel ID and account)", tID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tunnel endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var t tunnelDetails
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return nil, fmt.Errorf("could not parse tunnel response: %w", err)
	}
	if t.Token == "" {
		return nil, fmt.Errorf("tunnel response did not contain a token — try regenerating the tunnel")
	}
	return &t, nil
}

// resolveTunnelID resolves a tunnel name (or existing UUID) to a tunnel ID by
// fetching the account's tunnel list. Matching by ID is kept so that --resume,
// which stores the resolved ID in config, continues to work transparently.
func resolveTunnelID(apiURL, accountID, nameOrID string, cfg *Config) (string, error) {
	resp, err := authedGet(apiURL, "/v1/accounts/"+accountID+"/tunnels", cfg.AccessToken, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to fetch tunnels: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("tunnels endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var tunnels []tunnelListItem
	if err := json.NewDecoder(resp.Body).Decode(&tunnels); err != nil {
		return "", fmt.Errorf("could not parse tunnels response: %w", err)
	}

	for _, t := range tunnels {
		if t.Name == nameOrID || t.ID == nameOrID {
			return t.ID, nil
		}
	}
	return "", fmt.Errorf("no tunnel found with name %q", nameOrID)
}

// runPermanentTunnel orchestrates fetching tunnel details, printing status, and
// connecting to the tunnel server.  It saves resume state to config before
// connecting so that --resume works even if the process is interrupted.
func runPermanentTunnel(serverURL, apiURL, tID string, localPort int, showQR bool, localhostRewrite bool, cfg *Config) {
	if cfg.AccountID == "" {
		log.Fatal("No account selected — run: rbite --switch-accounts")
	}

	resolvedID, err := resolveTunnelID(apiURL, cfg.AccountID, tID, cfg)
	if err != nil {
		log.Fatalf("Could not resolve tunnel: %v", err)
	}

	details, err := fetchTunnelDetails(apiURL, cfg.AccountID, resolvedID, cfg)
	if err != nil {
		log.Fatalf("Could not fetch tunnel details: %v", err)
	}
	if !details.Enabled {
		log.Fatalf("Tunnel %q is disabled — enable it via the dashboard or API before connecting.", details.Name)
	}

	fmt.Printf("\n%s\n", tunnelArt)
	fmt.Printf("Permanent tunnel %q connected.\n", details.Name)
	fmt.Printf("> Internet endpoint: %s\n", details.PublicURL)
	if len(details.Ports) > 0 {
		fmt.Printf("> Allowed ports:     %v\n", details.Ports)
	}
	if localPort != 0 {
		fmt.Printf("> Local service:    http://localhost:%d\n", localPort)
	} else {
		fmt.Printf("> Local service:    http://localhost:{port} — dynamic, matches public request port\n")
	}
	fmt.Printf("Press Ctrl+C to stop\n\n")
	if showQR {
		printQR(details.PublicURL)
	}

	if localhostRewrite && len(details.Ports) > 0 {
		printPortRewriteWarning()
	}

	// Persist resume state before blocking so Ctrl+C restarts cleanly.
	cfg.LastSessionType = "permanent"
	cfg.LastTunnelID = resolvedID
	cfg.LastLocalPort = localPort
	_ = saveConfig(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		fmt.Println()
		cancel()
	}()

	var localAddr string
	if localPort != 0 {
		localAddr = fmt.Sprintf("localhost:%d", localPort)
	} else {
		localAddr = "localhost" // dynamic: port derived per-request from X-RBite-Port header
	}

	var rw *rewriteConfig
	if localhostRewrite {
		rw = &rewriteConfig{publicURL: details.PublicURL}
	}
	connectPermanentWithReconnect(ctx, serverURL, resolvedID, details.Token, localAddr, rw)
}

// connectPermanentOnce opens a single yamux-over-WebSocket connection for a
// permanent tunnel.  It returns:
//   - nil when the context is cancelled (clean exit)
//   - *errTerminated (IS errTunnelTerminated) when the server sends close code 4000
//   - errTunnelTerminated (wrapped) when the upgrade is rejected with 401/409
//   - any other error for unexpected disconnects (caller may reconnect)
func connectPermanentOnce(ctx context.Context, serverURL, tID, token, localAddr string, rw *rewriteConfig) error {
	muxURL := toWSURL(serverURL) + "/tunnel/mux?client_id=" + url.QueryEscape(tID) + "&token=" + url.QueryEscape(token)
	ws, resp, err := websocket.DefaultDialer.Dial(muxURL, nil)
	if err != nil {
		if resp != nil {
			if resp.StatusCode == http.StatusUnauthorized {
				return fmt.Errorf("%w: invalid token (try fetching fresh tunnel details)", errTunnelTerminated)
			}
			if resp.StatusCode == http.StatusConflict {
				return fmt.Errorf("%w: tunnel is already active elsewhere — disconnect the other client first", errTunnelTerminated)
			}
			return fmt.Errorf("mux dial failed (HTTP %d): %w", resp.StatusCode, err)
		}
		return fmt.Errorf("mux dial failed: %w", err)
	}
	defer ws.Close()

	conn := newWSConn(ws)

	session, err := yamux.Server(conn, nil)
	if err != nil {
		return fmt.Errorf("yamux session failed: %w", err)
	}
	defer session.Close()

	// Keep the WebSocket alive so proxies don't kill idle connections.
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := conn.ping(); err != nil {
					return
				}
			}
		}
	}()

	log.Printf("Connected to tunnel server, waiting for connections...")

	streamCh := make(chan net.Conn)
	errCh := make(chan error, 1)
	go func() {
		for {
			stream, err := session.Accept()
			if err != nil {
				errCh <- err
				return
			}
			streamCh <- stream
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if conn.closeErr != nil {
				return conn.closeErr
			}
			if errors.Is(err, errTunnelTerminated) {
				return errTunnelTerminated
			}
			return fmt.Errorf("mux session closed: %w", err)
		case stream := <-streamCh:
			go handleTunneledConnection(stream, localAddr, rw)
		}
	}
}

// connectPermanentWithReconnect wraps connectPermanentOnce with exponential-backoff
// reconnect logic.  Unlike ephemeral tunnels there is no expiry timer — the
// connection runs until the context is cancelled, the server terminates it, or
// the reconnect budget is exhausted.
func connectPermanentWithReconnect(ctx context.Context, serverURL, tID, token, localAddr string, rw *rewriteConfig) {
	attempt := 0
	deadline := time.Now().Add(reconnectBudget)

	for {
		connectedAt := time.Now()
		err := connectPermanentOnce(ctx, serverURL, tID, token, localAddr, rw)

		if ctx.Err() != nil {
			return
		}
		if err == nil {
			return
		}
		if errors.Is(err, errTunnelTerminated) {
			var term *errTerminated
			if errors.As(err, &term) {
				switch term.Reason {
				case "timeout":
					log.Printf("Session expired (timeout). Disconnecting.")
				case "max_transfer":
					log.Printf("Session terminated: transfer limit reached.")
				default:
					log.Printf("Tunnel terminated by server.")
				}
			} else {
				log.Printf("Tunnel terminated by server: %v", err)
			}
			return
		}

		// Unexpected disconnect. Reset the budget if this connection was healthy.
		if time.Since(connectedAt) > reconnectResetThreshold {
			attempt = 0
			deadline = time.Now().Add(reconnectBudget)
		}

		if time.Now().After(deadline) {
			log.Printf("Could not reconnect within %v, giving up: %v", reconnectBudget, err)
			return
		}

		delay := reconnectBackoff[min(attempt, len(reconnectBackoff)-1)]
		log.Printf("Connection lost (%v), reconnecting in %v... (attempt %d)", err, delay, attempt+1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		attempt++
	}
}
