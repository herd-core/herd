import asyncio
import httpx
import pytest
import time
import random
from herd import AsyncClient

@pytest.mark.asyncio
async def test_production_burst(herd_daemon_stress):
    """
    Simulates a production environment where:
    1. A baseline of active sessions is established and held open.
    2. A massive unexpected "burst" of requests hits the system concurrently.
    3. The pool must seamlessly provision and assign capacity (target_idle mechanics).
    4. The burst traffic gracefully finishes and resources are successfully released (burst down).
    5. The baseline traffic remains unaffected throughout the chaos.
    """
    
    client = AsyncClient(address=herd_daemon_stress)
    
    BASELINE_SESSIONS = 50
    BURST_SESSIONS = 60
    
    baseline_contexts = []
    baseline_tasks = []
    
    async def baseline_worker(ctx, idx: int):
        try:
            session = await ctx.__aenter__()
            # Keep baseline session busy to ensure it isn't affected
            proxy_url = session.proxy_url
            headers = {"X-Session-ID": session.id}
            
            async with httpx.AsyncClient() as hc:
                # Do continuous health checks equivalent to long-running work
                for _ in range(15):
                    resp = await hc.get(f"{proxy_url}/health", headers=headers, timeout=5.0)
                    resp.raise_for_status()
                    await asyncio.sleep(0.5)
                    
            return True
        except Exception as e:
            print(f"[!] Baseline {idx} failed: {e}")
            return False

    print(f"\n[+] Establishing baseline traffic with {BASELINE_SESSIONS} concurrent sessions...")
    # 1. Establish baseline
    for i in range(BASELINE_SESSIONS):
        ctx = client.acquire(worker_type="healthworker", timeout=15)
        baseline_contexts.append(ctx)
        baseline_tasks.append(asyncio.create_task(baseline_worker(ctx, i)))
        
    # Give the baseline a tiny bit of time to acquire
    await asyncio.sleep(2.0)
    
    async def burst_worker(idx: int):
        try:
            # Add up to 20ms jitter to avoid perfect thundering herd event-loop lockup
            await asyncio.sleep(random.uniform(0, 0.020))
            
            start_time = time.time()
            ctx = client.acquire(worker_type="healthworker", timeout=20)
            session = await ctx.__aenter__()
            acquire_time = time.time() - start_time
            
            # Simulate processing of burst traffic
            proxy_url = session.proxy_url
            headers = {"X-Session-ID": session.id}
            
            async with httpx.AsyncClient() as hc:
                resp = await hc.get(f"{proxy_url}/health", headers=headers, timeout=5.0)
                resp.raise_for_status()
                # Do a little arbitrary work
                await asyncio.sleep(5)
                
            await ctx.__aexit__(None, None, None)
            return True, acquire_time
        except Exception as e:
            print(f"[!] Burst {idx} failed: {e}")
            return False, 0.0

    print(f"\n[+] Incoming burst traffic: {BURST_SESSIONS} concurrent sessions...")
    
    # 2. Trigger Burst Up
    burst_start = time.time()
    burst_tasks = [asyncio.create_task(burst_worker(i)) for i in range(BURST_SESSIONS)]
    burst_results = await asyncio.gather(*burst_tasks)
    burst_total_time = time.time() - burst_start
    successes = sum(1 for success, _ in burst_results if success)
    max_acquire = max(latency for success, latency in burst_results if success)
    
    print(f"\n[+] Burst Traffic Complete.")
    print(f"    - Success: {successes} / {BURST_SESSIONS}")
    print(f"    - Max Acquisition Latency: {max_acquire:.2f}s")
    print(f"    - Total Burst Duration: {burst_total_time:.2f}s")
    
    assert successes == BURST_SESSIONS, f"Dropped traffic during burst! {BURST_SESSIONS - successes} failed."
    assert max_acquire < 15.0, "Worker provisioning took too long during burst scale up."
    
    # 3. Burst traffic is done. Wait for baseline to finish its run.
    print(f"\n[+] Burst traffic complete. Awaiting baseline task completion...")
    baseline_results = await asyncio.gather(*baseline_tasks)
    baseline_successes = sum(1 for success in baseline_results if success)

    print(f"\n[+] Cleaning up baseline traffic...")
    for ctx in baseline_contexts:
        await ctx.__aexit__(None, None, None)
    
    assert baseline_successes == BASELINE_SESSIONS, "Baseline traffic failed while defending against burst!"
    print("[+] Production burst scaling handled smoothly.")