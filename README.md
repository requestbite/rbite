# RequestBite RBite CLI

This repository hosts the RequestBite RBite CLI which is the premier CLI app for
the [RequestBite][rb] service. It's currently in active development and at the
moment it can be used to set up ephemeral RequestBite Tunnel tunnels that can be
used to expose services running on `localhost` to the public Internet - perfect
for when you need to demo something you're building or when you need to reach
a locally running service from across the Internet.

[rb]: https://requestbite.com

## Installation

### Quick Install (Recommended)

Install the latest release on MacOS or Linux like so:

```bash
curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash
```

The binary will be installed to `~/.local/bin` by default.

### Custom Installation Directory

To install the latest release to a custom directory, do like so:

```bash
curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash -s -- --prefix=$HOME/bin
```

### Install Older Version

To install a specific version (in this example, version 0.3.1), do like so:

```bash
curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash -s -- --version=0.3.1
```

### Manual Download

Download pre-built binaries from [GitHub
Releases](https://github.com/requestbite/rbite/releases).

**Supported Platforms:**

| OS      | Architecture          | Binary Name                   |
|---------|-----------------------|-------------------------------|
| macOS   | Intel (x86-64)        | `rbite-*-darwin-amd64.tar.gz` |
| macOS   | Apple Silicon (ARM64) | `rbite-*-darwin-arm64.tar.gz` |
| Linux   | x86-64                | `rbite-*-linux-amd64.tar.gz`  |
| Windows | x86-64                | `rbite-*-windows-amd64.zip`   |

After downloading, extract the archive and move the binary to a directory in
your PATH:

```bash
# macOS/Linux
tar -xzf rbite-*.tar.gz
mv rbite/rbite ~/.local/bin/

# Make sure ~/.local/bin is in your PATH
export PATH="$HOME/.local/bin:$PATH"
```

## Usage

Currently the `rbite` CLI can be used to create ephemeral tunnels for exposing
local HTTP services so that they're accessible from the public Internet.
Ephemeral tunnels can be created completely free-of-charge and does not require
any RequestBite account. Ephemeral endpoints have the following limitations:

* Each new endpoint has a random name
* They expire after 1 hour (possible to resume if closed accidentally)
* Can transfer at most 1 GB of data

Persistent tunnels that do not expire and with bigger transfer limits will be
available soon through RequestBite accounts.

The following type of data can be transferred through a tunnel:

* HTTP requests
* SSE streams
* WebSocket streams

### Examples

#### Create ephemeral tunnel

Create ephemeral tunnel (in this case exposing `http://localhost:8080`):

```bash
rbite -e 8080
```

This results in output like so:

```plaintext
 ______
[______]  RequestBite Tunnel ⚡️
__|  |_________________________

Ephemeral tunnel created. Expires at 16:20:17 (in 60 minutes).
> Internet endpoint: https://958b846f.et.rbite.dev
> Local service: http://localhost:8080
Press Ctrl+C to stop

2026/04/02 15:20:17 Connected to tunnel server, waiting for connections...
```

You can now click the link after `Internet endpoint` above to access the local
service.

#### Resume session

If you have a non-expired session, you can resume the last one like so:

```bash
rbite --resume
```

#### Session details

As soon as you have a running tunnel, it will display whatever requests are made
to your local endpoint like so (example below):

```plaintext
2026/04/02 16:02:55 Connected to tunnel server, waiting for connections...
2026/04/02 16:02:59 GET / 200 3ms
2026/04/02 16:02:59 GET /@vite/client 200 94ms
2026/04/02 16:02:59 GET /src/main.jsx 200 52ms
2026/04/02 16:02:59 GET /node_modules/.vite/deps/preact_debug.js?v=206b7cd4 200 1ms
2026/04/02 16:02:59 GET /node_modules/.vite/deps/preact.js?v=206b7cd4 200 1ms
2026/04/02 16:02:59 GET /src/index.css 200 1ms
2026/04/02 16:02:59 GET /src/app.jsx 200 0s
```

#### Session summary

When your tunnel session expires or you close it manually by hitting `ctrl-C`,
you will get a summary of what was transferred like so:

```plaintext
--- Session summary ---
Requests served:  101
Data transferred: 2.85 MB
```
