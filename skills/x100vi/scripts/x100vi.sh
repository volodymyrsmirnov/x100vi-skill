#!/bin/sh
# Runs the x100vi tool, installing it on first use.
#
#   sh x100vi.sh doctor --json      run any x100vi command
#   sh x100vi.sh --self-update      reinstall the latest version of the tool
#
# The tool is taken from PATH when it is already installed. Otherwise it is installed once
# with uv (or pipx). Set X100VI_SOURCE to install from somewhere else, e.g. a local checkout.
set -eu

SOURCE="${X100VI_SOURCE:-git+https://github.com/volodymyrsmirnov/x100vi-skill}"

find_tool() {
    if command -v x100vi >/dev/null 2>&1; then
        command -v x100vi
        return 0
    fi
    if command -v uv >/dev/null 2>&1; then
        bin_dir=$(uv tool dir --bin 2>/dev/null || true)
        if [ -n "$bin_dir" ] && [ -x "$bin_dir/x100vi" ]; then
            printf '%s\n' "$bin_dir/x100vi"
            return 0
        fi
    fi
    if [ -x "$HOME/.local/bin/x100vi" ]; then
        printf '%s\n' "$HOME/.local/bin/x100vi"
        return 0
    fi
    return 1
}

install_tool() {
    # $1 is "fresh" or "update". Installer output goes to stderr so stdout stays clean for --json.
    if command -v uv >/dev/null 2>&1; then
        echo "x100vi: installing the x100vi tool with uv from $SOURCE ..." >&2
        if [ "$1" = "update" ]; then
            uv tool install --force --reinstall "$SOURCE" >&2
        else
            uv tool install "$SOURCE" >&2
        fi
    elif command -v pipx >/dev/null 2>&1; then
        echo "x100vi: installing the x100vi tool with pipx from $SOURCE ..." >&2
        if [ "$1" = "update" ]; then
            pipx install --force "$SOURCE" >&2
        else
            pipx install "$SOURCE" >&2
        fi
    else
        cat >&2 <<MSG
error: the x100vi tool is not installed, and neither uv nor pipx is available to install it
hint: Install uv from https://docs.astral.sh/uv/getting-started/installation/ and run this again, or install the tool yourself: pipx install $SOURCE
MSG
        exit 127
    fi
}

if [ "${1:-}" = "--self-update" ]; then
    install_tool update
    tool=$(find_tool) || { echo "error: the tool was installed but cannot be found on this system" >&2; exit 127; }
    exec "$tool" --version
fi

if ! tool=$(find_tool); then
    install_tool fresh
    tool=$(find_tool) || {
        echo "error: the tool was installed but cannot be found" >&2
        echo "hint: add uv's tool folder to PATH with: uv tool update-shell" >&2
        exit 127
    }
fi
exec "$tool" "$@"
