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
	*) echo "perch: NOTE: ${INSTALL_DIR} is not on your PATH, add it to your shell profile" ;;
esac
echo "perch: run 'perch -server <windows-host>:2222' to connect"
