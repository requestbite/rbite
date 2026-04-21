[![Release](https://github.com/requestbite/rbite/actions/workflows/release.yml/badge.svg)](https://github.com/requestbite/rbite/actions/workflows/release.yml)

# RequestBite RBite CLI

## About

This repository hosts the RequestBite RBite CLI which is the CLI app for
the [RequestBite][rb] service. It's currently in active development and at the
moment it can be used to set up ephemeral RequestBite Tunnel tunnels that can be
used to expose services running on `localhost` to the public Internet - perfect
for when you need to demo something you're building or when you need to reach
a locally running service from across the Internet.

[rb]: https://requestbite.com

## Installation

### Quick Install

Install the latest release on MacOS or Linux like so (it also installs the
companion app [rbite-proxy](https://github.com/requestbite/proxy):

```bash
curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash
```

The binary will be installed to `~/.local/bin` by default.

### Custom Installation Directory

To install the latest release to a custom directory, do like so:

```bash
curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash -s -- --prefix $HOME/bin
```

### Install Older Version

To install a specific version (in this example, version 0.3.1), do like so:

```bash
curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash -s -- --version 0.3.1
```

Please note that this only affects the `rbite` app, not the companion
`rbite-proxy` app.

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

Full up-to-date documentation about what you can do with RBite CLI can be found
at <https://docs.requestbite.com/rbite/>. When installed, you can also get
detailed usage information by running:

```bash
man rbite
```
