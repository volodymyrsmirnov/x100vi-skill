#!/bin/sh
# Runs the x100vi tool, downloading it on first use.
#
#   sh x100vi.sh doctor --json      run any x100vi command
#   sh x100vi.sh --self-update      download the latest release again
#
# The tool is one binary per platform, published on every GitHub release. It is downloaded once
# (checksum verified) into a cache folder and reused. Environment:
#   X100VI_BIN          use this binary instead (e.g. one you built yourself)
#   X100VI_RELEASE_URL  download from here instead of the latest GitHub release
#   X100VI_CACHE        cache folder (default: ~/Library/Caches/x100vi or ~/.cache/x100vi)
set -eu

RELEASE_URL="${X100VI_RELEASE_URL:-https://github.com/volodymyrsmirnov/x100vi-skill/releases/latest/download}"

if [ -n "${X100VI_BIN:-}" ]; then
    exec "$X100VI_BIN" "$@"
fi

case "$(uname -s)" in
    Darwin) asset="x100vi-darwin-universal"; default_cache="$HOME/Library/Caches/x100vi" ;;
    Linux)
        case "$(uname -m)" in
            x86_64|amd64) asset="x100vi-linux-amd64" ;;
            aarch64|arm64) asset="x100vi-linux-arm64" ;;
            *) echo "error: no x100vi build for $(uname -m) Linux" >&2; exit 127 ;;
        esac
        default_cache="${XDG_CACHE_HOME:-$HOME/.cache}/x100vi" ;;
    MINGW*|MSYS*|CYGWIN*) asset="x100vi-windows-amd64.exe"; default_cache="${LOCALAPPDATA:-$HOME}/x100vi" ;;
    *) echo "error: no x100vi build for $(uname -s)" >&2; exit 127 ;;
esac
cache="${X100VI_CACHE:-$default_cache}"
tool="$cache/$asset"

sandboxed() {
    [ -n "${CODEX_SANDBOX:-}" ] || [ "${CODEX_SANDBOX_NETWORK_DISABLED:-}" = "1" ] || [ -n "${X100VI_SANDBOX:-}" ]
}

fetch() {
    # fetch URL FILE. Progress and errors go to stderr so stdout stays clean for --json.
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL --retry 2 -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$2" "$1"
    else
        echo "error: neither curl nor wget is available to download the x100vi tool" >&2
        exit 127
    fi
}

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    else
        shasum -a 256 "$1" | cut -d' ' -f1
    fi
}

download() {
    if sandboxed; then
        # echo, not a here-document: the shell needs a temp file for those, which a sandbox denies
        echo "error: the x100vi tool is not downloaded yet and cannot be downloaded from inside the sandbox (no network there)" >&2
        echo "hint: Run this same command again outside the sandbox (ask for approval to run it unsandboxed). The camera also needs that: USB is not reachable from a sandbox." >&2
        exit 14
    fi
    mkdir -p "$cache"
    echo "x100vi: downloading $asset from $RELEASE_URL ..." >&2
    tmp="$tool.download.$$"
    sums="$cache/SHA256SUMS.$$"
    trap 'rm -f "$tmp" "$sums"' EXIT INT TERM
    if ! fetch "$RELEASE_URL/$asset" "$tmp" || ! fetch "$RELEASE_URL/SHA256SUMS" "$sums"; then
        echo "error: could not download the x100vi tool from $RELEASE_URL" >&2
        echo "hint: Check the network connection and try again, or download $asset from https://github.com/volodymyrsmirnov/x100vi-skill/releases and set X100VI_BIN to its path." >&2
        exit 127
    fi
    want=$(grep " $asset\$" "$sums" | cut -d' ' -f1 | head -n 1)
    got=$(sha256 "$tmp")
    if [ -z "$want" ] || [ "$want" != "$got" ]; then
        echo "error: the downloaded $asset does not match the release checksum; it was not installed" >&2
        exit 127
    fi
    chmod +x "$tmp"
    mv -f "$tmp" "$tool"
    rm -f "$sums"
    trap - EXIT INT TERM
}

if [ "${1:-}" = "--self-update" ]; then
    download
    exec "$tool" --version
fi

if [ ! -x "$tool" ]; then
    download
fi
exec "$tool" "$@"
