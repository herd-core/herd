// pool.go — Session-affine process pool with singleflight Acquire.
//
// # Core invariant
//
//	1 sessionID → 1 Worker, for the lifetime of the session.
//
// # Concurrency model (read this before touching Acquire)
//
// There are three maps protected by a single mutex (p.mu):
//
//	p.sessions  map[string]Worker[C]       — live sessionID → worker bindings
//	p.inflight  map[string]chan struct{}    — in-progress Acquire for a session
//
// And one lock-free channel:
//
//	p.tickets chan struct{}                — bounded concurrency tokens
//
// The singleflight guarantee for Acquire(ctx, sessionID, config):
//
//  1. Lock → check sessions → if found: unlock, return (FAST PATH).
//  2. Lock → check inflight → if pending: grab chan, unlock, wait on it,
//     then restart from step 1 when chan closes.
//  3. Lock → create inflight[sessionID] = make(chan struct{}) → unlock.
//  4. Block on <-p.tickets (or ctx cancel).
//  5. Call p.factory.Spawn(ctx, sessionID, config). If error: return ticket, close inflight, return error.
//  6. Lock → sessions[sessionID]=w, delete inflight[sid], close(ch) → unlock.
//     Closing ch broadcasts to all goroutines waiting in step 2.
//  7. Return &Session[C]{…}
//
package herd

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/herd-core/herd/observer"
)

// ---------------------------------------------------------------------------
// Session[C]
// ---------------------------------------------------------------------------

type Session[C any] struct {
	ID string
	Worker Worker[C]

	pool *Pool[C]
	once sync.Once
}

func (s *Session[C]) Release() {
	s.once.Do(func() {
		s.pool.release(s.ID, s.Worker)
	})
}

// ---------------------------------------------------------------------------
// PoolStats
// ---------------------------------------------------------------------------

type PoolStats struct {
	TotalWorkers int
	ActiveSessions int
	InflightAcquires int
	Node observer.NodeStats
}

// ---------------------------------------------------------------------------
// Pool[C]
// ---------------------------------------------------------------------------

type Pool[C any] struct {
	factory WorkerFactory[C]
	cfg     config

	mu          sync.Mutex
	registry    SessionRegistry[C]
	inflight    map[string]chan struct{}

	workers []Worker[C]
	tickets chan struct{}

	done chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

func New[C any](factory WorkerFactory[C], opts ...Option) (*Pool[C], error) {
	cfg := defaultConfig()
	for _, apply_options := range opts {
		apply_options(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &Pool[C]{
		factory:   factory,
		cfg:       cfg,
		registry:  NewLocalRegistry[C](),
		inflight:  make(map[string]chan struct{}),
		workers:   make([]Worker[C], 0, cfg.max),
		tickets:   make(chan struct{}, cfg.max),
		done:      make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
	}

	for i := 0; i < cfg.max; i++ {
		p.tickets <- struct{}{}
	}

	return p, nil
}

func (p *Pool[C]) wireWorker(w Worker[C]) {
	w.OnCrash(func(sessionID string) {
		p.onCrash(sessionID)
	})
	p.mu.Lock()
	p.workers = append(p.workers, w)
	p.mu.Unlock()
}

func (p *Pool[C]) Factory() WorkerFactory[C] {
	return p.factory
}

// ---------------------------------------------------------------------------
// Acquire — the core primitive
// ---------------------------------------------------------------------------

func (p *Pool[C]) Acquire(ctx context.Context, sessionID string, config TenantConfig) (*Session[C], error) {
	for {
		var w Worker[C]
		var err error
		p.mu.Lock()

		w, err = p.registry.Get(ctx, sessionID)
		if err != nil {
			p.mu.Unlock()
			return nil, fmt.Errorf("herd: Acquire(%q): directory lookup failed: %w", sessionID, err)
		}
		if w != nil {
			p.mu.Unlock()
			return &Session[C]{ID: sessionID, Worker: w, pool: p}, nil
		}

		if ch, pending := p.inflight[sessionID]; pending {
			p.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, fmt.Errorf("herd: Acquire(%q): context cancelled while waiting for inflight: %w", sessionID, ctx.Err())
			}
		}

		ch := make(chan struct{})
		p.inflight[sessionID] = ch
		p.mu.Unlock()

		select {
		case <-p.tickets:
		case <-ctx.Done():
			p.mu.Lock()
			delete(p.inflight, sessionID)
			p.mu.Unlock()
			close(ch)
			return nil, fmt.Errorf("herd: Acquire(%q): timed out waiting for capacity: %w", sessionID, ctx.Err())
		}

		// Unlock the singleflight so we do not block other concurrent requests while spawning.
		// Wait, the singleflight lock is already released above! (p.mu.Unlock())
		// Spawning is fully concurrent here (except gated by p.tickets channel).
		w, err = p.factory.Spawn(ctx, sessionID, config)
		if err != nil {
			p.tickets <- struct{}{}
			p.mu.Lock()
			delete(p.inflight, sessionID)
			p.mu.Unlock()
			close(ch)
			return nil, fmt.Errorf("herd: Acquire(%q): failed to spawn: %w", sessionID, err)
		}

		p.wireWorker(w)

		p.mu.Lock()
		if err = p.registry.Put(ctx, sessionID, w); err != nil {
			p.mu.Unlock()
			close(ch)
			// we have a worker but failed to pin, cleanup
			_ = w.Close()
			p.removeWorker(w)
			p.tickets <- struct{}{}
			return nil, fmt.Errorf("herd: Acquire(%q): failed to pin session: %w", sessionID, err)
		}

		delete(p.inflight, sessionID)
		p.mu.Unlock()

		close(ch)

		log.Printf("[pool] Acquire(%q): pinned to worker %s", sessionID, w.ID())

		return &Session[C]{ID: sessionID, Worker: w, pool: p}, nil
	}
}

func (p *Pool[C]) GetSession(ctx context.Context, sessionID string) (*Session[C], error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	w, err := p.registry.Get(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("herd: GetSession(%q): directory lookup failed: %w", sessionID, err)
	}
	if w == nil {
		return nil, nil // Not found
	}

	return &Session[C]{ID: sessionID, Worker: w, pool: p}, nil
}

// ---------------------------------------------------------------------------
// release — called by Session.Release
// ---------------------------------------------------------------------------

func (p *Pool[C]) release(sessionID string, w Worker[C]) {
	p.mu.Lock()
	_ = p.registry.Delete(context.Background(), sessionID)
	isValid := false
	for _, existing := range p.workers {
		if existing.ID() == w.ID() {
			isValid = true
			break
		}
	}
	p.mu.Unlock()

	if !isValid {
		log.Printf("[pool] release(%q): worker %s already evicted, discarding", sessionID, w.ID())
		return
	}

	if err := w.Close(); err != nil {
		log.Printf("[pool] release(%q): worker %s close error: %v", sessionID, w.ID(), err)
	}

	p.removeWorker(w)
	p.tickets <- struct{}{}

	log.Printf("[pool] release(%q): worker %s terminated per single-use policy", sessionID, w.ID())
}

func (p *Pool[C]) KillWorker(sessionID string, reason string) error {
	p.mu.Lock()
	w, err := p.registry.Get(context.Background(), sessionID)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("herd: KillWorker(%q): registry lookup failed: %w", sessionID, err)
	}
	if w == nil {
		p.mu.Unlock()
		return nil
	}

	_ = p.registry.Delete(context.Background(), sessionID)
	p.mu.Unlock()

	if err := w.Close(); err != nil {
		return fmt.Errorf("herd: KillWorker(%q): close worker %s: %w", sessionID, w.ID(), err)
	}

	p.removeWorker(w)
	p.tickets <- struct{}{}

	log.Printf("[pool] KillWorker(%q): worker %s terminated (%s)", sessionID, w.ID(), reason)
	return nil
}

// ---------------------------------------------------------------------------
// onCrash — called by processWorker.monitor on unexpected exit
// ---------------------------------------------------------------------------

func (p *Pool[C]) onCrash(sessionID string) {
	p.mu.Lock()
	w, _ := p.registry.Get(context.Background(), sessionID)
	hadSession := w != nil
	_ = p.registry.Delete(context.Background(), sessionID)

	if ch, pending := p.inflight[sessionID]; pending {
		delete(p.inflight, sessionID)
		close(ch)
	}
	p.mu.Unlock()

	if hadSession {
		log.Printf("[pool] onCrash(%q): worker %s crashed — session lost", sessionID, w.ID())
		p.removeWorker(w)
		p.tickets <- struct{}{}
	}

	if p.cfg.crashHandler != nil {
		p.cfg.crashHandler(sessionID)
	}
}

// ---------------------------------------------------------------------------
// Scale helpers
// ---------------------------------------------------------------------------

func (p *Pool[C]) removeWorker(w Worker[C]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, existing := range p.workers {
		if existing.ID() == w.ID() {
			p.workers = append(p.workers[:i], p.workers[i+1:]...)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Stats & Shutdown
// ---------------------------------------------------------------------------

func (p *Pool[C]) Stats() PoolStats {
	nodeStats, _ := observer.PollNodeStats()

	p.mu.Lock()
	defer p.mu.Unlock()
	return PoolStats{
		TotalWorkers:     len(p.workers),
		ActiveSessions:   p.registry.Len(),
		InflightAcquires: len(p.inflight),
		Node:             nodeStats,
	}
}

func (p *Pool[C]) Shutdown(ctx context.Context) error {
	p.cancel()
	close(p.done)
	p.mu.Lock()
	workers := make([]Worker[C], len(p.workers))
	copy(workers, p.workers)
	p.mu.Unlock()

	for _, w := range workers {
		if err := w.Close(); err != nil {
			log.Printf("[pool] Shutdown: error closing worker %s: %v", w.ID(), err)
			return err
		}
	}
	log.Println("[pool] shutdown complete")
	return nil
}
