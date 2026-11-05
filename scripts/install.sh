#!/bin/bash
# msgvault installer
# Usage: curl -fsSL https://raw.githubusercontent.com/unstaticlabs/msgvault/main/scripts/install.sh | bash

set -e

REPO="unstaticlabs/msgvault"
GITHUB_BASE_URL="https://github.com"
BINARY_NAME="msgvault"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info() { echo -e "${GREEN}$1${NC}"; }
warn() { echo -e "${YELLOW}$1${NC}"; }
error() { echo -e "${RED}$1${NC}" >&2; exit 1; }

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Darwin) echo "darwin" ;;
        Linux) echo "linux" ;;
        *) error "Unsupported OS: $(uname -s). msgvault supports macOS and Linux." ;;
    esac
}

# Detect architecture
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64) echo "amd64" ;;
        aarch64|arm64) echo "arm64" ;;
        *) error "Unsupported architecture: $(uname -m)" ;;
    esac
}

# Find install directory
find_install_dir() {
    if [ -w "/usr/local/bin" ]; then
        echo "/usr/local/bin"
    else
        mkdir -p "$HOME/.local/bin"
        echo "$HOME/.local/bin"
    fi
}

# Download with curl or wget
download() {
    local url="$1"
    local output="$2"
    if command -v curl &>/dev/null; then
        curl -fsSL "$url" -o "$output"
    elif command -v wget &>/dev/null; then
        wget -q "$url" -O "$output"
    else
        error "Neither curl nor wget found"
    fi
}

# Get latest release version
get_latest_version() {
    # Use the HTML /releases/latest endpoint, which 302-redirects to
    # /releases/tag/<version>. Unlike api.github.com it is not rate-limited
    # at 60 req/hr per IP, so users behind shared NAT / VPN don't get 403.
    local url="${GITHUB_BASE_URL}/${REPO}/releases/latest"
    local final_url=""
    if command -v curl &>/dev/null; then
        final_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$url") || return 1
    elif command -v wget &>/dev/null; then
        final_url=$(wget --spider -S "$url" 2>&1 \
            | awk 'tolower($1)=="location:" {print $2}' \
            | tail -1 \
            | tr -d '\r\n') || return 1
    else
        return 1
    fi
    case "$final_url" in
        */releases/tag/*) echo "${final_url##*/releases/tag/}" ;;
        *) return 1 ;;
    esac
}

# Verify checksum
verify_checksum() {
    local file="$1"
    local checksums_file="$2"
    local filename="$3"
    local required="${4:-false}"

    if [ ! -f "$checksums_file" ]; then
        [ "$required" != true ] || error "Checksum file is required for the Linux release"
        warn "Checksum file not available, skipping verification"
        return 0
    fi

    local expected=$(awk -v f="$filename" '{gsub(/^\*/, "", $2); if ($2==f) {print $1; exit}}' "$checksums_file")
    if [ -z "$expected" ]; then
        [ "$required" != true ] || error "No checksum found for $filename"
        warn "No checksum found for $filename, skipping verification"
        return 0
    fi

    local actual
    if command -v sha256sum &>/dev/null; then
        actual=$(sha256sum "$file" | cut -d' ' -f1)
    elif command -v shasum &>/dev/null; then
        actual=$(shasum -a 256 "$file" | cut -d' ' -f1)
    else
        [ "$required" != true ] || error "A sha256 tool is required for the Linux release"
        warn "No sha256 tool available, skipping verification"
        return 0
    fi

    if [ "$expected" != "$actual" ]; then
        error "Checksum verification failed!\n  Expected: $expected\n  Actual:   $actual"
    fi

    info "Checksum verified"
}

# Stage on the destination filesystem, then publish one complete executable.
# The Linux pair is ordered helper-first/main-last, not a two-file transaction.
install_binary() {
    local source="$1"
    local destination="$2"
    local install_dir="${destination%/*}"
    local stage
    local command_prefix=()
    if [ ! -w "$install_dir" ]; then
        command_prefix=(sudo)
    fi
    [ ! -d "$destination" ] || return 1
    stage=$("${command_prefix[@]}" mktemp "$install_dir/.msgvault-install.XXXXXX") || return 1
    if ! "${command_prefix[@]}" cp "$source" "$stage" ||
       ! "${command_prefix[@]}" chmod 755 "$stage" ||
       ! "${command_prefix[@]}" mv -f "$stage" "$destination"; then
        "${command_prefix[@]}" rm -f "$stage"
        return 1
    fi
}

# Install from GitHub releases
install_from_release() {
    local os="$1"
    local arch="$2"
    local install_dir="$3"

    info "Fetching latest release..."
    local version
    version=$(get_latest_version) || return 1

    if [ -z "$version" ]; then
        return 1
    fi

    info "Found version: $version"

    local platform="${os}_${arch}"
    local filename="${BINARY_NAME}_${version#v}_${platform}.tar.gz"
    local base_url="${GITHUB_BASE_URL}/${REPO}/releases/download/${version}"

    local tmpdir
    tmpdir=$(mktemp -d) || return 1
    trap "rm -rf -- $(printf '%q' "$tmpdir")" EXIT

    info "Downloading ${filename}..."
    if ! download "${base_url}/${filename}" "$tmpdir/release.tar.gz"; then
        return 1
    fi

    # Download and verify checksum
    if download "${base_url}/SHA256SUMS" "$tmpdir/SHA256SUMS" 2>/dev/null; then
        verify_checksum "$tmpdir/release.tar.gz" "$tmpdir/SHA256SUMS" "$filename" "$([ "$os" = linux ] && echo true || echo false)"
    else
        [ "$os" != linux ] || error "Could not download SHA256SUMS for the Linux release"
        warn "WARNING: Could not download SHA256SUMS — integrity not verified"
    fi

    info "Extracting..."
    tar -xzf "$tmpdir/release.tar.gz" -C "$tmpdir" || return 1

    # Linux releases contain a main executable and its linked Codex helper.
    # Keep older helpers and publish the main executable last: a running
    # predecessor continues using its own helper through the update.
    if [ "$os" = linux ]; then
        local helper="$tmpdir/msgvault-codex-bridge"
        [ -f "$helper" ] && [ ! -L "$helper" ] || error "Codex helper not found in Linux archive"
        local helper_digest helper_hash
        if command -v sha256sum &>/dev/null; then
            helper_hash=$(sha256sum "$helper") || return 1
        else
            helper_hash=$(shasum -a 256 "$helper") || return 1
        fi
        helper_digest="${helper_hash%% *}"
        [ -f "$tmpdir/${BINARY_NAME}" ] && [ ! -L "$tmpdir/${BINARY_NAME}" ] || error "Binary not found in archive"
        install_binary "$helper" "$install_dir/msgvault-codex-bridge.$helper_digest" || return 1
    fi

    if [ -f "$tmpdir/${BINARY_NAME}" ]; then
        install_binary "$tmpdir/${BINARY_NAME}" "$install_dir/${BINARY_NAME}" || return 1
    else
        error "Binary not found in archive"
    fi

    # macOS code signing
    if [ "$os" = "darwin" ] && [ -f "$install_dir/${BINARY_NAME}" ]; then
        codesign -s - "$install_dir/${BINARY_NAME}" 2>/dev/null || true
    fi

    return 0
}

# Main
main() {
    info "Installing msgvault..."
    echo

    local os=$(detect_os)
    local arch=$(detect_arch)
    local install_dir=$(find_install_dir)

    info "Platform: ${os}/${arch}"
    info "Install directory: ${install_dir}"
    echo

    if install_from_release "$os" "$arch" "$install_dir"; then
        info "Installed from GitHub release"
    else
        error "Installation failed. Please check https://github.com/${REPO}/releases for available builds."
    fi

    echo
    info "Installation complete!"
    echo

    # Check PATH
    if ! echo "$PATH" | grep -q "$install_dir"; then
        warn "Add this to your shell profile:"
        echo "  export PATH=\"\$PATH:$install_dir\""
        echo
    fi

    echo "Get started:"
    echo "  msgvault init-db"
    echo "  msgvault add-account you@gmail.com"
    echo "  msgvault sync-full you@gmail.com --limit 100"
    echo "  msgvault tui"
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
    main "$@"
fi
