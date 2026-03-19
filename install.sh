#!/bin/sh
# tailkitd install script
# Usage:
#   curl -fsSL https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh | sudo sh -s -- --auth-key tskey-auth-...
#   wget -qO- https://github.com/wf-pro-dev/tailkitd/releases/latest/download/install.sh | sudo sh -s -- --auth-key tskey-auth-...
#
# Optional flags (passed after --):
#   --auth-key <key>     Tailscale auth key (required)
#   --hostname  <name>   Override tailnet hostname (default: system hostname)
#   --nosystemd          Download the static nosystemd variant
#   --version   <ver>    Pin a specific release version (default: latest)

set -eu

REPO="wf-pro-dev/tailkitd"
VERSION="latest"
NOSYSTEMD=""
AUTH_KEY=""
HOSTNAME_OVERRIDE=""

# ---------- Parse flags ----------
while [ $# -gt 0 ]; do
  case "$1" in
    --auth-key)  AUTH_KEY="$2";           shift 2 ;;
    --hostname)  HOSTNAME_OVERRIDE="$2";  shift 2 ;;
    --nosystemd) NOSYSTEMD="_nosystemd";  shift   ;;
    --version)   VERSION="$2";            shift 2 ;;
    *) echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
done

if [ -z "$AUTH_KEY" ]; then
  # Fall back to environment variable.
  AUTH_KEY="${TS_AUTHKEY:-}"
fi
if [ -z "$AUTH_KEY" ]; then
  echo "error: --auth-key or TS_AUTHKEY is required" >&2
  exit 1
fi

# ---------- Detect platform ----------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  armv7l)  ARCH="armv7" ;;
  *)
    echo "error: unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

if [ "$OS" != "linux" ]; then
  echo "error: tailkitd only runs on Linux (got: $OS)" >&2
  exit 1
fi

# ---------- Resolve download URL ----------
if [ "$VERSION" = "latest" ]; then
  # Resolve the actual latest version tag via GitHub redirect.
  VERSION="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO}/releases/latest" \
    | sed 's|.*/tag/||')"
fi

ARCHIVE="tailkitd_${VERSION}_${OS}_${ARCH}${NOSYSTEMD}.tar.gz"
CHECKSUM_FILE="tailkitd_${VERSION}_checksums.txt"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

# ---------- Download ----------
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "Downloading tailkitd ${VERSION} (${OS}/${ARCH}${NOSYSTEMD})…"
curl -fsSL "${BASE_URL}/${ARCHIVE}"      -o "${TMP}/${ARCHIVE}"
curl -fsSL "${BASE_URL}/${CHECKSUM_FILE}" -o "${TMP}/${CHECKSUM_FILE}"

# ---------- Verify checksum ----------
echo "Verifying checksum…"
cd "$TMP"
grep "${ARCHIVE}" "${CHECKSUM_FILE}" | sha256sum -c -
cd - > /dev/null

# ---------- Extract ----------
tar -xzf "${TMP}/${ARCHIVE}" -C "$TMP"

# The binary inside the archive is always named "tailkitd" or "tailkitd-nosystemd".
BINARY_NAME="tailkitd"
if [ -n "$NOSYSTEMD" ]; then
  BINARY_NAME="tailkitd-nosystemd"
fi

chmod +x "${TMP}/${BINARY_NAME}"

# ---------- Run install ----------
echo "Running tailkitd install…"

INSTALL_ARGS="--auth-key ${AUTH_KEY}"
if [ -n "$HOSTNAME_OVERRIDE" ]; then
  INSTALL_ARGS="${INSTALL_ARGS} --hostname ${HOSTNAME_OVERRIDE}"
fi

# shellcheck disable=SC2086
exec "${TMP}/${BINARY_NAME}" install $INSTALL_ARGS
