package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"errors"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Version information — set via ldflags at build time.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

type Config struct {
	ClientID string `yaml:"clientId"`
}

// loadOrCreateConfig reads ~/.config/rbite/config.yaml, creating it with a
// fresh UUIDv4 clientId if it does not already exist.
func loadOrCreateConfig() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not determine home directory: %w", err)
	}
	cfgDir := filepath.Join(home, ".config", "rbite")
	cfgPath := filepath.Join(cfgDir, "config.yaml")

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
		out, marshalErr := yaml.Marshal(&cfg)
		if marshalErr != nil {
			return nil, fmt.Errorf("could not marshal config: %w", marshalErr)
		}
		if writeErr := os.WriteFile(cfgPath, out, 0o644); writeErr != nil {
			return nil, fmt.Errorf("could not write config file: %w", writeErr)
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
		out, marshalErr := yaml.Marshal(&cfg)
		if marshalErr != nil {
			return nil, fmt.Errorf("could not marshal config: %w", marshalErr)
		}
		if writeErr := os.WriteFile(cfgPath, out, 0o644); writeErr != nil {
			return nil, fmt.Errorf("could not write config file: %w", writeErr)
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

func printHelp() {
	defaultServer := getEnv("TUNNEL_SERVER_URL", "http://localhost:8080")
	fmt.Printf("RequestBite Tunnel v%s\n\n", Version)
	fmt.Println("Usage:")
	fmt.Printf("  rbite [options]\n\n")
	fmt.Println("Options:")
	fmt.Printf("  -e, --expose int      Port to expose via ephemeral tunnel\n")
	fmt.Printf("  -h, --help            Show help information\n")
	fmt.Printf("  -r, --resume          Resume the last session if it has not expired\n")
	fmt.Printf("  -s, --server string   Tunnel server URL (default %q)\n", defaultServer)
	fmt.Printf("  -v, --version         Show version information\n")
	fmt.Println()
}

func main() {
	// Load .env file
	_ = godotenv.Load()

	// Command line flags
	var (
		ephemeralPort int
		showVersion   bool
		showHelp      bool
		resume        bool
		serverURL     string
	)
	defaultServer := getEnv("TUNNEL_SERVER_URL", "http://localhost:8080")

	flag.IntVar(&ephemeralPort, "e", 0, "")
	flag.IntVar(&ephemeralPort, "expose", 0, "")
	flag.BoolVar(&showVersion, "v", false, "")
	flag.BoolVar(&showVersion, "version", false, "")
	flag.BoolVar(&showHelp, "h", false, "")
	flag.BoolVar(&showHelp, "help", false, "")
	flag.BoolVar(&resume, "r", false, "")
	flag.BoolVar(&resume, "resume", false, "")
	flag.StringVar(&serverURL, "s", defaultServer, "")
	flag.StringVar(&serverURL, "server", defaultServer, "")
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

	// Validate flags
	if resume && ephemeralPort != 0 {
		log.Fatal("Error: --resume and --expose cannot be used together")
	}
	if !resume && ephemeralPort == 0 {
		log.Fatal("Error: -e/--expose flag is required to specify the localhost port")
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
		fmt.Printf("Internet endpoint: https://%s\n", tunnelURL)
		fmt.Printf("Local service: http://localhost:%d\n", ephemeralPort)
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
		fmt.Printf("Ephemeral tunnel created. Expires at %s (in %d minutes).\n", expiresAt.Local().Format("15:04:05"), expiresIn)
		fmt.Printf("Internet endpoint: https://%s\n", tunnelURL)
		fmt.Printf("Local service: http://localhost:%d\n", ephemeralPort)
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