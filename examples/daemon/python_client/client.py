import grpc
import requests
import queue
import time
import threading

import herd_pb2
import herd_pb2_grpc

class HerdClient:
    def __init__(self, socket_path="/tmp/herd.sock"):
        """Initialize the client with the UDS path to the Control Plane."""
        self.channel = grpc.insecure_channel(f'unix://{socket_path}')
        self.stub = herd_pb2_grpc.HerdServiceStub(self.channel)
        
        # Queue to hold requests for the bidirectional gRPC stream
        self._queue = queue.Queue()
        self._response_iterator = None
        self._stream_thread = None
        
        self.session_id = None
        self.proxy_address = None

    def _request_generator(self):
        """Generator that reads from the queue to keep the gRPC stream open."""
        while True:
            req = self._queue.get()
            if req is None:  # None acting as EOF signal
                break
            yield req

    def acquire(self, worker_type="", timeout_seconds=0):
        """
        Requests a worker from the Herd control plane.
        Starts the gRPC stream and parses the first response to get the session details.
        """
        # 1. Start the bi-directional stream
        self._response_iterator = self.stub.Acquire(self._request_generator())
        
        # 2. Send the initial acquire request
        self._queue.put(herd_pb2.AcquireRequest(
            worker_type=worker_type,
            timeout_seconds=timeout_seconds
        ))
        
        # 3. Read the first response back
        try:
            resp = next(self._response_iterator)
            self.session_id = resp.session_id
            self.proxy_address = resp.proxy_address
            # Assuming returning proxy has http:// prefix. Add it if it doesn't.
            if not self.proxy_address.startswith("http"):
                self.proxy_address = f"http://{self.proxy_address}"
                
            return resp
        except grpc.RpcError as e:
            print(f"Failed to acquire session: {e}")
            raise

    def proxy_request(self, method, path, **kwargs):
        """
        Helper method to send an HTTP request to the Data Plane proxy.
        Automatically injects the required X-Session-ID header.
        """
        if not self.session_id:
            raise ValueError("Must acquire a session first")

        url = f"{self.proxy_address}{path}"
        
        # Inject the Herd session header
        headers = kwargs.pop('headers', {})
        headers['X-Session-ID'] = self.session_id

        # Use a persistent requests.Session for performance if available
        if not hasattr(self, '_http_session'):
            self._http_session = requests.Session()

        # Forward actual user traffic via regular HTTP
        return self._http_session.request(method, url, headers=headers, **kwargs)

    def close(self):
        """Gracefully halves the stream, telling the Daemon to clean up the worker."""
        if self._queue:
            self._queue.put(None) # Signal generator to exit
        if self.channel:
            self.channel.close()
        if hasattr(self, '_http_session'):
            self._http_session.close()

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        self.close()

if __name__ == "__main__":
    import concurrent.futures
    import statistics

    # --- Configuration ---
    NUM_CONCURRENT_CLIENTS = 10     # Represents number of concurrent users / gRPC streams
    REQUESTS_PER_CLIENT = 50        # How many HTTP requests each user makes sequentially
    # ---------------------

    def simulate_client(client_id):
        stats = {
            "client_id": client_id,
            "acquire_time": 0,
            "http_latencies": [],
            "successes": 0,
            "errors": 0
        }
        
        try:
            t0 = time.time()
            with HerdClient("/tmp/herd.sock") as client:
                # 1. Connect and acquire session (Control Plane)
                client.acquire(worker_type=f"test_worker")
                t1 = time.time()
                stats["acquire_time"] = t1 - t0
                
                # 2. Blast standard HTTP requests (Data Plane proxy)
                for _ in range(REQUESTS_PER_CLIENT):
                    req_start = time.time()
                    try:
                        resp = client.proxy_request("GET", "/health")
                        if resp.status_code == 200:
                            stats["successes"] += 1
                        else:
                            stats["errors"] += 1
                    except Exception as e:
                        stats["errors"] += 1
                    finally:
                        req_end = time.time()
                        stats["http_latencies"].append(req_end - req_start)
                        
        except Exception as e:
            print(f"[Client {client_id}] Failed to complete tasks: {e}")
            stats["errors"] += REQUESTS_PER_CLIENT
            
        return stats

    print(f"🚀 Starting Herd Performance & Concurrency Test")
    print(f"   - Concurrent Clients: {NUM_CONCURRENT_CLIENTS} (Simultaneous gRPC Streams)")
    print(f"   - HTTP Requests per Client: {REQUESTS_PER_CLIENT}")
    print(f"   - Total Expected HTTP Requests: {NUM_CONCURRENT_CLIENTS * REQUESTS_PER_CLIENT}")
    print("-" * 50)

    start_time = time.time()
    
    # Run test using ThreadPool limits
    results = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=NUM_CONCURRENT_CLIENTS) as executor:
        futures = [executor.submit(simulate_client, i) for i in range(NUM_CONCURRENT_CLIENTS)]
        for future in concurrent.futures.as_completed(futures):
            results.append(future.result())

    total_time = time.time() - start_time
    
    # Aggregate Metrics
    total_success = sum(r["successes"] for r in results)
    total_errors = sum(r["errors"] for r in results)
    all_latencies = [lat for r in results for lat in r["http_latencies"]]
    all_acquires = [r["acquire_time"] for r in results if r["acquire_time"] > 0]
    
    avg_latency = statistics.mean(all_latencies) if all_latencies else 0
    p95_latency = statistics.quantiles(all_latencies, n=20)[18] if len(all_latencies) >= 20 else 0
    avg_acquire = statistics.mean(all_acquires) if all_acquires else 0

    print("\n🏁 TEST COMPLETE")
    print(f"⏱️  Total Duration:      {total_time:.2f} seconds")
    print(f"⚡ Throughput:         {len(all_latencies) / total_time:.2f} req/s")
    print("\n📊 CONNECTION METRICS (Control Plane)")
    print(f"   Average Acquire Time: {avg_acquire * 1000:.2f} ms")
    
    print("\n📊 HTTP METRICS (Data Plane)")
    print(f"   Total Successes:      {total_success}")
    print(f"   Total Errors:         {total_errors}")
    print(f"   Average Latency:      {avg_latency * 1000:.2f} ms")
    if p95_latency:
        print(f"   P95 Latency:          {p95_latency * 1000:.2f} ms")
