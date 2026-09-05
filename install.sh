#!/bin/sh
# rkdash installer.
#
#   Install or update:  curl -fsSL <raw-url>/install.sh | sh
#   Uninstall:          curl -fsSL <raw-url>/install.sh | sh -s -- --uninstall
#
# Environment overrides:
#   RKDASH_VERSION   install a specific tag (e.g. v1.1.0) instead of the latest
#   RKDASH_PREFIX    install prefix (default /usr/local)
#   RKDASH_PURGE     with --uninstall, also delete the config file
#
# POSIX sh, no bashisms: this has to run under dash on a stock Debian image.

set -eu

REPO="anand34577/rkdash"
ASSET="rkdash-linux-arm64"
BIN_NAME="rkdash"
PREFIX="${RKDASH_PREFIX:-/usr/local}"
BIN_DIR="$PREFIX/bin"
TARGET="$BIN_DIR/$BIN_NAME"

# Colour only when stdout is a terminal. Piped into sh, stdout usually still is,
# but a log capture shouldn't get escape codes.
if [ -t 1 ]; then
    C_RED=$(printf '\033[31m'); C_GRN=$(printf '\033[32m')
    C_YEL=$(printf '\033[33m'); C_DIM=$(printf '\033[2m'); C_OFF=$(printf '\033[0m')
else
    C_RED=''; C_GRN=''; C_YEL=''; C_DIM=''; C_OFF=''
fi

info() { printf '%s==>%s %s\n' "$C_GRN" "$C_OFF" "$*"; }
warn() { printf '%s==>%s %s\n' "$C_YEL" "$C_OFF" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$C_RED" "$C_OFF" "$*" >&2; exit 1; }

# ---------------------------------------------------------------- privileges

# Elevate per-command rather than re-executing the whole script under sudo:
# re-exec would need the script on disk, and it arrives on stdin from a pipe.
# A wrapper function rather than a $SUDO variable, so the command is never
# subject to word splitting.
if [ "$(id -u)" -eq 0 ]; then
    as_root() { "$@"; }
elif command -v sudo >/dev/null 2>&1; then
    as_root() { sudo "$@"; }
    info "Not running as root; will use sudo for $BIN_DIR"
else
    die "Need root to write $BIN_DIR, and sudo is not installed. Re-run as root."
fi

# ---------------------------------------------------------------- platform

check_platform() {
    os=$(uname -s)
    [ "$os" = "Linux" ] || die "rkdash is Linux-only (this is $os)."

    arch=$(uname -m)
    case "$arch" in
        aarch64|arm64) ;;
        *) die "rkdash ships linux/arm64 only; this machine is $arch.
       Build from source instead: https://github.com/$REPO#building" ;;
    esac
}

# ---------------------------------------------------------------- fetching

# One of curl or wget; almost every image has at least one.
if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
else
    die "Need curl or wget to download rkdash."
fi

# The binary is about to run as root, so a corrupted or tampered download must
# not be installed. Abort rather than silently skipping the check.
verify_checksum() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum -c "$ASSET.sha256" >/dev/null 2>&1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 -c "$ASSET.sha256" >/dev/null 2>&1
    else
        die "No sha256sum or shasum available to verify the download.
       Install coreutils, or fetch the release manually if you accept the risk."
    fi
}

# ---------------------------------------------------------------- install

do_install() {
    check_platform

    if [ -n "${RKDASH_VERSION:-}" ]; then
        version="$RKDASH_VERSION"
        base="https://github.com/$REPO/releases/download/$version"
        info "Installing rkdash $version"
    else
        base="https://github.com/$REPO/releases/latest/download"
        info "Installing the latest rkdash release"
    fi

    previous=''
    if [ -x "$TARGET" ]; then
        previous=$("$TARGET" --version 2>/dev/null || echo "unknown")
        info "Found an existing install ($previous) at $TARGET"
    fi

    tmp=$(mktemp -d)
    # Clean up the download on any exit path, including a failed checksum.
    trap 'rm -rf "$tmp"' EXIT INT TERM
    cd "$tmp" || die "Cannot enter temp directory $tmp"

    info "Downloading $ASSET"
    fetch "$base/$ASSET" "$ASSET" \
        || die "Download failed. Check the release exists: https://github.com/$REPO/releases"
    fetch "$base/$ASSET.sha256" "$ASSET.sha256" \
        || die "Could not download the checksum file; refusing to install unverified."

    info "Verifying checksum"
    verify_checksum || die "Checksum mismatch — the download is corrupt or tampered with. Nothing was installed."

    chmod 755 "$ASSET"
    as_root mkdir -p "$BIN_DIR"
    # install(1) replaces the file atomically, so a running rkdash isn't
    # corrupted mid-write and there's no window where the binary is missing.
    if command -v install >/dev/null 2>&1; then
        as_root install -m 755 "$ASSET" "$TARGET"
    else
        as_root cp "$ASSET" "$TARGET.new"
        as_root chmod 755 "$TARGET.new"
        as_root mv "$TARGET.new" "$TARGET"
    fi

    installed=$("$TARGET" --version 2>/dev/null || echo "unknown")
    if [ -n "$previous" ] && [ "$previous" = "$installed" ]; then
        info "Already up to date ($installed)"
    elif [ -n "$previous" ]; then
        info "Updated $previous -> $installed"
    else
        info "Installed rkdash $installed to $TARGET"
    fi

    case ":$PATH:" in
        *":$BIN_DIR:"*) ;;
        *) warn "$BIN_DIR is not on your PATH; run it as $TARGET" ;;
    esac

    printf '\n  %sRun it:%s sudo %s\n' "$C_GRN" "$C_OFF" "$BIN_NAME"
    printf '  %sroot is required to read the GPU/NPU debugfs nodes.%s\n\n' "$C_DIM" "$C_OFF"
}

# ---------------------------------------------------------------- uninstall

do_uninstall() {
    removed=0

    if [ -e "$TARGET" ]; then
        info "Removing $TARGET"
        as_root rm -f "$TARGET"
        removed=1
    else
        warn "No binary at $TARGET"
    fi

    # The config is the user's own work, so it survives by default; RKDASH_PURGE
    # opts into deleting it. Never prompt: stdin is the script itself when piped.
    conf="${XDG_CONFIG_HOME:-$HOME/.config}/rkdash"
    if [ -d "$conf" ]; then
        if [ -n "${RKDASH_PURGE:-}" ]; then
            info "Removing config directory $conf"
            rm -rf "$conf"
        else
            info "Keeping config at $conf (set RKDASH_PURGE=1 to remove it)"
        fi
    fi

    if [ "$removed" -eq 1 ]; then
        info "rkdash uninstalled"
    else
        info "Nothing to uninstall"
    fi
}

# ---------------------------------------------------------------- entry

usage() {
    cat <<EOF
rkdash installer

Usage:
  install.sh                 install or update to the latest release
  install.sh --uninstall     remove the binary (keeps the config)
  install.sh --version TAG   install a specific release, e.g. v1.1.0
  install.sh --help          this message

Environment:
  RKDASH_VERSION   same as --version
  RKDASH_PREFIX    install prefix (default /usr/local)
  RKDASH_PURGE     with --uninstall, also delete the config file
EOF
}

action=install
while [ $# -gt 0 ]; do
    case "$1" in
        --uninstall|--remove|uninstall) action=uninstall ;;
        --update|--install|install|update) action=install ;;
        --version) shift; [ $# -gt 0 ] || die "--version needs a tag"; RKDASH_VERSION="$1" ;;
        --help|-h) usage; exit 0 ;;
        *) die "Unknown option: $1 (try --help)" ;;
    esac
    shift
done

case "$action" in
    install)   do_install ;;
    uninstall) do_uninstall ;;
esac
