#!/usr/bin/env sh
set -eu

REPO="${FIXORA_CLI_REPO:-baka126/fixora-cli}"
BINARY="${FIXORA_CLI_BINARY:-kubectl-fixora}"
REQUESTED_INSTALL_DIR="${INSTALL_DIR:-}"
VERSION="${VERSION:-latest}"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  linux|darwin) ;;
  *) echo "unsupported OS: $OS" >&2; exit 1 ;;
esac

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi

if ! command -v tar >/dev/null 2>&1; then
  echo "tar is required" >&2
  exit 1
fi

path_has_dir() {
  case ":${PATH:-}:" in
    *":$1:"*) return 0 ;;
    *) return 1 ;;
  esac
}

resolve_install_dir() {
  if [ -n "$REQUESTED_INSTALL_DIR" ]; then
    printf '%s\n' "$REQUESTED_INSTALL_DIR"
    return
  fi

  for dir in /usr/local/bin /opt/homebrew/bin "$HOME/.local/bin"; do
    if [ -d "$dir" ] && [ -w "$dir" ] && path_has_dir "$dir"; then
      printf '%s\n' "$dir"
      return
    fi
  done

  for dir in /usr/local/bin /opt/homebrew/bin; do
    if path_has_dir "$dir"; then
      printf '%s\n' "$dir"
      return
    fi
  done

  printf '%s\n' /usr/local/bin
}

install_binary() {
  src="$1"
  dest_dir="$2"
  dest="${dest_dir}/${BINARY}"

  if [ ! -d "$dest_dir" ]; then
    mkdir -p "$dest_dir" 2>/dev/null || true
  fi

  if [ -d "$dest_dir" ] && [ -w "$dest_dir" ]; then
    install -m 0755 "$src" "$dest"
    return
  fi

  if command -v sudo >/dev/null 2>&1; then
    echo "install directory ${dest_dir} is not writable; requesting sudo" >&2
    sudo mkdir -p "$dest_dir"
    sudo install -m 0755 "$src" "$dest"
    return
  fi

  echo "install directory ${dest_dir} is not writable and sudo was not found" >&2
  echo "retry with: curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | INSTALL_DIR=\$HOME/.local/bin sh" >&2
  exit 1
}

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)"
fi

if [ -z "$VERSION" ]; then
  echo "could not resolve release version" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

ARCHIVE="${BINARY}_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

curl -fsSL "${BASE_URL}/${ARCHIVE}" -o "${TMP_DIR}/${ARCHIVE}"
curl -fsSL "${BASE_URL}/checksums.txt" -o "${TMP_DIR}/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$TMP_DIR" && grep " ${ARCHIVE}$" checksums.txt | sha256sum -c -)
elif command -v shasum >/dev/null 2>&1; then
  expected="$(grep " ${ARCHIVE}$" "${TMP_DIR}/checksums.txt" | awk '{print $1}')"
  actual="$(shasum -a 256 "${TMP_DIR}/${ARCHIVE}" | awk '{print $1}')"
  [ "$expected" = "$actual" ] || { echo "checksum verification failed" >&2; exit 1; }
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "$TMP_DIR"
INSTALL_DIR="$(resolve_install_dir)"
install_binary "${TMP_DIR}/${BINARY}" "$INSTALL_DIR"

echo "installed ${BINARY} ${VERSION} to ${INSTALL_DIR}/${BINARY}"
if ! path_has_dir "$INSTALL_DIR"; then
  echo "warning: ${INSTALL_DIR} is not on PATH; kubectl will not find the plugin until PATH includes it" >&2
fi
echo "verify with: kubectl fixora version"
