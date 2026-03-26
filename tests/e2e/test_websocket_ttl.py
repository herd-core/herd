import asyncio
import httpx
import websockets
import pytest
import time
from herd import AsyncClient

@pytest.mark.asyncio
async def test_legitimate_websocket_absolute_ttl(herd_daemon_websocket):
    """
    Scenario 3: The Legitimate Long-Running WebSocket
    Verifies that healthy traffic is not murdered prematurely, but absolute TTL is respected:
    1. A client connects and upgrades to a WebSocket.
    2. Because it is a WebSocket, the 2-second data timeout is correctly bypassed.
    3. The client regularly sends gRPC pings.
    4. We verify the worker survives past typical timeouts (e.g. up to 10s).
    5. The worker must be killed precisely when `AbsoluteTTL` (15s) is reached.
    """
    daemon_addr = herd_daemon_websocket["remote"]
    
    # Step 1: Establish connection normally
    client = AsyncClient(address=daemon_addr)
    
    start_time = time.time()
    
    async with client.acquire(worker_type="healthworker", timeout=10) as session:
        proxy_url = session.proxy_url
        session_id = session.id
        
        # Convert http://127.0.0.1:PORT to ws://127.0.0.1:PORT
        ws_url = proxy_url.replace("http://", "ws://") + "/ws"
        
        headers = {"X-Session-ID": session_id}
        
        # Step 2: Establish the WebSocket connection
        print(f"\\n[+] Establishing WebSocket connection to {ws_url}...")
        try:
            async with websockets.connect(
                ws_url, 
                extra_headers=headers,
                ping_interval=None # disable ping to test bare minimum
            ) as ws:
                print("[+] WebSocket connected successfully!")
                
                # Step 3 & 4: Prove it outlives normal bounds without being reaped.
                # In conftest, data_timeout is 2s, heartbeat grace is 5s. 
                # We'll wait 10 seconds. The native background heartbeat task in the `AsyncClient` 
                # will keep the lease alive, and the WS connection will keep ActiveConns = 1.
                print("[+] Exchanging messages to prove channel is alive for 10 seconds...")
                
                for i in range(5):
                    await asyncio.sleep(2.0)
                    msg = f"ping_{i}"
                    await ws.send(msg)
                    resp = await ws.recv()
                    assert resp == f"pong: {msg}"
                    print(f"    - Exchanged message successfully: {resp} (elapsed: {int(time.time() - start_time)}s)")
                
                # Check elapsed time
                elapsed = time.time() - start_time
                assert elapsed >= 10.0, "Did not survive long enough to prove the scenario."
                print("[+] Successfully bypassed data_timeout (2s) and standard sweeps!")
                
                # Step 5: Test the Absolute TTL Guillotine.
                # In conftest, AbsoluteTTL is perfectly tuned to 15s. 
                # The reaper ticks every 5 seconds. So it might take up to 20 seconds
                # to actually detect and kill the process depending on cycle offset.
                print("[+] Waiting for the 15s Absolute TTL guillotine to drop (waiting up to 13s extra to ensure sweep)...")
                
                try:
                    await asyncio.sleep(13.0)
                    # Try to send/recv one more time
                    await ws.send("final_ping")
                    await ws.recv()
                    pytest.fail("WebSocket survived past the AbsoluteTTL. Executioner failed!")
                except websockets.exceptions.ConnectionClosed:
                    print("[+] Caught expected ConnectionClosed exception. Absolute TTL enforced!")

        except Exception as e:
            if not isinstance(e, AssertionError) and "Did not survive" not in str(e):
                # Ensure we didn't fail early
                pytest.fail(f"WebSocket closed prematurely: {e}")

    # Finally, ensure the backing worker is genuinely gone
    async with httpx.AsyncClient() as http_client:
        try:
            resp = await http_client.get(f"{proxy_url}/health", headers=headers, timeout=2.0)
            if resp.status_code == 404:
                print("[+] Verified target session was purged (404) after Absolute TTL.")
            else:
                pytest.fail(f"Worker responded after Absolute TTL! Status: {resp.status_code}")
        except (httpx.ConnectError, httpx.ReadTimeout, httpx.RemoteProtocolError, httpx.HTTPStatusError):
            print("[+] Verified target worker is unresponsive (purged).")
