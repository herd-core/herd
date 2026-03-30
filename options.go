// options.go — functional options for Pool construction.
package herd

// ---------------------------------------------------------------------------
// config — internal knobs for Pool
// ---------------------------------------------------------------------------

// config holds all tuneable parameters for a Pool.
// Defaults are set in defaultConfig(); all fields are safe to read concurrently
// after New[C] returns because they are never mutated after that point.
type config struct {
	// max is the ceiling: the pool will never spawn more than this many workers.
	// Default: 10
	max int

	// crashHandler is called when a worker's subprocess exits while it holds an
	// active session. The argument is the sessionID that was lost.
	// Use this to clean up any external state tied to that session.
	// Default: nil (crash is logged but not propagated to caller)
	crashHandler func(sessionID string)
}

// defaultConfig returns the baseline configuration.
// All fields must have a valid, production-ready zero value so that
// herd.New(factory) with no extra options produces a working pool.
func defaultConfig() config {
	return config{
		max: 10,
	}
}

// ---------------------------------------------------------------------------
// Option
// ---------------------------------------------------------------------------

// Option is a functional option for New[C].
type Option func(*config)

// ---------------------------------------------------------------------------
// WithMaxWorkers — pool sizing
// ---------------------------------------------------------------------------

// WithMaxWorkers sets the max capacity.
//
//   - max: hard cap on concurrent workers; Acquire blocks once this limit
//     is reached until a worker becomes available.
//
// Panics if max < 1.
func WithMaxWorkers(max int) Option {
	if max < 1 {
		panic("herd: WithMaxWorkers max must be >= 1")
	}
	return func(c *config) {
		c.max = max
	}
}

// ---------------------------------------------------------------------------
// WithCrashHandler — crash recovery callback
// ---------------------------------------------------------------------------

// WithCrashHandler registers a callback invoked when a worker's subprocess
// exits unexpectedly while it holds an active session.
//
// fn receives the sessionID that was lost. Use it to:
//   - Delete session-specific state in your database
//   - Return an error to the end user ("your session was interrupted")
//   - Trigger a re-run of the failed job
//
// fn is called from a background monitor goroutine. It must not block for
// extended periods; spawn a goroutine if you need to do heavy work.
//
// If WithCrashHandler is not set, crashes are only logged.
func WithCrashHandler(fn func(sessionID string)) Option {
	return func(c *config) { c.crashHandler = fn }
}
