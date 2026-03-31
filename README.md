# rbite

`rbite` is the RequestBite tunnel client. It exposes a local HTTP service to the internet via an ephemeral tunnel — no port forwarding or firewall rules required.

## How it works

1. **Register a session** — `rbite` sends a POST to the tunnel server, registering a new ephemeral session tied to a persistent client ID (stored in `~/.config/rbite/config.yaml`). The server responds with a public URL and an expiry time.
2. **Open a mux connection** — `rbite` connects to the server over a WebSocket, on top of which a [yamux](https://github.com/hashicorp/yamux) multiplexed session is established. The client acts as the yamux server, accepting streams opened by the tunnel server.
3. **Proxy requests** — for each inbound yamux stream, `rbite` dials `localhost:<port>`, reads a full HTTP request from the stream, forwards it to the local service, reads the response, and writes it back into the stream. WebSocket upgrades (HTTP 101) are handled by falling back to a raw bidirectional copy after the handshake.
4. **Session summary** — when the tunnel closes (Ctrl+C, expiry, or server disconnect), `rbite` fetches and prints the total request count and data transferred for the session.

```
Internet → Tunnel server → WebSocket/yamux → rbite → localhost:<port>
```

### Client identity

On first run, `rbite` generates a UUIDv4 `clientId` and saves it to `~/.config/rbite/config.yaml`. This ID is sent with every request so the server can associate sessions with clients. Only one active session per client is allowed at a time.

## Usage

```
rbite [options]

Options:
  -e, --expose int      Port to expose via ephemeral tunnel
  -r, --resume          Resume the last session if it has not expired
  -s, --server string   Tunnel server URL (default: from TUNNEL_SERVER_URL env or http://localhost:8080)
  -v, --version         Show version information
  -h, --help            Show help information
```

### Examples

```bash
# Expose local port 3000
rbite -e 3000

# Expose port 8080 against a specific server
rbite -e 8080 -s https://api.t.rbite.dev

# Resume a previous session (reconnect after a disconnect)
rbite --resume
```

## Configuration

`rbite` loads `.env` from the working directory on startup. The following variables are recognised:

| Variable            | Default                     | Description                    |
|---------------------|-----------------------------|--------------------------------|
| `TUNNEL_SERVER_URL` | `http://localhost:8080`     | Tunnel server base URL         |
| `DEBUG`             | `false`                     | Enable debug logging           |
| `CONNECTION_TIMEOUT`| `30`                        | Connection timeout in seconds  |
| `READ_TIMEOUT`      | `60`                        | Read timeout in seconds        |
| `WRITE_TIMEOUT`     | `60`                        | Write timeout in seconds       |

A `.env` file is included in the repo pointing at the dev server (`https://api.dev.t.rbite.dev`).

## Building and running

### Prerequisites

- Go 1.21+
- [Air](https://github.com/air-verse/air) for hot-reload development (`go install github.com/air-verse/air@latest`)

### Makefile targets

| Target      | Description                                                                 |
|-------------|-----------------------------------------------------------------------------|
| `build`     | Build for the current platform into `build/rbite` (default)                 |
| `dev`       | Build and run with hot reload via Air; pass CLI args with `ARGS="..."`      |
| `build-all` | Cross-compile for macOS (amd64/arm64), Linux (amd64), and Windows (amd64)   |
| `release`   | Run `build-all`, then create `.tar.gz`/`.zip` archives and a `SHA256SUMS`   |
| `install`   | Build and copy the binary to `~/.local/bin/rbite`                           |
| `clean`     | Remove `build/`, `dist/`, and `tmp/`                                        |
| `version`   | Print version, build time, and git commit                                   |
| `help`      | Show all targets and examples                                               |

### Quick start

```bash
# Build for the current platform
make build

# Run (expose port 3000)
./build/rbite -e 3000
```

### Development with hot reload

```bash
# Start with hot reload (rebuilds on any .go / config file change)
make dev ARGS="-e 3000"
```

Air is configured in `.air.toml`. It watches Go and config files, rebuilds via `make build`, and cleans up the `tmp/` directory on exit.

### Cross-platform builds

```bash
make build-all
```

Binaries are written to `build/` with names like `rbite-<version>-<os>-<arch>`.

### Creating a release

```bash
make release
```

Produces versioned archives in `dist/` along with a `SHA256SUMS` checksum file. The version is derived automatically from the nearest git tag (e.g. `v1.2.3` → `1.2.3`).

### Install locally

```bash
make install
```

Copies the binary to `~/.local/bin/rbite`. Warns if that directory is not on `$PATH`.
