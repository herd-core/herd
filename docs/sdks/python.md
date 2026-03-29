# Python SDK

The official Herd Python client offers a robust, production-ready interface for interacting with the Herd daemon. It supports both fully synchronous and highly concurrent `asyncio` patterns.

## Installation

Install the library directly from PyPI:

```bash
pip install herd-client
```

## Quick Start (Synchronous)

For standard scripts, data processing tools, or non-async web frameworks, use the synchronous `Client`. Using it as a context manager ensures the background heartbeat process automatically starts and stops safely.

```python
import requests
from herd import Client

# Note: The transport can be unix:/// or a standard tcp endpoint
client = Client("unix:///tmp/herd.sock")

# Acquire a dedicated worker process for this block
with client.acquire(worker_type="healthworker", timeout=15) as session:
    print(f"Connected to worker: {session.id}")
    
    # The proxy URL handles dynamic routing directly to that specific worker
    resp = requests.post(
        f"{session.proxy_url}/health",
        headers={"X-Session-ID": session.id}
    )
    
    print(resp.json())
```

## Quick Start (Asyncio)

For high-performance gateways, FastAPI integrations, or asynchronous tools, use the `AsyncClient`.

```python
import asyncio
import httpx
from herd import AsyncClient

async def main():
    client = AsyncClient("unix:///tmp/herd.sock")
    
    async with client.acquire(worker_type="my-worker") as session:
        print(f"Connected to worker: {session.id}")
        
        async with httpx.AsyncClient() as http:
            resp = await http.post(
                f"{session.proxy_url}/health",
                headers={"X-Session-ID": session.id}
            )
            print(resp.json())

if __name__ == "__main__":
    asyncio.run(main())
```

## Under the Hood

When you call `acquire()` and enter the `with` block:
1. The client opens a gRPC stream over the Control Plane and formally registers a session with the daemon's `LifecycleManager`.
2. A background execution thread/task is automatically spun up that sends a `PING` every few seconds to keep the process alive.
3. If your script crashes, the context manager drops the heartbeat, and the daemon's `Reaper` immediately assassinates the zombie worker process, guaranteeing zero container or dependency leaks.
