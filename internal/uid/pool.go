// Package uid provides a thread-safe, lock-free pool of unprivileged UIDs for
// per-MicroVM isolation. Each Firecracker process is assigned a unique UID/GID
// from the pool, ensuring DAC (Discretionary Access Control) isolation between
// concurrent tenants on the same host.
//
// Design rationale:
//   - A buffered channel is used as the backing store. Channel send/receive is
//     handled by the Go runtime via lock-free fast-paths in most cases, making
//     Checkout a sub-microsecond operation with zero mutex overhead on the hot path.
//   - Checkout is a non-blocking select so callers get an immediate ErrPoolExhausted
//     rather than blocking indefinitely when the pool is empty.
//   - Return validates the returned UID is in-range to catch bugs early rather
//     than silently corrupting the pool.
package uid

import (
	"errors"
	"fmt"
)

// ErrPoolExhausted is returned by Checkout when no UIDs are available.
var ErrPoolExhausted = errors.New("uid pool exhausted: all UIDs are currently leased to active VMs")

// Pool is a thread-safe, lock-free pool of unprivileged UIDs.
// It must be created via NewPool; the zero value is not usable.
//
// The pool assigns UIDs from a contiguous range [start, start+size).
// Both Checkout and Return are safe for concurrent use from multiple goroutines.
type Pool struct {
	ch    chan int
	start int
	end   int // exclusive upper bound: start + size
}

// NewPool creates a Pool covering UIDs in [start, start+size).
//
// Constraints:
//   - start must be > 0 (UID 0 is root; never acceptable as a jailer UID).
//   - size must be >= 1.
//
// The pool is pre-filled in ascending order so the first Checkout always returns
// start, making behaviour deterministic and easy to reason about in tests.
func NewPool(start, size int) (*Pool, error) {
	if start <= 0 {
		return nil, fmt.Errorf("uid pool: start must be > 0, got %d", start)
	}
	if size < 1 {
		return nil, fmt.Errorf("uid pool: size must be >= 1, got %d", size)
	}

	ch := make(chan int, size)
	for i := 0; i < size; i++ {
		ch <- start + i
	}

	return &Pool{
		ch:    ch,
		start: start,
		end:   start + size,
	}, nil
}

// Checkout leases the next available UID from the pool.
//
// This is a non-blocking operation: if the pool is empty it returns
// ErrPoolExhausted immediately rather than blocking the caller.
// The caller must call Return when the UID is no longer needed (i.e., after the
// associated Firecracker process has fully exited and all resources have been
// cleaned up).
func (p *Pool) Checkout() (int, error) {
	select {
	case uid := <-p.ch:
		return uid, nil
	default:
		return 0, ErrPoolExhausted
	}
}

// Return gives a previously leased UID back to the pool.
//
// It returns an error if the UID is outside the pool's range (programming error)
// or if the pool's channel is already full (double-return / bug), rather than
// panicking or blocking.
func (p *Pool) Return(uid int) error {
	if uid < p.start || uid >= p.end {
		return fmt.Errorf("uid pool: cannot return uid %d: out of pool range [%d, %d)", uid, p.start, p.end)
	}
	select {
	case p.ch <- uid:
		return nil
	default:
		return fmt.Errorf("uid pool: cannot return uid %d: pool is already full (double-return?)", uid)
	}
}

// Available returns a snapshot of the number of UIDs currently available for
// checkout. This is a point-in-time observation; the true count may change
// immediately after the call.
func (p *Pool) Available() int {
	return len(p.ch)
}

// Capacity returns the total size of the pool (available + leased).
func (p *Pool) Capacity() int {
	return cap(p.ch)
}

// Start returns the first UID in the pool's range.
func (p *Pool) Start() int {
	return p.start
}
