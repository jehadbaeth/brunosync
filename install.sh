#!/bin/sh
# Installs the brunosync binary for this machine from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/jehadbaeth/brunosync/main/install.sh | sh
#
# Environment:
#   BRUNOSYNC_VERSION      tag to install, e.g. v0.1.0 (default: latest release)
#   BRUNOSYNC_INSTALL_DIR  target directory (default: $HOME/.local/bin)
set -eu

repo="jehadbaeth/brunosync"
version="${BRUNOSYNC_VERSION:-latest}"
dir="${BRUNOSYNC_INSTALL_DIR:-$HOME/.local/bin}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	darwin | linux) ;;
	*) echo "unsupported OS: $os (on Windows download brunosync-windows-amd64.exe from the releases page)" >&2; exit 1 ;;
esac
arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

asset="brunosync-$os-$arch"
if [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	base="https://github.com/$repo/releases/download/$version"
fi

tmp=$(mktemp -d)
trap 'rm -f "$tmp/$asset" "$tmp/SHA256SUMS"; rmdir "$tmp" 2>/dev/null || true' EXIT

echo "downloading $asset ($version)"
curl -fsSL -o "$tmp/$asset" "$base/$asset"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"

expected=$(grep " $asset\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$asset" | cut -d' ' -f1)
else
	actual=$(shasum -a 256 "$tmp/$asset" | cut -d' ' -f1)
fi
if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
	echo "checksum mismatch for $asset" >&2
	exit 1
fi

mkdir -p "$dir"
chmod +x "$tmp/$asset"
mv "$tmp/$asset" "$dir/brunosync"
echo "installed $("$dir/brunosync" version) to $dir/brunosync"

case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "note: $dir is not on your PATH. Add this to your shell profile:  export PATH=\"$dir:\$PATH\"" ;;
esac
