import asyncio
import uuid
import httpx
import pytest
from herd import AsyncClient


@pytest.mark.asyncio
async def test_clean_disconnect_race_condition(herd_daemon):
    """
    Scenario 4: The Clean Disconnect Race Condition
    This tests the integrity of the granular locking strategy. A client finishes its job
    and cleanly closes the gRPC stream. Your gRPC server receives the EOF and triggers
    the `defer manager.UnregisterAndKill()` block, requiring a write lock on the global registry.
    At that exact millisecond, your background Reaper loop ticks and attempts to acquire a read
    lock on the registry to perform a system-wide sweep.
    """
    
    # Using the python client library implementation:
    client = AsyncClient(address=herd_daemon)
    
    async def run_client_lifecycle(i: int):
        try:
            # We purposely stagger the startups slightly
            await asyncio.sleep(i * 0.1)
            
            # Acquire a worker from the pool
            session_ctx = client.acquire(worker_type="healthworker", timeout=10)
            session = await session_ctx.__aenter__()
            
            # Read a cheap endpoint a few times
            proxy_url = session.proxy_url
            headers = {"X-Session-ID": session.id}
            
            async with httpx.AsyncClient() as hc:
                for _ in range(3):
                    resp = await hc.get(f"{proxy_url}/health", headers=headers)
                    resp.raise_for_status()
                    await asyncio.sleep(0.5)
            # We add a sleep right up until ~4.8 - 5.1 seconds depending on `i`
            # to guarantee that `.close()` is called exactly when the Reaper is reading the lock
            await asyncio.sleep(2.5 + (0.001 * i))
            
            # Cleanly close the session, firing EOF and the write lock
            await session_ctx.__aexit__(None, None, None)
            return True
        except Exception as e:
            print(f"Session failed: {e}")
            return False
            
    # herd_daemon has a pool size of max_workers: 5
    # We spawn 5 concurrent clients to occupy the pool completely
    # and disconnect them nearly simultaneously
    tasks = []
    for i in range(5):
        tasks.append(asyncio.create_task(run_client_lifecycle(i)))
        
    results = await asyncio.gather(*tasks)
    
    assert all(results), "Not all clients cleanly disconnected. Lock contention / Race Condition detected."
