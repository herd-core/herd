#!/bin/bash
# scripts/gen_proto.sh - Helper to compile gRPC stubs inside the workspace.

set -e

echo "Compiling Protocol Buffers..."

protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       internal/proto/v1/herd.proto

echo "✅ Generation complete."
