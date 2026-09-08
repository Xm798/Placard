#!/bin/sh
# Install script for the Placard CLI.
#
# Usage:
#   curl -fsSL https://<your placard server>/install.sh | sh
#
# The binaries come from the project's GitHub releases, which carry two tag
# namespaces: vX.Y.Z is the SERVER and cli/vX.Y.Z is the CLI. Everything below
# looks at cli/v only.
#
# Environment:
#   PLACARD_BIN_DIR        install directory (default: $HOME/.local/bin)
#   PLACARD_CLI_VERSION    pin a version instead of taking the newest release
#   PLACARD_SKIP_CHECKSUM  set to 1 to skip sha256 verification (prints a warning)
#
# Security note, stated plainly: checksums.txt is an asset of the same release
# as the binaries. Whoever can rewrite the binary can rewrite the checksum too,
# so the sha256 check defends against transport corruption and against a
# silently skipped verification — NOT against tampering by someone holding
# write access to the repository's releases. The real trust roots here are that
# write access and TLS.

set -eu

# --- Configuration ---

BIN_DIR="${PLACARD_BIN_DIR:-$HOME/.local/bin}"
REPO="Xm798/placard"
API_URL="https://api.github.com/repos/${REPO}/releases?per_page=100"
# Server (vX.Y.Z) and CLI (cli/vX.Y.Z) releases share one list, so a long run of
# server releases can push the newest CLI release onto a later page.
MAX_PAGES=5
# A Placard server rewrites this line when it serves /install.sh, pinning the
# CLI release it resolved. Left empty (installing straight from GitHub) the
# newest release wins. The server-side rewrite matches this line exactly.
DEFAULT_CLI_VERSION=""
TMPFILE=""

cleanup() {
  if [ -n "$TMPFILE" ] && [ -f "$TMPFILE" ]; then
    rm -f "$TMPFILE"
  fi
}
trap cleanup EXIT

# --- Color output (TTY detection) ---

if [ -t 2 ]; then
  RED='\033[0;31m'
  YELLOW='\033[0;33m'
  GREEN='\033[0;32m'
  CYAN='\033[0;36m'
  BOLD='\033[1m'
  DIM='\033[2m'
  RESET='\033[0m'
else
  RED=''
  YELLOW=''
  GREEN=''
  CYAN=''
  BOLD=''
  DIM=''
  RESET=''
fi

error() {
  printf "${RED}error${RESET}: %s\n" "$*" >&2
  exit 1
}

warn() {
  printf "${YELLOW}warning${RESET}: %s\n" "$*" >&2
}

info() {
  printf "${DIM}%s${RESET}\n" "$*" >&2
}

info_bold() {
  printf "${BOLD}%s${RESET}\n" "$*" >&2
}

success() {
  printf "${GREEN}%s${RESET}\n" "$*" >&2
}

# --- Dependency checks ---

check_deps() {
  if ! command -v curl >/dev/null 2>&1; then
    error "curl is required but was not found. Install curl and try again."
  fi
}

# --- Platform detection ---

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux)  echo "linux" ;;
    MINGW*|MSYS*|CYGWIN*)
      error "On Windows use install.ps1 instead:
  irm https://<your placard server>/install.ps1 | iex" ;;
    *)
      error "Unsupported operating system: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64)  echo "x64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      error "Unsupported architecture: $(uname -m)" ;;
  esac
}

# --- SHA256 helper ---

# macOS ships shasum, not sha256sum. Both paths are required.
compute_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    error "No SHA256 tool found (need sha256sum or shasum)."
  fi
}

# --- Release resolution ---

# Every URL below is read out of the release list rather than assembled here:
# the CLI's tags contain a slash (cli/v1.2.3), and how that is spelled inside a
# download URL is GitHub's business, not this script's.
fetch_page() {
  curl -fsSL -H 'Accept: application/vnd.github+json' "${API_URL}&page=$1" \
    || error "Failed to fetch ${API_URL}&page=$1"
}

# json_strings puts every JSON string on its own line, which is what lets the
# release list be read with grep alone — no awk, no jq, nothing a minimal
# system might not have.
json_strings() {
  printf '%s' "$1" | tr '"' '\n'
}

# get_version prints the version to install, or nothing when the fetched pages
# hold no CLI release yet: the pinned version, or the newest stable cli/v one.
# GitHub lists releases newest first and hides drafts from anonymous callers,
# and a version made only of digits and dots is by definition not a prerelease
# (those carry a hyphen: 2.0.0-rc.1) — so the first match is the newest stable
# CLI release.
get_version() {
  if [ -n "${PLACARD_CLI_VERSION:-}" ]; then
    echo "$PLACARD_CLI_VERSION"
    return
  fi
  if [ -n "$DEFAULT_CLI_VERSION" ]; then
    echo "$DEFAULT_CLI_VERSION"
    return
  fi
  json_strings "$1" | grep -E '^cli/v[0-9]+(\.[0-9]+)*$' | head -n 1 | sed -e 's|^cli/v||'
}

# asset_url reads back the download URL GitHub reported for one asset. The tag
# is part of that URL path, which is what keeps checksums.txt — named the same
# in every release — from being taken out of a neighbouring one.
asset_url() {
  json_strings "$1" | grep "^https://.*/download/cli/v$2/$3\$" | head -n 1
}

# --- Checksum verification (fail-closed) ---

verify_checksum() {
  _file="$1"
  _binary_name="$2"
  _checksums_url="$3"

  if [ "${PLACARD_SKIP_CHECKSUM:-}" = "1" ]; then
    warn "PLACARD_SKIP_CHECKSUM=1 — sha256 verification DISABLED for this install."
    return 0
  fi

  if [ -z "$_checksums_url" ]; then
    error "The release publishes no checksums.txt — installation aborted.
Set PLACARD_SKIP_CHECKSUM=1 to install without verification."
  fi

  info "Verifying checksum..."
  _checksums=$(curl -fsSL "$_checksums_url") \
    || error "Could not fetch ${_checksums_url} — installation aborted.
Set PLACARD_SKIP_CHECKSUM=1 to install without verification."
  if [ -z "$_checksums" ]; then
    error "Empty checksums.txt at ${_checksums_url} — installation aborted."
  fi

  # goreleaser writes `<sha256>  <name>` with TWO spaces. Anchoring on the two
  # spaces plus end-of-line is what keeps this from matching a different
  # artifact whose name merely ends with the same suffix. If the checksum file
  # format ever changes, this grep stops matching and the install FAILS — that
  # is intentional; it must never degrade into "skip verification".
  _expected=$(printf '%s\n' "$_checksums" | grep "  ${_binary_name}$" | cut -d' ' -f1)
  if [ -z "$_expected" ]; then
    error "No checksum entry for ${_binary_name} in checksums.txt — installation aborted."
  fi

  _actual=$(compute_sha256 "$_file")
  if [ "$_actual" != "$_expected" ]; then
    error "SHA256 mismatch — installation aborted.
  Expected: ${_expected}
  Got:      ${_actual}"
  fi
  info "Checksum verified."
}

# --- Main install logic ---

install_placard() {
  os=$(detect_os)
  arch=$(detect_arch)

  # Pages are concatenated rather than parsed as one document — json_strings
  # reads lines, not structure — and fetching stops as soon as the release and
  # the asset for this platform are both in hand.
  info "Fetching release list..."
  releases=""
  version=""
  binary_url=""
  page=1
  while [ "$page" -le "$MAX_PAGES" ]; do
    releases="${releases}$(fetch_page "$page")"
    version=$(get_version "$releases")
    if [ -n "$version" ]; then
      binary_name="placard-${version}-${os}-${arch}"
      binary_url=$(asset_url "$releases" "$version" "$binary_name")
      if [ -n "$binary_url" ]; then
        break
      fi
    fi
    page=$((page + 1))
  done

  if [ -z "$version" ]; then
    error "No cli/v* release found at ${API_URL}"
  fi
  if [ -z "$binary_url" ]; then
    error "Release cli/v${version} publishes no ${binary_name}. Either it does not exist or this platform is not published."
  fi
  checksums_url=$(asset_url "$releases" "$version" "checksums.txt")

  printf "${BOLD}Installing placard ${CYAN}%s${RESET}${BOLD} (%s-%s)...${RESET}\n" \
    "$version" "$os" "$arch" >&2
  echo "" >&2

  # Download to a temp file first: a partial transfer must never overwrite a
  # working installation.
  TMPFILE=$(mktemp)
  info "Downloading ${binary_url}..."
  curl -fsSL "$binary_url" -o "$TMPFILE" \
    || error "Failed to download ${binary_url}"

  verify_checksum "$TMPFILE" "$binary_name" "$checksums_url"

  if [ ! -d "$BIN_DIR" ]; then
    mkdir -p "$BIN_DIR" || error "Failed to create directory: ${BIN_DIR}"
  fi

  # No sudo anywhere: this installs into a user-owned directory by design.
  mv "$TMPFILE" "${BIN_DIR}/placard" \
    || error "Failed to install binary to ${BIN_DIR}/placard"
  TMPFILE=""
  chmod +x "${BIN_DIR}/placard" || error "Failed to mark binary executable"

  if "${BIN_DIR}/placard" --version >/dev/null 2>&1; then
    echo "" >&2
    success "placard ${version} installed successfully!"
    echo "" >&2
    "${BIN_DIR}/placard" --version >&2
  else
    error "Verification failed: ${BIN_DIR}/placard did not run. The binary may be corrupted."
  fi
}

# --- PATH instructions ---

print_instructions() {
  shell_name=$(basename "${SHELL:-}")

  case "$shell_name" in
    bash) shell_rc="$HOME/.bashrc" ;;
    zsh)  shell_rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
    fish) shell_rc="$HOME/.config/fish/config.fish" ;;
    *)    shell_rc="your shell configuration file" ;;
  esac

  echo "" >&2

  case ":$PATH:" in
    *":$BIN_DIR:"*)
      info_bold "placard is ready to use!"
      printf '  %splacard --help%s\n' "$CYAN" "$RESET" >&2
      ;;
    *)
      info_bold "Add ${BIN_DIR} to your PATH to get started:"
      echo "" >&2
      if [ "$shell_name" = "fish" ]; then
        printf "  ${CYAN}set -Ua fish_user_paths \"%s\"${RESET}\n" "$BIN_DIR" >&2
      else
        printf "  ${CYAN}export PATH=\"%s:\$PATH\"${RESET}\n" "$BIN_DIR" >&2
      fi
      echo "" >&2
      info "Add that line to ${shell_rc} to make it permanent."
      echo "" >&2
      info_bold "Then run:"
      printf '  %splacard --help%s\n' "$CYAN" "$RESET" >&2
      ;;
  esac
}

main() {
  check_deps
  install_placard
  print_instructions
}

main
