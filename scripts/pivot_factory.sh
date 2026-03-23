#!/bin/bash
set -e

rm -f factory.go

echo "Moving process_worker_factory.go back to root..."
mv internal/core/process_worker_factory.go .
mv internal/core/factory_cgroup_test.go .

sed -i '' 's/package core/package herd/g' process_worker_factory.go
sed -i '' 's/package core/package herd/g' factory_cgroup_test.go

# Revert herd.Worker back to Worker
sed -i '' 's/herd.Worker/Worker/g' process_worker_factory.go

# Capitalize Sandbox items in internal/core/sandbox*
echo "Capitalizing Sandbox hooks..."
sed -i '' 's/type sandboxHandle/type SandboxHandle/g' internal/core/sandbox*
sed -i '' 's/sandboxHandle/SandboxHandle/g' internal/core/sandbox*

sed -i '' 's/type sandboxConfig/type SandboxConfig/g' internal/core/sandbox*
sed -i '' 's/sandboxConfig/SandboxConfig/g' internal/core/sandbox*

sed -i '' 's/func applySandboxFlags/func ApplySandboxFlags/g' internal/core/sandbox*
sed -i '' 's/applySandboxFlags/ApplySandboxFlags/g' internal/core/sandbox*

sed -i '' 's/func defaultNamespaceCloneFlags/func DefaultNamespaceCloneFlags/g' internal/core/sandbox*
sed -i '' 's/defaultNamespaceCloneFlags/DefaultNamespaceCloneFlags/g' internal/core/sandbox*

# Update process_worker_factory.go to use core. prefixed Sandbox methods
# Add import
sed -i '' 's|"sync/atomic"|"sync/atomic"\n\t"github.com/herd-core/herd/internal/core"|g' process_worker_factory.go
# Wait, previous fix added "github.com/herd-core/herd" to process_worker_factory.go, remove it or replace it!
sed -i '' 's|"github.com/herd-core/herd"|"github.com/herd-core/herd/internal/core"|g' process_worker_factory.go

echo "✅ Pivot completed. Checking build..."
go build ./...
