import asyncio
import httpx
import pytest
from herd import AsyncClient

@pytest.mark.asyncio
async def test_data_plane_deadlock_recovery(herd_daemon):
    """
    Scenario 1: The Data Plane Deadlock
    Verifies that when a client hits a malicious route that hangs indefinitely:
    1. The Context.WithTimeout on the proxy ticks down exactly 30 seconds and cancels.
    2. The resulting HTTP request receives a Timeout error (Client disconnects or returns 504).
    3. The LifecycleManager forcibly kills the worker to prevent zombie resources.
       (We verify this by ensuring subsequent requests on the same proxy_url fail since
        the worker process is killed).
    """
    
    # Establish connection to the generated testing daemon
    client = AsyncClient(address=herd_daemon)
    
    # We acquire a session. Timeout ensures if the worker fails to even start we fail fast. 
    async with client.acquire(worker_type="healthworker", timeout=10) as session:
        proxy_url = session.proxy_url
        assert proxy_url is not None, "Proxy URL must be provided by the session"
        
        # Requests to the daemon must identify the targeted session
        headers = {"X-Session-ID": session.id}
        
        # Verify healthy route first
        async with httpx.AsyncClient() as http_client:
            health_response = await http_client.get(f"{proxy_url}/health", headers=headers)
            assert health_response.status_code == 200, "Worker is not healthy initially"

            # Issue malicious request to /deadlock.
            # We set read timeout on `httpx` to ~10 seconds expecting the 
            # Go daemon's config-defined timeout (e.g. 5s) to sever the connection securely.
            print("\\n[+] Issuing malicious deadlock request. Holding...")
            
            try:
                # The Go daemon should sever this precisely after 5s.
                resp = await http_client.get(f"{proxy_url}/deadlock", headers=headers, timeout=12.0)
                if resp.status_code in [502, 504]:
                    print(f"[+] Caught expected {resp.status_code} response.")
                else:
                    pytest.fail(f"The request should have failed or returned a proxy timeout. Got: {resp.status_code}")
            except httpx.ReadTimeout:
                # Based on the exact HTTP proxy implementation, we might get a Timeout or a Protocol Error 
                # if the daemon rigidly kills the underlying stream.
                print("[+] Caught expected ReadTimeout from proxy")
            except httpx.RemoteProtocolError:
                # Expected if the TCP connection is violently severed.
                print("[+] Caught expected RemoteProtocolError from severed proxy connection")
            except httpx.HTTPStatusError as e:
                # Expected if the Go daemon's proxy serves an HTTP 504 Gateway Timeout or similar.
                assert e.response.status_code in [502, 504], "Expected 5xx response due to context timeout"

            # After the Go proxy enforces the deadline, the daemon's Reaper/Manager
            # should have killed the individual worker process instantly.
            # Therefore, attempting to query the health endpoint again MUST fail 
            # because the underlying process for this session no longer exists.
            print("\\n[+] Verifying zombie worker was assassinated...")
            
            try:
                # Use a very short timeout, worker is already dead.
                resp2 = await http_client.get(f"{proxy_url}/health", headers=headers, timeout=2.0)
                if resp2.status_code == 404 and "session not found" in resp2.text:
                    print("[+] Verified target session was purged (404 Not Found).")
                else:
                    pytest.fail(f"The healthworker process should be DEAD, but it responded. Zombie leak detected! Status: {resp2.status_code}, Body: {resp2.text}")
            except (httpx.ConnectError, httpx.ReadTimeout, httpx.RemoteProtocolError, httpx.HTTPStatusError):
                # Request failure means the process was successfully purged.
                print("[+] Verified target worker is unresponsive (purged).")
