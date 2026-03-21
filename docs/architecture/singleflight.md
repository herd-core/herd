# Singleflight & Locks

Herd enforces strict session affinity even under heavy concurrent load. To prevent "thundering herd" issues, it utilizes a lightweight singleflight mechanism.

---

## 💥 The Race Condition

If two or more concurrent HTTP requests for `sessionID = "user-123"` arrive at the `Pool` at the exact same millisecond, a naive implementation might:
1.  See that no worker is pinned to `user-123`.
2.  Pop a free worker `W1` from the pool.
3.  Pop a free worker `W2` from the pool for the second request.
4.  Pin BOTH workers to the same session ID!

This breaks the core invariant: **1 Session ID → 1 Worker**.

---

## 🛡️ The Solution: Channel Broadcast

Herd maintains an `inflight` map of channels (`map[string]chan struct{}`) to serialize acquisitions for the same session ID.

### The Lifecycle

1.  **Check Phase**: `Pool.Acquire` first checks if the session ID exists in the `sessions` map. If it does, it returns immediately (Fast Path).
2.  **Lock Phase**: If it doesn't exist, it checks the `inflight` map:
    -   **If no inflight channel exists**:
        -   The goroutine creates a channel `ch := make(chan struct{})` and adds it to the `inflight` map.
        -   It proceeds to pop a worker from the `available` list and pins it to the session.
        -   Finally, it closes the channel `close(ch)` and removes it from `inflight`.
    -   **If an inflight channel DOES exist**:
        -   The goroutine does **not** fetch a worker.
        -   It waits on the channel: `<-ch`.
        -   Once the channel is closed, it knows the first goroutine completed the workspace binding.
        -   It fetches the newly pinned worker from the `sessions` map and continues.

This mechanism ensures that only the **first** request incurs the overhead of worker allocation or validation, while backlogs are queued smoothly with zero wasted resources.
