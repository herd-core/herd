import asyncio
import os
import subprocess
import tempfile
import time
import socket
import pytest

# Paths to the Go source within the codebase
PROJ_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
HERD_CMD_DIR = os.path.join(PROJ_ROOT, "cmd", "herd")
HEALTHWORKER_DIR = os.path.join(PROJ_ROOT, "testdata", "healthworker")

def wait_for_socket(socket_path: str, timeout: float = 5.0) -> bool:
    """Block until the unix domain socket exists and accepts connections."""
    start = time.time()
    while time.time() - start < timeout:
        if os.path.exists(socket_path):
            try:
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
                    s.connect(socket_path)
                    return True
            except ConnectionRefusedError:
                pass
        time.sleep(0.1)
    return False

@pytest.fixture(scope="session", autouse=True)
def compiled_binaries():
    """Compiles the herd daemon and test worker once per test session."""
    temp_dir = tempfile.mkdtemp()
    herd_bin = os.path.join(temp_dir, "herd")
    worker_bin = os.path.join(temp_dir, "healthworker")
    
    print(f"\n[+] Compiling test binaries to {temp_dir}...")
    subprocess.run(["go", "build", "-o", herd_bin, "./cmd/herd"], cwd=PROJ_ROOT, check=True)
    subprocess.run(["go", "build", "-o", worker_bin, "./testdata/healthworker"], cwd=PROJ_ROOT, check=True)
    
    yield {"herd": herd_bin, "worker": worker_bin}
    
    # Cleanup binaries
    if os.path.exists(herd_bin): os.remove(herd_bin)
    if os.path.exists(worker_bin): os.remove(worker_bin)
    os.rmdir(temp_dir)

@pytest.fixture()
def herd_daemon(compiled_binaries):
    """
    Spawns an isolated herd daemon process attached to a dynamic unix socket.
    Tears it down gracefully after the test.
    """
    import uuid
    sock_path = f"/tmp/herd_e2e_{uuid.uuid4().hex[:8]}.sock"
    herd_bin = compiled_binaries["herd"]
    worker_bin = compiled_binaries["worker"]
    
    # Get a random free port for HTTP data plane
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(('127.0.0.1', 0))
    data_port = s.getsockname()[1]
    s.close()
    
    # Create configuration file dynamically
    cfg_path = f"/tmp/herd_cfg_{uuid.uuid4().hex[:8]}.yaml"
    with open(cfg_path, "w") as f:
        f.write(f'''
network:
  control_socket: "{sock_path}"
  data_bind: "127.0.0.1:{data_port}"
worker:
  command: ["{worker_bin}"]
  health_path: "/health"
resources:
  min_workers: 1
  max_workers: 5
  memory_limit_mb: 512
  cpu_limit_cores: 1.0
  pids_limit: 1024
  insecure_sandbox: true
  data_timeout: "5s"  # smaller timeout to avoid waiting 30 seconds during dev/CI
  heartbeat_grace: "5s" # aggressively short heartbeat grace for testing 
''')
    
    # Run daemon process
    print(f"\\n[+] Starting daemon at {sock_path} on data port {data_port}")
    proc = subprocess.Popen([herd_bin, "start", "--config", cfg_path], 
                            cwd=PROJ_ROOT, 
                            stdout=subprocess.PIPE, 
                            stderr=subprocess.PIPE)
                            
    # Wait for the gRPC socket to be listening
    if not wait_for_socket(sock_path):
        proc.kill()
        out, err = proc.communicate()
        raise RuntimeError(f"Daemon failed to start:\\nSTDOUT: {out.decode()}\\nSTDERR: {err.decode()}")

    yield f"unix://{sock_path}"

    # Teardown daemon
    proc.terminate()
    try:
        proc.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
    
    # Cleanup
    if os.path.exists(sock_path):
        os.remove(sock_path)
    if os.path.exists(cfg_path):
        os.remove(cfg_path)

@pytest.fixture()
def herd_daemon_websocket(compiled_binaries):
    """
    Spawns an isolated herd daemon specifically tuned for the WebSocket / Absolute TTL test.
    """
    import uuid
    sock_path = f"/tmp/herd_e2e_{uuid.uuid4().hex[:8]}.sock"
    herd_bin = compiled_binaries["herd"]
    worker_bin = compiled_binaries["worker"]
    
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(('127.0.0.1', 0))
    data_port = s.getsockname()[1]
    s.close()
    
    cfg_path = f"/tmp/herd_cfg_{uuid.uuid4().hex[:8]}.yaml"
    with open(cfg_path, "w") as f:
        f.write(f'''
network:
  control_socket: "{sock_path}"
  data_bind: "127.0.0.1:{data_port}"
worker:
  command: ["{worker_bin}"]
  health_path: "/health"
resources:
  min_workers: 1
  max_workers: 5
  memory_limit_mb: 512
  cpu_limit_cores: 1.0
  pids_limit: 1024
  insecure_sandbox: true
  data_timeout: "2s"      # very small data timeout
  heartbeat_grace: "5s"   # normal heartbeat grace
  absolute_ttl: "15s"     # The guillotine drops at exactly 15s
''')
    
    print(f"\\n[+] Starting WEBSOCKET test daemon at {sock_path} on data port {data_port}")
    proc = subprocess.Popen([herd_bin, "start", "--config", cfg_path], 
                            cwd=PROJ_ROOT, 
                            stdout=subprocess.PIPE, 
                            stderr=subprocess.PIPE)
                            
    if not wait_for_socket(sock_path):
        proc.kill()
        out, err = proc.communicate()
        raise RuntimeError(f"Daemon failed to start:\\nSTDOUT: {out.decode()}\\nSTDERR: {err.decode()}")

    yield {"remote": f"unix://{sock_path}", "data_port": data_port}

    proc.terminate()
    try:
        proc.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
    
    if os.path.exists(sock_path): os.remove(sock_path)
    if os.path.exists(cfg_path): os.remove(cfg_path)

@pytest.fixture()
def herd_daemon_stress(compiled_binaries):
    """
    Spawns an isolated herd daemon tuned for high concurrency.
    """
    import uuid
    sock_path = f"/tmp/herd_e2e_{uuid.uuid4().hex[:8]}.sock"
    herd_bin = compiled_binaries["herd"]
    worker_bin = compiled_binaries["worker"]
    
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(('127.0.0.1', 0))
    data_port = s.getsockname()[1]
    s.close()
    
    cfg_path = f"/tmp/herd_cfg_{uuid.uuid4().hex[:8]}.yaml"
    with open(cfg_path, "w") as f:
        f.write(f'''
network:
  control_socket: "{sock_path}"
  data_bind: "127.0.0.1:{data_port}"
worker:
  command: ["{worker_bin}"]
  health_path: "/health"
resources:
  min_workers: 5
  max_workers: 150
  memory_limit_mb: 64
  cpu_limit_cores: 0.3
  pids_limit: 1024
  insecure_sandbox: true
  data_timeout: "10s"
  heartbeat_grace: "10s"
''')
    
    print(f"\\n[+] Starting STRESS daemon at {sock_path} on data port {data_port}")
    
    log_file = open(f"/tmp/herd_stress_{data_port}.log", "w")
    proc = subprocess.Popen([herd_bin, "start", "--config", cfg_path], 
                            cwd=PROJ_ROOT, 
                            stdout=log_file, 
                            stderr=subprocess.STDOUT)
                            
    if not wait_for_socket(sock_path, timeout=10.0):
        proc.kill()
        raise RuntimeError(f"Daemon failed to start")

    yield f"unix://{sock_path}"

    proc.terminate()
    try:
        proc.wait(timeout=2.0)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
    
    if os.path.exists(sock_path): os.remove(sock_path)
    if os.path.exists(cfg_path): os.remove(cfg_path)
