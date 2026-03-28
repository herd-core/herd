#!/usr/bin/env bash
set -e

# Compile the guest agent as a static binary
echo "Compiling herd-guest-agent..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o herd-guest-agent ./cmd/herd-guest-agent

# Create a temporary staging directory
BUILD_DIR=$(mktemp -d -t herd-initrd.XXXXXX)
trap "rm -rf $BUILD_DIR" EXIT

# Copy the binary to the standard Linux init path
# Note: When the kernel boots from initrd, it executes /init as PID 1
cp herd-guest-agent "$BUILD_DIR/init"
chmod +x "$BUILD_DIR/init"

echo "Packaging initrd archive..."
cd "$BUILD_DIR"
find . -print0 | cpio --null -o -V -H newc > "$OLDPWD/herd-guest-agent.initrd"
cd "$OLDPWD"

echo "Successfully built herd-guest-agent.initrd!"
