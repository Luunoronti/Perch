#!/bin/sh
# Installs the perch client (Linux) from the latest GitHub release.
# Usage: curl -sSL https://raw.githubusercontent.com/Luunoronti/Perch/main/install.sh | sh
set -eu

REPO="${PERCH_REPO:-Luunoronti/Perch}" # override with PERCH_REPO=owner/repo if you fork
INSTALL_DIR="${PERCH_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -m)" in
	x86_64 | amd64) ASSET="perch-amd64" ;;
	i386 | i486 | i586 | i686) ASSET="perch-386" ;;
	*)
		echo "perch: unsupported architecture $(uname -m) (only linux/amd64 and linux/386 are built)" >&2
		exit 1
		;;
esac

if [ "$(uname -s)" != "Linux" ]; then
	echo "perch: this install script only supports Linux (the client's only supported OS)" >&2
	exit 1
fi

BASE_URL="https://github.com/${REPO}/releases/latest/download"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

echo "perch: downloading ${ASSET} from ${REPO} (latest release)..."
curl -sSL -o "$TMP/perch" "${BASE_URL}/${ASSET}"
curl -sSL -o "$TMP/checksums.txt" "${BASE_URL}/checksums.txt"

echo "perch: verifying checksum..."
EXPECTED="$(grep " ${ASSET}\$" "$TMP/checksums.txt" | cut -d' ' -f1)"
if [ -z "$EXPECTED" ]; then
	echo "perch: could not find checksum for ${ASSET} in checksums.txt" >&2
	exit 1
fi
ACTUAL="$(sha256sum "$TMP/perch" | cut -d' ' -f1)"
if [ "$EXPECTED" != "$ACTUAL" ]; then
	echo "perch: checksum mismatch! expected $EXPECTED, got $ACTUAL" >&2
	exit 1
fi

mkdir -p "$INSTALL_DIR"
install -m 0755 "$TMP/perch" "$INSTALL_DIR/perch"

echo "perch: installed to ${INSTALL_DIR}/perch"

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*)
		# Not on PATH right now (this may just be a non-interactive `sh` losing
		# it, or a real gap) -- add it to the user's shell rc so future shells
		# pick it up, unless it's already there.
		RC=""
		case "${SHELL:-}" in
			*/zsh) RC="$HOME/.zshrc" ;;
			*/bash) RC="$HOME/.bashrc" ;;
			*) RC="$HOME/.profile" ;;
		esac
		LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
		if [ -f "$RC" ] && grep -qF "$INSTALL_DIR" "$RC" 2>/dev/null; then
			echo "perch: ${INSTALL_DIR} already referenced in ${RC}, restart your shell to pick it up"
		else
			printf '\n# added by perch install.sh\n%s\n' "$LINE" >>"$RC"
			echo "perch: added ${INSTALL_DIR} to PATH in ${RC} -- restart your shell (or run: source ${RC})"
		fi
		;;
esac
echo "perch: run 'perch -server <windows-host>:2222' to connect"
