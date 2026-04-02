package uid

import (
	"errors"
	"sync"
	"testing"
)

func TestNewPool_Validation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		start     int
		size      int
		wantErrOK bool
	}{
		{"zero start", 0, 10, true},
		{"negative start", -1, 10, true},
		{"zero size", 300000, 0, true},
		{"negative size", 300000, -1, true},
		{"valid", 300000, 100, false},
		{"min valid start", 1, 1, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := NewPool(tc.start, tc.size)
			if tc.wantErrOK {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if p != nil {
					t.Fatalf("expected nil pool on error, got non-nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if p == nil {
					t.Fatalf("expected non-nil pool")
				}
			}
		})
	}
}

func TestPool_InitialState(t *testing.T) {
	t.Parallel()

	p, err := NewPool(300000, 50)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	if got := p.Available(); got != 50 {
		t.Fatalf("Available: want 50, got %d", got)
	}
	if got := p.Capacity(); got != 50 {
		t.Fatalf("Capacity: want 50, got %d", got)
	}
	if got := p.Start(); got != 300000 {
		t.Fatalf("Start: want 300000, got %d", got)
	}
}

func TestPool_CheckoutOrderIsAscending(t *testing.T) {
	t.Parallel()

	const start, size = 300000, 5
	p, _ := NewPool(start, size)

	for i := 0; i < size; i++ {
		uid, err := p.Checkout()
		if err != nil {
			t.Fatalf("Checkout #%d: unexpected error: %v", i, err)
		}
		want := start + i
		if uid != want {
			t.Fatalf("Checkout #%d: want %d, got %d", i, want, uid)
		}
		if got := p.Available(); got != size-i-1 {
			t.Fatalf("Available after checkout #%d: want %d, got %d", i, size-i-1, got)
		}
	}
}

func TestPool_Exhaustion(t *testing.T) {
	t.Parallel()

	p, _ := NewPool(300000, 2)
	_, _ = p.Checkout()
	_, _ = p.Checkout()

	uid, err := p.Checkout()
	if !errors.Is(err, ErrPoolExhausted) {
		t.Fatalf("expected ErrPoolExhausted, got err=%v uid=%d", err, uid)
	}
	if uid != 0 {
		t.Fatalf("expected zero UID on exhaustion, got %d", uid)
	}
}

func TestPool_ReturnAndRecheckout(t *testing.T) {
	t.Parallel()

	p, _ := NewPool(300000, 3)

	// Drain the pool.
	u0, _ := p.Checkout()
	u1, _ := p.Checkout()
	u2, _ := p.Checkout()

	// Return in different order.
	if err := p.Return(u2); err != nil {
		t.Fatalf("Return(%d): %v", u2, err)
	}
	if err := p.Return(u0); err != nil {
		t.Fatalf("Return(%d): %v", u0, err)
	}
	if err := p.Return(u1); err != nil {
		t.Fatalf("Return(%d): %v", u1, err)
	}

	if got := p.Available(); got != 3 {
		t.Fatalf("Available after returning all: want 3, got %d", got)
	}

	// Should be able to check out again.
	uid, err := p.Checkout()
	if err != nil {
		t.Fatalf("Checkout after return: %v", err)
	}
	// The UID must be one of the three we returned.
	valid := map[int]bool{u0: true, u1: true, u2: true}
	if !valid[uid] {
		t.Fatalf("Checkout returned unexpected uid %d", uid)
	}
}

func TestPool_DoubleReturn(t *testing.T) {
	t.Parallel()

	p, _ := NewPool(300000, 1)
	uid, _ := p.Checkout()

	if err := p.Return(uid); err != nil {
		t.Fatalf("first Return: %v", err)
	}

	// Second return should error (pool is full).
	if err := p.Return(uid); err == nil {
		t.Fatal("expected error on double-return, got nil")
	}
}

func TestPool_OutOfRangeReturn(t *testing.T) {
	t.Parallel()

	p, _ := NewPool(300000, 10)

	cases := []int{0, 299999, 300010, 400000, -1}
	for _, uid := range cases {
		if err := p.Return(uid); err == nil {
			t.Fatalf("Return(%d): expected out-of-range error, got nil", uid)
		}
	}
}

// TestPool_Concurrent spawns N goroutines that each checkout and return exactly
// one UID. After all goroutines finish, the pool must be full again with no
// data races.
func TestPool_Concurrent(t *testing.T) {
	t.Parallel()

	const (
		poolSize   = 100
		goroutines = 200 // deliberate: goroutines > pool size to stress-test exhaustion handling
	)

	p, _ := NewPool(500000, poolSize)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		leased   []int
		failures int
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()

			uid, err := p.Checkout()
			if errors.Is(err, ErrPoolExhausted) {
				// Acceptable — pool is smaller than goroutines.
				return
			}
			if err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
				return
			}

			// Simulate work.
			mu.Lock()
			leased = append(leased, uid)
			mu.Unlock()

			if err := p.Return(uid); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if failures > 0 {
		t.Fatalf("concurrent test: %d unexpected failures", failures)
	}

	// After all goroutines have returned their UIDs, the pool should be full.
	if got := p.Available(); got != poolSize {
		t.Fatalf("Available after concurrent round-trip: want %d, got %d", poolSize, got)
	}

	// All leased UIDs must be within the pool range.
	for _, uid := range leased {
		if uid < 500000 || uid >= 500000+poolSize {
			t.Fatalf("leased UID %d is out of pool range [500000, %d)", uid, 500000+poolSize)
		}
	}
}
