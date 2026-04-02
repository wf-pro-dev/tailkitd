#!/bin/sh
# tailkitd install script
#
# Pipe install (recommended):
#   curl -fsSL https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh | sudo sh -s -- --auth-key tskey-auth-...
#
# Download and run:
#   curl -fsSL https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh -o install.sh
#   chmod +x install.sh && sudo ./install.sh --auth-key tskey-auth-...
#
# Flags (passed after -- when piping, or directly when running the file):
#   --auth-key <key>   Tailscale auth key (required, or set TS_AUTHKEY env var)
#   --hostname <name>  Override tailnet hostname (default: system hostname)
#   --nosystemd        Download the static no-cgo variant (no systemd journal)
#   --version  <ver>   Pin a specific release e.g. v0.1.4 (default: latest)

set -eu

REPO="wf-pro-dev/tailkitd"
VERSION=""
NOSYSTEMD=""
AUTH_KEY=""
HOSTNAME_OVERRIDE=""

# ── Helpers ───────────────────────────────────────────────────────────────────

info()  { echo "[tailkitd] $*"; }
fatal() { echo "[tailkitd] error: $*" >&2; exit 1; }
need()  { command -v "$1" >/dev/null 2>&1 || fatal "'$1' is required but not found"; }

# ---------- Detect if being piped (stdin is not a tty) ----------
# When piped, the user cannot interactively respond to prompts.
# We fail fast on missing flags rather than hanging.
PIPED=0
if [ ! -t 0 ]; then
  PIPED=1
fi

# ---------- Parse flags ----------
while [ $# -gt 0 ]; do
  case "$1" in
    --auth-key)  AUTH_KEY="$2";           shift 2 ;;
    --hostname)  HOSTNAME_OVERRIDE="$2";  shift 2 ;;
    --nosystemd) NOSYSTEMD=1;             shift   ;;
    --version)   VERSION="$2";            shift 2 ;;
    *) fatal "unknown flag: $1" ;;
  esac
done

# Auth key: flag > env var
if [ -z "$AUTH_KEY" ]; then
  AUTH_KEY="${TS_AUTHKEY:-}"
fi
if [ -z "$AUTH_KEY" ]; then
  echo "error: --auth-key or TS_AUTHKEY is required" >&2
  echo "  example: curl ... | sudo sh -s -- --auth-key tskey-auth-xxxx" >&2
  exit 1
fi

# ---------- Require root ----------
if [ "$(id -u)" -ne 0 ]; then
  fatal "install must be run as root (use sudo)"
fi

# ---------- Detect platform ----------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  linux|darwin) ;;
  *) fatal "unsupported OS: $OS" ;;
esac

case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) fatal "unsupported architecture: $ARCH" ;;
esac

# ---------- Resolve version ----------
if [ -z "$VERSION" ]; then
  # Follow the /releases/latest redirect and extract the tag from the URL.
  VERSION="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO}/releases/latest" \
    | sed 's|.*/tag/||' \
    | tr -d '[:space:]')"
fi

if [ -z "$VERSION" ]; then
  fatal "could not resolve latest version — check your internet connection"
fi

# Normalise: ensure VERSION has a leading v (e.g. 0.1.4 → v0.1.4).
case "$VERSION" in
  v*) ;;
  *)  VERSION="v${VERSION}" ;;
esac

# GoReleaser strips the leading v from {{ .Version }} in archive name templates.
# The archive on GitHub is named  tailkitd_0.1.4_linux_amd64.tar.gz
# but the tag (and download path) is v0.1.4.
VERSION_BARE="${VERSION#v}"

# ---------- Build asset name ----------
SUFFIX=""
if [ -n "$NOSYSTEMD" ]; then
  SUFFIX="_nosystemd"
fi

ARCHIVE="tailkitd_${VERSION_BARE}_${OS}_${ARCH}${SUFFIX}.tar.gz"
CHECKSUM_FILE="tailkitd_${VERSION_BARE}_checksums.txt"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

# ---------- Download ----------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

info "Downloading tailkitd ${VERSION} (${OS}/${ARCH}${SUFFIX})…"
echo "  ${BASE_URL}/${ARCHIVE}"

curl -fsSL "${BASE_URL}/${ARCHIVE}"       -o "${TMP}/${ARCHIVE}"
curl -fsSL "${BASE_URL}/${CHECKSUM_FILE}" -o "${TMP}/${CHECKSUM_FILE}"

# ---------- Verify checksum ----------
info "Verifying checksum…"
cd "$TMP"
grep "${ARCHIVE}" "${CHECKSUM_FILE}" | sha256sum -c -
cd - > /dev/null
echo "  OK"

# ---------- Extract ----------
tar -xzf "${TMP}/${ARCHIVE}" -C "$TMP"

BINARY_NAME="tailkitd"
if [ -n "$NOSYSTEMD" ]; then
  BINARY_NAME="tailkitd-nosystemd"
fi

chmod +x "${TMP}/${BINARY_NAME}"

# ---------- Hand off to the binary ----------
info "Running tailkitd install…"

INSTALL_ARGS="--auth-key ${AUTH_KEY}"
if [ -n "$HOSTNAME_OVERRIDE" ]; then
  INSTALL_ARGS="${INSTALL_ARGS} --hostname ${HOSTNAME_OVERRIDE}"
fi

# shellcheck disable=SC2086
exec "${TMP}/${BINARY_NAME}" install $INSTALL_ARGS
