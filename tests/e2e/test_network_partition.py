import asyncio
import httpx
import pytest
from herd import AsyncClient

@pytest.mark.asyncio
async def test_silent_network_partition(herd_daemon):
    """
    Scenario 2: The Silent Network Partition
    Verifies that when the network lies (client drops without sending TCP FIN/RST):
    1. The HTTP Proxy and gRPC Server hold the connection open initially.
    2. Because PING messages stop, `LastControlHeartbeat` becomes stale.
    3. Exactly after `heartbeat_grace` (e.g. 5s configured in the daemon), the Reaper 
       recognizes the phantom client and ruthlessly executes the worker process.
    """
    
    # Track the session and proxy URL we acquire
    proxy_url = None
    session_id = None

    # Step 1: Establish connection normally
    client = AsyncClient(address=herd_daemon)
    
    try:
        # We manually acquire without using the `async with` context manager.
        # This allows us to brutally sever the underlying transport without triggering
        # the context manager's clean `session.close()` which sends an EOF.
        session_ctx = client.acquire(worker_type="healthworker", timeout=10)
        session = await session_ctx.__aenter__()
        proxy_url = session.proxy_url
        session_id = session.id
        
        assert proxy_url is not None, "Proxy URL must be provided by the session"
        headers = {"X-Session-ID": session_id}
        
        # Verify worker is up and running.
        async with httpx.AsyncClient() as http_client:
            health_res = await http_client.get(f"{proxy_url}/health", headers=headers)
            assert health_res.status_code == 200, "Worker is not healthy initially"

        print("\\n[+] Worker is healthy. Proceeding to simulate silent network partition...")
        
        # Step 2: Stop sending heartbeats without triggering a clean stream disconnect.
        # We simulate the "phantom" network drop by explicitly cancelling the background heartbeat task.
        # Since no EOF is sent, the Go daemon's gRPC stream stays open, but PINGs stop.
        session._heartbeat_task.cancel()
        
        print("[+] Cancelled heartbeat loop to simulate dropped network (no FIN/RST sent).")

    except Exception:
        # Ensure we don't leak resources if setup fails
        if 'session' in locals():
            await session.close()
        raise

    # Step 3: Wait out the Reaper loop. 
    # In our conftest, we set `heartbeat_grace: "5s"`. The Reaper runs on a 5-second interval.
    # Therefore, waiting 12 seconds ensures at least one sweep will trigger the execution.
    print("[+] Waiting 12 seconds for the Phantom Reaper guillotine to drop...")
    await asyncio.sleep(12.0)
    
    # Step 4: Verify the worker process was terminated by the Reaper.
    print("[+] Verifying zombie worker was assassinated...")
    headers = {"X-Session-ID": session_id}
    
    async with httpx.AsyncClient() as http_client:
        try:
            # The daemon should have killed the process. Our HTTP request should return 404
            # indicating that the session no longer exists in the daemon's registry.
            resp = await http_client.get(f"{proxy_url}/health", headers=headers, timeout=2.0)
            if resp.status_code == 404 and "session not found" in resp.text:
                print("[+] Verified target session was purged (404 Not Found) by the Reaper.")
            else:
                pytest.fail(f"The healthworker process should be DEAD, but it responded. Reaper failed! Status: {resp.status_code}, Body: {resp.text}")
        except (httpx.ConnectError, httpx.ReadTimeout, httpx.RemoteProtocolError, httpx.HTTPStatusError):
            print("[+] Verified target worker is unresponsive (purged).")
            
    # Explicitly clear the queue generator to avoid unclosed event loop warnings
    try:
        if 'session_ctx' in locals():
            await session_ctx.__aexit__(None, None, None)
    except Exception:
        pass
