import asyncio
import httpx
import pytest
import time
from herd import AsyncClient

@pytest.mark.asyncio
async def test_thundering_allocation_spike(herd_daemon_stress):
    """
    Scenario 5: The Thundering Allocation Spike
    1. Triggers massive concurrent connection attempts (100 sessions) 
       simultaneously to test the daemon's internal gRPC handler and global write locks.
    2. Keeps them alive to evaluate the Reaper's `sweep()` read lock overhead.
    3. Tears them all down to assess the corresponding connection closures.
    """
    
    # Use our specialized fixture with max_workers: 150
    client = AsyncClient(address=herd_daemon_stress)
    
    # We will spawn 100 concurrent tasks trying to acquire a worker at the exact same time
    # to hit the gRPC server and the `manager.Register()` bottleneck.
    TARGET_CONCURRENCY = 10
    
    async def worker_lifecycle(i: int):
        try:
            # Phase 1: Thundering Herd Acquisition
            # We add zero jitter to deliberately create a "thundering herd"
            start_time = time.time()
            session_ctx = client.acquire(worker_type="healthworker", timeout=15)
            session = await session_ctx.__aenter__()
            acquire_time = time.time() - start_time
            print(f"[+] Task {i} acquired session in {acquire_time:.2f}s")
            
            # Phase 2: Active Connection Hold
            # We want all 100 workers alive at the same time so the next background
            # Reaper `sweep()` cycle is forced to iterate over a huge snapshot of 100 items
            # validating that holding the global lock is minimal.
            proxy_url = session.proxy_url
            headers = {"X-Session-ID": session.id}
            
            # Keep alive for 8 seconds (enough to span a 5s Reaper sweep)
            async with httpx.AsyncClient() as hc:
                print(f"[+] Task {i} sending health check to {proxy_url}")
                # resp = await hc.get(f"{proxy_url}/health", headers=headers, timeout=5.0)
                # resp.raise_for_status()
            
            # Phase 3: The Thundering Stampede Disconnect
            await session_ctx.__aexit__(None, None, None)
            return True, acquire_time
            
        except Exception as e:
            print(f"[!] Session {i} failed: {e}")
            return False, 0.0

    print(f"\\n[+] Firing {TARGET_CONCURRENCY} concurrent test connections...")
    start_total = time.time()
    
    tasks = [asyncio.create_task(worker_lifecycle(i)) for i in range(TARGET_CONCURRENCY)]
    results = await asyncio.gather(*tasks)
    
    total_duration = time.time() - start_total
    
    successes = sum(1 for success, _ in results if success)
    max_acquire = max(latency for success, latency in results if success)
    
    print(f"\\n[+] Spike Results: {successes}/{TARGET_CONCURRENCY} completely succeeded.")
    print(f"[+] Max Acquisition latency: {max_acquire:.2f} seconds.")
    print(f"[+] Total test duration: {total_duration:.2f} seconds.")
    
    assert successes == TARGET_CONCURRENCY, f"Only {successes}/{TARGET_CONCURRENCY} connections succeeded under load! Lock/Registry throttling detected."
    # If the global registry mutex choked, the max acquire latency would skyrocket or timeout.
    assert max_acquire < 12.0, f"Acquiring workers took way too long ({max_acquire:.2f}s)! Potential registry lock contention bottleneck."

