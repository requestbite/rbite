#!/usr/bin/env bash
#
# install.sh - Install rbite from GitHub releases
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash
#   curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash -s -- --prefix=$HOME/bin
#   curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash -s -- --version 0.0.1
#

set -euo pipefail

# Configuration
BINARY_NAME="rbite"
GITHUB_REPO="requestbite/rbite"
DEFAULT_INSTALL_DIR="$HOME/.local/bin"

# Parse command line arguments
VERSION=""
PREFIX=""

while [[ $# -gt 0 ]]; do
  case $1 in
  --version)
    VERSION="$2"
    shift 2
    ;;
  --prefix)
    PREFIX="$2"
    shift 2
    ;;
  --help)
    cat <<EOF
rbite - Installation Script

Usage:
  install.sh [options]

Options:
  --version VERSION    Install specific version (e.g., 0.0.1)
  --prefix PATH        Install to PATH/bin (default: ~/.local/bin)
  --help               Show this help message

Examples:
  # Install latest version to default location
  ./install.sh

  # Install specific version
  ./install.sh --version 0.0.1

  # Install to custom location
  ./install.sh --prefix \$HOME

  # One-line install from GitHub
  curl -fsSL https://raw.githubusercontent.com/requestbite/rbite/main/install.sh | bash

EOF
    exit 0
    ;;
  *)
    echo "Unknown option: $1"
    echo "Run with --help for usage information"
    exit 1
    ;;
  esac
done

# Colors (only if terminal supports it)
if [ -t 1 ]; then
  COLOR_RESET='\033[0m'
  COLOR_BOLD='\033[1m'
  COLOR_GREEN='\033[32m'
  COLOR_BLUE='\033[34m'
  COLOR_RED='\033[31m'
  COLOR_YELLOW='\033[33m'
else
  COLOR_RESET=''
  COLOR_BOLD=''
  COLOR_GREEN=''
  COLOR_BLUE=''
  COLOR_RED=''
  COLOR_YELLOW=''
fi

# Utility functions
info() {
  echo -e "${COLOR_BOLD}${COLOR_BLUE}==>${COLOR_RESET} ${COLOR_BOLD}$*${COLOR_RESET}"
}

success() {
  echo -e "${COLOR_GREEN}✓${COLOR_RESET} $*"
}

error() {
  echo -e "${COLOR_RED}✗ Error:${COLOR_RESET} $*" >&2
}

warning() {
  echo -e "${COLOR_YELLOW}⚠${COLOR_RESET} $*"
}

die() {
  error "$*"
  exit 1
}

# Check if command exists
command_exists() {
  command -v "$1" >/dev/null 2>&1
}

# Cleanup function
TEMP_DIR=""
cleanup() {
  if [ -n "$TEMP_DIR" ] && [ -d "$TEMP_DIR" ]; then
    rm -rf "$TEMP_DIR"
  fi
}
trap cleanup EXIT INT TERM

# Check prerequisites
check_prerequisites() {
  local missing=()

  if ! command_exists curl; then
    missing+=("curl")
  fi

  if ! command_exists tar; then
    missing+=("tar")
  fi

  if ! command_exists shasum && ! command_exists sha256sum; then
    missing+=("shasum or sha256sum")
  fi

  if [ ${#missing[@]} -gt 0 ]; then
    error "Missing required tools:"
    for tool in "${missing[@]}"; do
      echo "  - $tool"
    done
    exit 1
  fi
}

# Detect operating system
detect_os() {
  local os
  os="$(uname -s)"

  case "$os" in
  Linux*)
    echo "linux"
    ;;
  Darwin*)
    echo "darwin"
    ;;
  *)
    die "Unsupported operating system: $os (supported: Linux, macOS)"
    ;;
  esac
}

# Detect architecture
detect_arch() {
  local arch
  arch="$(uname -m)"

  case "$arch" in
  x86_64)
    echo "amd64"
    ;;
  arm64 | aarch64)
    echo "arm64"
    ;;
  *)
    die "Unsupported architecture: $arch (supported: x86_64, arm64)"
    ;;
  esac
}

# Get latest version from GitHub
get_latest_version() {
  info "Fetching latest version from GitHub..." >&2

  local version
  version=$(curl -fsSL -H "User-Agent: rbite-installer" "https://api.github.com/repos/$GITHUB_REPO/releases/latest" |
    grep '"tag_name"' |
    sed -E 's/.*"tag_name": *"v?([^"]+)".*/\1/' || echo "")

  if [ -z "$version" ]; then
    die "Failed to fetch latest version from GitHub API"
  fi

  echo "$version"
}

# Determine installation directory
determine_install_dir() {
  local install_dir

  if [ -n "$PREFIX" ]; then
    install_dir="$PREFIX/bin"
  elif [ -d "$DEFAULT_INSTALL_DIR" ] || mkdir -p "$DEFAULT_INSTALL_DIR" 2>/dev/null; then
    install_dir="$DEFAULT_INSTALL_DIR"
  else
    install_dir="/usr/local/bin"
    warning "Cannot create $DEFAULT_INSTALL_DIR, will try system-wide installation"
    warning "This may require sudo privileges"
  fi

  echo "$install_dir"
}

# Check if directory is writable
is_writable() {
  local dir="$1"

  if [ -d "$dir" ]; then
    [ -w "$dir" ]
  else
    # Check parent directory
    local parent
    parent="$(dirname "$dir")"
    [ -w "$parent" ]
  fi
}

# Download and verify archive
download_and_verify() {
  local url="$1"
  local archive_name="$2"
  local checksum_url="$3"

  info "Downloading from GitHub releases..."
  if ! curl -fsSL --progress-bar "$url" -o "$archive_name"; then
    die "Failed to download $url"
  fi
  success "Downloaded $archive_name"

  # Download checksums
  info "Verifying checksum..."
  if ! curl -fsSL "$checksum_url" -o SHA256SUMS; then
    warning "Could not download checksums file, skipping verification"
    return 0
  fi

  # Verify checksum
  local expected_checksum
  expected_checksum=$(grep "$archive_name" SHA256SUMS | awk '{print $1}')

  if [ -z "$expected_checksum" ]; then
    warning "Checksum not found in SHA256SUMS, skipping verification"
    return 0
  fi

  local actual_checksum
  if command_exists shasum; then
    actual_checksum=$(shasum -a 256 "$archive_name" | awk '{print $1}')
  else
    actual_checksum=$(sha256sum "$archive_name" | awk '{print $1}')
  fi

  if [ "$expected_checksum" != "$actual_checksum" ]; then
    die "Checksum verification failed!
Expected: $expected_checksum
Actual:   $actual_checksum"
  fi

  success "Checksum verified"
}

# Extract archive contents
extract_binary() {
  local archive_name="$1"

  info "Extracting archive..."

  if [[ "$archive_name" == *.tar.gz ]]; then
    tar -xzf "$archive_name"
    if [ ! -f "$BINARY_NAME/$BINARY_NAME" ]; then
      die "Binary not found in archive"
    fi
    mv "$BINARY_NAME/$BINARY_NAME" "${BINARY_NAME}.tmp"
    # Keep completions/ and man/ directories in place for later installation
    if [ -d "$BINARY_NAME/completions" ]; then
      mv "$BINARY_NAME/completions" completions
    fi
    if [ -d "$BINARY_NAME/man" ]; then
      mv "$BINARY_NAME/man" man
    fi
    rm -rf "$BINARY_NAME"
    mv "${BINARY_NAME}.tmp" "$BINARY_NAME"
  else
    die "Unsupported archive format: $archive_name"
  fi

  success "Extracted archive"
}

# Install binary
install_binary() {
  local install_dir="$1"
  local use_sudo=false

  # Create install directory if needed
  if [ ! -d "$install_dir" ]; then
    if ! mkdir -p "$install_dir" 2>/dev/null; then
      use_sudo=true
    fi
  fi

  # Check if we need sudo
  if ! is_writable "$install_dir"; then
    use_sudo=true
  fi

  info "Installing to $install_dir..."

  if [ "$use_sudo" = true ]; then
    warning "Installation requires elevated privileges"
    if ! command_exists sudo; then
      die "sudo is required but not available"
    fi

    sudo mkdir -p "$install_dir"
    sudo rm -f "$install_dir/$BINARY_NAME"
    sudo cp "$BINARY_NAME" "$install_dir/"
    sudo chmod +x "$install_dir/$BINARY_NAME"
  else
    mkdir -p "$install_dir"
    rm -f "$install_dir/$BINARY_NAME"
    cp "$BINARY_NAME" "$install_dir/"
    chmod +x "$install_dir/$BINARY_NAME"
  fi

  success "Installed $BINARY_NAME to $install_dir"
}

# Install shell completions
install_completions() {
  local os="$1"

  # Fish completion — user-level, works on both Linux and macOS
  local fish_completion_dir="$HOME/.config/fish/completions"
  if command_exists fish; then
    mkdir -p "$fish_completion_dir"
    if [ -f "completions/rbite.fish" ]; then
      cp "completions/rbite.fish" "$fish_completion_dir/rbite.fish"
      success "Installed Fish completion to $fish_completion_dir/rbite.fish"
    fi
  fi

  # Bash completion
  local bash_completion_dir=""
  if [ "$os" = "darwin" ]; then
    # Prefer Homebrew location; fall back to user-level XDG path
    if command_exists brew; then
      bash_completion_dir="$(brew --prefix 2>/dev/null)/etc/bash_completion.d"
    fi
  fi
  if [ -z "$bash_completion_dir" ]; then
    bash_completion_dir="$HOME/.local/share/bash-completion/completions"
  fi

  if [ -f "completions/rbite.bash" ]; then
    mkdir -p "$bash_completion_dir"
    cp "completions/rbite.bash" "$bash_completion_dir/rbite"
    success "Installed Bash completion to $bash_completion_dir/rbite"
  fi
}

# Install man page
install_man_page() {
  local man_dir="$HOME/.local/share/man/man1"

  if [ -f "man/rbite.1" ]; then
    mkdir -p "$man_dir"
    cp "man/rbite.1" "$man_dir/rbite.1"
    success "Installed man page to $man_dir/rbite.1"

    # Rebuild the manual page index when tools are available
    if command_exists mandb; then
      mandb -q "$HOME/.local/share/man" 2>/dev/null || true
    elif command_exists makewhatis; then
      makewhatis "$HOME/.local/share/man" 2>/dev/null || true
    fi
  fi
}

# Verify installation
verify_installation() {
  local install_dir="$1"
  local binary_path="$install_dir/$BINARY_NAME"

  if [ ! -f "$binary_path" ]; then
    die "Installation verification failed: $binary_path not found"
  fi

  if [ ! -x "$binary_path" ]; then
    die "Installation verification failed: $binary_path is not executable"
  fi

  # Try to run --version
  if "$binary_path" --version >/dev/null 2>&1; then
    success "Installation verified"
  else
    warning "Binary exists but --version check failed"
  fi
}

# Check if install directory is in PATH
check_path() {
  local install_dir="$1"

  if ! echo "$PATH" | grep -q "$install_dir"; then
    warning "Installation directory is not in your PATH"
    echo ""
    echo "Add to PATH by adding this line to your shell configuration:"
    echo "  export PATH=\"$install_dir:\$PATH\""
    echo ""

    # Detect shell and config file
    local shell_config=""
    if [ -n "${BASH_VERSION:-}" ]; then
      shell_config="~/.bashrc or ~/.bash_profile"
    elif [ -n "${ZSH_VERSION:-}" ]; then
      shell_config="~/.zshrc"
    else
      shell_config="your shell configuration file"
    fi

    echo "Example for $shell_config:"
    echo "  echo 'export PATH=\"$install_dir:\$PATH\"' >> $shell_config"
    echo "  source $shell_config"
    echo ""
  fi
}

# Main installation function
main() {
  echo ""
  info "rbite - Installation Script"
  echo ""

  # Check prerequisites
  check_prerequisites

  # Detect platform
  local os
  local arch
  os=$(detect_os)
  arch=$(detect_arch)

  echo "Detected platform: $os/$arch"

  # Get version
  if [ -z "$VERSION" ]; then
    VERSION=$(get_latest_version)
  fi
  echo "Version: $VERSION"

  # Determine installation directory
  local install_dir
  install_dir=$(determine_install_dir)
  echo "Install directory: $install_dir"
  echo ""

  # Construct download URL
  local archive_name="${BINARY_NAME}-${VERSION}-${os}-${arch}.tar.gz"
  local base_url="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}"
  local download_url="${base_url}/${archive_name}"
  local checksum_url="${base_url}/SHA256SUMS"

  # Create temporary directory
  TEMP_DIR=$(mktemp -d)
  cd "$TEMP_DIR"

  # Download and verify
  download_and_verify "$download_url" "$archive_name" "$checksum_url"

  # Extract
  extract_binary "$archive_name"

  # Install
  install_binary "$install_dir"

  # Install shell completions
  install_completions "$os"

  # Install man page
  install_man_page

  # Verify
  verify_installation "$install_dir"

  echo ""
  success "Installation complete!"
  echo ""

  # Check PATH
  check_path "$install_dir"

  # Show usage
  echo "Usage:"
  echo "  $BINARY_NAME --help"
  echo "  $BINARY_NAME --version"
  echo ""
}

# Run main function
main
