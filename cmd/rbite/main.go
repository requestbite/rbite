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
	flag.Usage = printHelp
	flag.Parse()

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
	if err := saveConfig(cfg); err != nil {
		return err
	}

	fmt.Println("Login successful. Access token stored in ~/.config/rbite/config.yaml")
	return nil
}
