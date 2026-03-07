// examples/browser/main.go — steel-browser adapter built on herd.
//
// This is the steel-orchestrator rewritten as a herd user.
// External API is identical: POST/GET/DELETE /sessions.
// Internal code is ~1/5 the size.
//
// # Removed vs the original orchestrator
//
//   - pool.go, worker.go, session.go  → replaced by herd.Pool
//   - proxy.go + retry loop          → herd health-check + singleflight make
//     retry logic unnecessary; replaced by 3-line forwardTo()
//   - manual crash handler wiring    → herd.WithCrashHandler option
//
// # Run
//
//	go run . --binary ../../steel-browser --port 8080
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hackstrix/herd"
)

func main() {
	min := flag.Int("min-workers", 2, "minimum worker processes")
	max := flag.Int("max-workers", 10, "maximum worker processes")
	port := flag.Int("port", 8080, "listen port")
	binary := flag.String("binary", "./steel-browser", "path to steel-browser binary")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// ── Pool setup ─────────────────────────────────────────────────────────
	// ProcessFactory handles: free port allocation, PORT env injection,
	// /health polling, crash monitoring, auto-restart.
	// WithCrashHandler logs the lost session — add DB cleanup here if needed.
	pool, err := herd.New(
		herd.NewProcessFactory(*binary),
		herd.WithAutoScale(*min, *max),
		herd.WithTTL(5*time.Minute),
		herd.WithCrashHandler(func(sessionID string) {
			log.Printf("[browser] session %q lost — worker crashed", sessionID)
		}),
	)
	if err != nil {
		log.Fatalf("failed to start pool: %v", err)
	}

	// ── Routes ─────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// POST /sessions — acquire worker → create session on it → pin sessionID
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, _ := io.ReadAll(r.Body)
		defer r.Body.Close()

		// Step 1: get any free worker to create the session (no sessionID yet)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()

		// Use a temporary unique key for the initial acquire so the pool
		// hands us exactly one free worker slot.
		tempKey := fmt.Sprintf("new-%d", time.Now().UnixNano())
		sess, err := pool.Acquire(ctx, tempKey)
		if err != nil {
			http.Error(w, "no workers available: "+err.Error(), http.StatusServiceUnavailable)
			return
		}

		// Step 2: forward POST /sessions to the chosen worker
		respBody, status, err := forwardTo(sess.Worker, http.MethodPost, "/sessions", body, "application/json")
		if err != nil {
			sess.Release() // return worker to pool on failure
			http.Error(w, "worker error: "+err.Error(), http.StatusBadGateway)
			return
		}

		// Step 3: parse the returned session ID from steel-browser
		var parsed struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.ID == "" {
			sess.Release()
			http.Error(w, "could not parse session ID from worker response", http.StatusBadGateway)
			return
		}

		// Step 4: re-pin under the real sessionID.
		// Release the tempKey slot and immediately Acquire the real ID.
		// This is safe: the same worker is still free (nothing else grabbed it)
		// because Release + Acquire are both synchronous from this goroutine.
		sess.Release()

		realCtx, realCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer realCancel()
		realSess, err := pool.Acquire(realCtx, parsed.ID)
		if err != nil {
			http.Error(w, "failed to pin real session: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		// Hold the real session open — the client will call DELETE /sessions/:id
		// or TTL will sweep it after 5 minutes of inactivity.
		_ = realSess

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write(respBody)
	})

	// GET /sessions/:id  — look up pinned worker, forward, keep session alive
	// DELETE /sessions/:id — forward, then release the herd session
	mux.HandleFunc("/sessions/", func(w http.ResponseWriter, r *http.Request) {
		sessionID := strings.TrimPrefix(r.URL.Path, "/sessions/")
		if sessionID == "" {
			http.Error(w, "session ID required", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		sess, err := pool.Acquire(ctx, sessionID)
		if err != nil {
			http.Error(w, "session not found or worker unavailable: "+err.Error(), http.StatusNotFound)
			return
		}

		switch r.Method {
		case http.MethodGet:
			respBody, status, err := forwardTo(sess.Worker, http.MethodGet, "/sessions/"+sessionID, nil, "")
			if err != nil {
				sess.Release()
				http.Error(w, "worker error: "+err.Error(), http.StatusBadGateway)
				return
			}
			// Don't release — keep session pinned for future requests
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			w.Write(respBody)

		case http.MethodDelete:
			_, _, _ = forwardTo(sess.Worker, http.MethodDelete, "/sessions/"+sessionID, nil, "")
			sess.Release() // free the worker — session is done
			w.WriteHeader(http.StatusNoContent)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Housekeeping endpoints
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		s := pool.Stats()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s)
	})

	// ── Graceful shutdown ──────────────────────────────────────────────────
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		sig := <-ch
		log.Printf("received %s — shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = pool.Shutdown(ctx)
		os.Exit(0)
	}()

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("[browser] listening on %s (binary=%s min=%d max=%d)", addr, *binary, *min, *max)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// ---------------------------------------------------------------------------
// forwardTo — 3-line proxy helper
// ---------------------------------------------------------------------------

// forwardTo sends method + path to the worker's base address and returns
// the raw response body. Content-type is only set when non-empty.
func forwardTo(w herd.Worker[*http.Client], method, path string, body []byte, contentType string) ([]byte, int, error) {
	url := w.Address() + path

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = strings.NewReader(string(body))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := w.Client().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("forward %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, err
}
