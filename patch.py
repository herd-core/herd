import re

with open('firecracker_factory.go', 'r') as f:
    code = f.read()

code = code.replace(
'''        // Ensure old socket is removed
        os.Remove(socketPath)

        // In a real implementation we would interact via the API socket
        // but for now we just start the process and configure via CLI

        // Create a minimal config json
        configPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.json", workerID))''',
'''        // Ensure old socket is removed
        os.Remove(socketPath)

        rootfsPath, err := f.Storage.PullAndSnapshot(ctx, "docker.io/library/ubuntu:latest", workerID)
        if err != nil {
                return nil, fmt.Errorf("failed to pull and snapshot rootfs: %w", err)
        }

        // Create a minimal config json
        configPath := filepath.Join(f.SocketPathDir, fmt.Sprintf("%s.json", workerID))'''
)

with open('firecracker_factory.go', 'w') as f:
    f.write(code)
