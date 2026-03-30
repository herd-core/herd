#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# Builds the static herd-guest-agent binary at the repo root. The daemon copies
# this into each containerd snapshot before Firecracker boots (no initrd).

echo "Compiling herd-guest-agent..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o herd-guest-agent ./cmd/herd-guest-agent

echo "Built $REPO_ROOT/herd-guest-agent (injected into rootfs at spawn; initrd not used)."
