#!/bin/bash
set -e

REPO="herd-core/herd"
VERSION="v0.5.4" # Matches internal/config.Version

# Detect Arch
ARCH=$(uname -m)
case $ARCH in
    x86_64) GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    arm64) GOARCH="arm64" ;;
    *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

# Download URL (matching GoReleaser 'binary' format)
URL="https://github.com/$REPO/releases/download/$VERSION/herd-linux-$GOARCH"

echo "🚀 Downloading herd $VERSION for $GOARCH..."
curl -sSL -o herd "$URL"
chmod +x herd

echo "📦 Installing herd to /usr/local/bin..."
sudo mv herd /usr/local/bin/herd

echo "✅ Herd installed successfully at /usr/local/bin/herd"
echo ""
echo "To complete the setup, run:"
echo "  sudo herd init"
