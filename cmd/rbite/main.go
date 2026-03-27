package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
)

// Version information — set via ldflags at build time.
var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

type CreateEphemeralRequest struct {
	Port int `json:"port"`
}

type CreateEphemeralResponse struct {
	URL string `json:"url"`
}

type controlMsg struct {
	Type   string `json:"type"`
	ConnID string `json:"conn_id"`
}

func main() {
	// Command line flags
	var (
		ephemeralPort = flag.Int("e", 0, "Create ephemeral tunnel to localhost port")
		showVersion   = flag.Bool("version", false, "Show version information")
		showHelp      = flag.Bool("help", false, "Show help information")
		serverURL     = flag.String("server", "http://localhost:8080", "Tunnel server URL")
	)
	flag.Parse()

	// Show version
	if *showVersion {
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
	if *showHelp {
		fmt.Printf("rbite v%s\n\n", Version)
		fmt.Println("Usage:")
		fmt.Printf("  rbite [options]\n\n")
		fmt.Println("Options:")
		flag.PrintDefaults()
		os.Exit(0)
	}

	// Validate ephemeral port
	if *ephemeralPort == 0 {
		log.Fatal("Error: -e flag is required to specify the localhost port")
	}

	// Create ephemeral tunnel
	ephemeralURL, err := createEphemeralTunnel(*serverURL, *ephemeralPort)
	if err != nil {
		log.Fatalf("Failed to create ephemeral tunnel: %v", err)
	}

	fmt.Printf("Ephemeral tunnel created!\n")
	fmt.Printf("Internet endpoint: %s\n", ephemeralURL)
	fmt.Printf("Local service: localhost:%d\n", *ephemeralPort)
	fmt.Printf("Press Ctrl+C to stop\n\n")

	// Connect to tunnel server
	localAddr := fmt.Sprintf("localhost:%d", *ephemeralPort)
	connectToTunnelServer(*serverURL, localAddr)
}

func createEphemeralTunnel(serverURL string, port int) (string, error) {
	// Send POST request to create ephemeral tunnel
	resp, err := http.Post(serverURL+"/v1/ephemeral", "application/json", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create ephemeral tunnel: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var ephemeralResp CreateEphemeralResponse
	if err := json.NewDecoder(resp.Body).Decode(&ephemeralResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %v", err)
	}

	return ephemeralResp.URL, nil
}

func connectToTunnelServer(serverURL, localAddr string) {
	ctrlURL := serverURL + "/tunnel/connect"
	ws, resp, err := websocket.DefaultDialer.Dial(ctrlURL, nil)
	if err != nil {
		if resp != nil {
			log.Fatalf("control dial failed (HTTP %d): %v", resp.StatusCode, err)
		}
		log.Fatalf("control dial failed: %v", err)
	}
	defer ws.Close()

	log.Printf("Connected to tunnel server, waiting for connections...")

	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			log.Fatalf("control channel closed: %v", err)
		}

		var msg controlMsg
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("unrecognised control message: %s", raw)
			continue
		}

		if msg.Type == "new_connection" {
			log.Printf("New connection request connId=%s", msg.ConnID)
			go handleTunneledConnection(serverURL, msg.ConnID, localAddr)
		}
	}
}

// handleTunneledConnection opens a WebSocket data stream back to the server for
// the given connId, connects to the local service, and bidirectionally proxies
// all bytes between them.
func handleTunneledConnection(serverURL, connID, localAddr string) {
	// 1. Open data stream WebSocket to the server.
	streamURL := serverURL + "/tunnel/stream/" + connID
	streamWS, _, err := websocket.DefaultDialer.Dial(streamURL, nil)
	if err != nil {
		log.Printf("stream dial failed (connId=%s): %v", connID, err)
		return
	}
	defer streamWS.Close()

	// 2. Connect to the local service.
	localConn, err := net.Dial("tcp", localAddr)
	if err != nil {
		log.Printf("local service dial failed (connId=%s, addr=%s): %v", connID, localAddr, err)
		return
	}
	defer localConn.Close()

	log.Printf("Bridging connId=%s <-> %s", connID, localAddr)

	// 3. Wrap the WebSocket as a net.Conn for io.Copy.
	streamConn := newWSConn(streamWS)

	// 4. Bidirectional copy: tunnel stream <-> local service.
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(localConn, streamConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(streamConn, localConn)
		done <- struct{}{}
	}()
	<-done

	log.Printf("Connection closed connId=%s", connID)
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

// Ensure wsConn satisfies io.ReadWriter (used by io.Copy).
var _ io.ReadWriter = (*wsConn)(nil)