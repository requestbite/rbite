package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/joho/godotenv"
)

// Version information — set via ldflags at build time.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

type CreateEphemeralRequest struct {
	Port     int    `json:"port"`
	ClientID string `json:"client_id"`
}

type CreateEphemeralResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func printHelp() {
	defaultServer := getEnv("TUNNEL_SERVER_URL", "http://localhost:8080")
	fmt.Printf("RequestBite Tunnel v%s\n\n", Version)
	fmt.Println("Usage:")
	fmt.Printf("  rbite [options]\n\n")
	fmt.Println("Options:")
	fmt.Printf("  -e, --expose int      Port to expose via ephemeral tunnel\n")
	fmt.Printf("  -h, --help            Show help information\n")
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
		serverURL     string
	)
	defaultServer := getEnv("TUNNEL_SERVER_URL", "http://localhost:8080")

	flag.IntVar(&ephemeralPort, "e", 0, "")
	flag.IntVar(&ephemeralPort, "expose", 0, "")
	flag.BoolVar(&showVersion, "v", false, "")
	flag.BoolVar(&showVersion, "version", false, "")
	flag.BoolVar(&showHelp, "h", false, "")
	flag.BoolVar(&showHelp, "help", false, "")
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

	// Validate ephemeral port
	if ephemeralPort == 0 {
		log.Fatal("Error: -e/--expose flag is required to specify the localhost port")
	}

	clientID := uuid.New().String()

	// Create ephemeral tunnel
	ephemeralResp, err := createEphemeralTunnel(serverURL, ephemeralPort, clientID)
	if err != nil {
		log.Fatalf("Failed to create ephemeral tunnel: %v", err)
	}

	fmt.Printf("Ephemeral tunnel created on %s. Expires at %s.\n", serverHostname(serverURL), ephemeralResp.ExpiresAt.Local().Format("15:04:05"))
	fmt.Printf("Internet endpoint: https://%s\n", ephemeralResp.URL)
	fmt.Printf("Local service: http://localhost:%d\n", ephemeralPort)
	fmt.Printf("Press Ctrl+C to stop\n\n")

	// Connect to tunnel server
	localAddr := fmt.Sprintf("localhost:%d", ephemeralPort)
	connectToTunnelServer(serverURL, clientID, localAddr, ephemeralResp.ExpiresAt)
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

func toWSURL(serverURL string) string {
	switch {
	case len(serverURL) >= 8 && serverURL[:8] == "https://":
		return "wss://" + serverURL[8:]
	case len(serverURL) >= 7 && serverURL[:7] == "http://":
		return "ws://" + serverURL[7:]
	}
	return serverURL
}

func connectToTunnelServer(serverURL, clientID, localAddr string, expiresAt time.Time) {
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
		case <-expiry.C:
			log.Printf("Tunnel expired. Disconnecting.")
			return
		case err := <-errCh:
			log.Fatalf("mux session closed: %v", err)
		case stream := <-streamCh:
			go handleTunneledConnection(stream, localAddr)
		}
	}
}

// handleTunneledConnection proxies bytes between an inbound yamux stream
// (from the tunnel server) and the local service.
func handleTunneledConnection(stream net.Conn, localAddr string) {
	defer stream.Close()

	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		log.Printf("local service dial failed (%s): %v", localAddr, err)
		return
	}
	defer localConn.Close()

	done := make(chan struct{}, 2)
	go func() {
		io.Copy(localConn, stream)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(stream, localConn)
		done <- struct{}{}
	}()
	<-done
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