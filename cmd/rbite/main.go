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

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

//go:embed tunnel-art.txt
var tunnelArt string

// Version information — set via ldflags at build time.
var (
	Version             = "dev"
	BuildTime           = "unknown"
	GitCommit           = "unknown"
	DefaultAPIHostname  = ""
)

type Config struct {
	ClientID     string `yaml:"clientId"`
	AccessToken  string `yaml:"accessToken,omitempty"`
	RefreshToken string `yaml:"refreshToken,omitempty"`
	AccountID    string `yaml:"accountId,omitempty"`
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
		return "https://" + DefaultAPIHostname
	}
	if h := getEnv("API_HOSTNAME", ""); h != "" {
		return "https://" + h
	}
	return "http://localhost:8080"
}

func printHelp() {
	defaultServer := buildDefaultServerURL()
	fmt.Printf("\n\033[38;5;208mRequestBite RBite CLI\033[0m ⚡ v%s\n\n", Version)
	fmt.Println("Usage:")
	fmt.Printf("  rbite [options]\n\n")
	fmt.Println("Options:")
	fmt.Printf("  -e, --ephemeral int         Port to expose via ephemeral tunnel\n")
	fmt.Printf("  -h, --help                  Show help information\n")
	fmt.Printf("      --login                 Log in via browser (OIDC)\n")
	fmt.Printf("      --no-upgrade-check      Disable automatic upgrade check\n")
	fmt.Printf("  -r, --resume                Resume the last session if it has not expired\n")
	fmt.Printf("      --views-list            List active inspector views for the current account\n")
	fmt.Printf("      --views-tail [view ID]  Stream live requests for a view (prompts if no ID given)\n")
	fmt.Printf("      --views-open [view ID]  Open a view's capture URL in the browser (prompts if no ID given)\n")
	fmt.Printf("      --switch-accounts       Switch the active account\n")
	fmt.Printf("      --tunnel-server string  Tunnel server URL (default %q)\n", defaultServer)
	fmt.Printf("  -v, --version               Show version information\n")
	fmt.Println()
}

func main() {
	// Load .env file
	_ = godotenv.Load()

	// Command line flags
	var (
		ephemeralPort  int
		showVersion    bool
		showHelp       bool
		resume         bool
		serverURL      string
		noUpgradeCheck bool
		loginMode      bool
		switchAccounts bool
		listViews      bool
		tailViewID     string
		openViewID     string
	)
	defaultServer := buildDefaultServerURL()

	flag.IntVar(&ephemeralPort, "e", 0, "")
	flag.IntVar(&ephemeralPort, "ephemeral", 0, "")
	flag.BoolVar(&showVersion, "v", false, "")
	flag.BoolVar(&showVersion, "version", false, "")
	flag.BoolVar(&showHelp, "h", false, "")
	flag.BoolVar(&showHelp, "help", false, "")
	flag.BoolVar(&resume, "r", false, "")
	flag.BoolVar(&resume, "resume", false, "")
	flag.StringVar(&serverURL, "tunnel-server", defaultServer, "")
	flag.BoolVar(&noUpgradeCheck, "no-upgrade-check", false, "")
	flag.BoolVar(&loginMode, "login", false, "")
	flag.BoolVar(&switchAccounts, "switch-accounts", false, "")
	flag.BoolVar(&listViews, "views-list", false, "")
	flag.StringVar(&tailViewID, "views-tail", "", "")
	flag.StringVar(&openViewID, "views-open", "", "")
	flag.Usage = printHelp

	// Pre-scan os.Args to detect --views-tail / --views-open without a value,
	// since flag.StringVar requires a value. Strip bare flags and record intent.
	viewTailNoID := false
	viewOpenNoID := false
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

	// Login flow
	if loginMode {
		apiURL := getEnv("REQUESTBITE_API_URL", serverURL)
		if err := runLogin(apiURL); err != nil {
			log.Fatalf("Login failed: %v", err)
		}
		os.Exit(0)
	}

	// Tail view
	if tailViewID != "" || viewTailNoID {
		apiURL := getEnv("REQUESTBITE_API_URL", serverURL)
		id := tailViewID
		if id == "" {
			var err error
			id, err = selectView(apiURL, "tail")
			if err != nil {
				log.Fatalf("View selection failed: %v", err)
			}
		}
		if err := runTailView(apiURL, id); err != nil {
			log.Fatalf("Tail view failed: %v", err)
		}
		os.Exit(0)
	}

	// Open view in browser
	if openViewID != "" || viewOpenNoID {
		apiURL := getEnv("REQUESTBITE_API_URL", serverURL)
		id := openViewID
		if id == "" {
			var err error
			id, err = selectView(apiURL, "open")
			if err != nil {
				log.Fatalf("View selection failed: %v", err)
			}
		}
		if err := runOpenView(apiURL, id); err != nil {
			log.Fatalf("Open view failed: %v", err)
		}
		os.Exit(0)
	}

	// List views
	if listViews {
		apiURL := getEnv("REQUESTBITE_API_URL", serverURL)
		if err := runListViews(apiURL); err != nil {
			log.Fatalf("List views failed: %v", err)
		}
		os.Exit(0)
	}

	// Switch accounts
	if switchAccounts {
		apiURL := getEnv("REQUESTBITE_API_URL", serverURL)
		if err := runSwitchAccounts(apiURL); err != nil {
			log.Fatalf("Switch accounts failed: %v", err)
		}
		os.Exit(0)
	}

	// Check for updates (unless disabled or running in development)
	if !noUpgradeCheck && !isRunningInDevelopment() {
		checkForUpdates()
	}

	// Validate flags
	if resume && ephemeralPort != 0 {
		log.Fatal("Error: --resume and --ephemeral cannot be used together")
	}
	if !resume && ephemeralPort == 0 {
		printHelp()
		os.Exit(0)
	}

	cfg, err := loadOrCreateConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
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
	connectToTunnelServer(ctx, serverURL, clientID, localAddr, expiresAt)

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

func connectToTunnelServer(ctx context.Context, serverURL, clientID, localAddr string, expiresAt time.Time) {
	muxURL := toWSURL(serverURL) + "/tunnel/mux?client_id=" + clientID
	ws, resp, err := websocket.DefaultDialer.Dial(muxURL, nil)
	if err != nil {
		if resp != nil {
			log.Fatalf("mux dial failed (HTTP %d): %v", resp.StatusCode, err)
		}
		log.Fatalf("mux dial failed: %v", err)
	}
	defer ws.Close()

	// The tunnel client accepts streams opened by the server → yamux.Server role.
	session, err := yamux.Server(newWSConn(ws), nil)
	if err != nil {
		log.Fatalf("yamux session failed: %v", err)
	}
	defer session.Close()

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
			return
		case <-expiry.C:
			log.Printf("Tunnel expired. Disconnecting.")
			return
		case err := <-errCh:
			log.Printf("mux session closed: %v", err)
			return
		case stream := <-streamCh:
			go handleTunneledConnection(stream, localAddr)
		}
	}
}

// handleTunneledConnection proxies one HTTP request/response between an inbound
// yamux stream and the local service, logging the method, path, status, and duration.
// For WebSocket upgrades (101) it falls back to a raw bidirectional copy after
// forwarding the handshake, preserving any bytes already buffered by the readers.
func handleTunneledConnection(stream net.Conn, localAddr string) {
	defer stream.Close()

	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		log.Printf("local dial failed (%s): %v", localAddr, err)
		return
	}
	defer localConn.Close()

	start := time.Now()

	// Keep buffered readers in scope — needed for the WebSocket fallback so that
	// any bytes read ahead past the HTTP headers are not lost.
	streamBuf := bufio.NewReader(stream)
	localBuf := bufio.NewReader(localConn)

	req, err := http.ReadRequest(streamBuf)
	if err != nil {
		log.Printf("failed to read request: %v", err)
		return
	}

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
	ws     *websocket.Conn
	reader io.Reader
}

func newWSConn(ws *websocket.Conn) *wsConn {
	return &wsConn{ws: ws}
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
			return 0, err
		}
		if msgType == websocket.CloseMessage {
			return 0, io.EOF
		}
		c.reader = r
	}
}

func (c *wsConn) Write(b []byte) (int, error) {
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

// runLogin performs the OIDC Authorization Code + PKCE login flow.
func runLogin(apiURL string) error {
	clientID := getEnv("OAUTH_CLIENT_ID", "")
	if clientID == "" {
		return errors.New("OAUTH_CLIENT_ID is not set")
	}
	scopes := getEnv("OAUTH_SCOPES", "openid email profile")
	callbackURL := getEnv("OAUTH_CALLBACK_URL", "http://localhost:7332/auth/callback")

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
		clientID := getEnv("OAUTH_CLIENT_ID", "")
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
	doRequest := func(token string) (*http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(apiURL, "/")+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultClient.Do(req)
	}

	resp, err := doRequest(accessToken)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && cfg.RefreshToken != "" {
		resp.Body.Close()
		clientID := getEnv("OAUTH_CLIENT_ID", "")
		newAccess, newRefresh, refreshErr := refreshAccessToken(apiURL, cfg.RefreshToken, clientID)
		if refreshErr != nil {
			return nil, fmt.Errorf("session expired and token refresh failed: %w", refreshErr)
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
	fmt.Printf("Name:        %s\n", selected.Name)
	fmt.Printf("Capture URL: %s\n", selected.CaptureURL)
	fmt.Printf("ID:          %s\n", selected.ID)
	fmt.Println()
	fmt.Printf("To stream request info, run \"rbite --views-tail %s\"\n", selected.ID)
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

	hqURL := strings.TrimRight(getEnv("HQ_URL", ""), "/")
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
		clientID := getEnv("OAUTH_CLIENT_ID", "")
		newAccess, newRefresh, refreshErr := refreshAccessToken(apiURL, cfg.RefreshToken, clientID)
		if refreshErr != nil {
			return fmt.Errorf("session expired and token refresh failed: %w", refreshErr)
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
