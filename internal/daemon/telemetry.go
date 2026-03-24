package daemon

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type EventLogger struct {
	format string
	logger *log.Logger
}

func NewEventLogger(format string, logger *log.Logger) *EventLogger {
	if logger == nil {
		logger = log.Default()
	}
	if format != "json" && format != "text" {
		format = "text"
	}
	return &EventLogger{format: format, logger: logger}
}

func (l *EventLogger) Info(event string, fields map[string]any) {
	l.emit("info", event, fields)
}

func (l *EventLogger) Warn(event string, fields map[string]any) {
	l.emit("warn", event, fields)
}

func (l *EventLogger) Error(event string, fields map[string]any) {
	l.emit("error", event, fields)
}

func (l *EventLogger) emit(level, event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}

	if l.format == "json" {
		record := map[string]any{
			"ts":    time.Now().UTC().Format(time.RFC3339Nano),
			"level": level,
			"event": event,
		}
		for k, v := range fields {
			record[k] = v
		}
		b, err := json.Marshal(record)
		if err == nil {
			l.logger.Print(string(b))
			return
		}
	}

	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := []string{"level=" + level, "event=" + event}
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, fields[k]))
	}
	l.logger.Print(strings.Join(parts, " "))
}

type lifecycleCounters struct {
	acquireRequests atomic.Uint64
	acquireFailures atomic.Uint64
	sessionsStarted atomic.Uint64
	sessionsKilled  atomic.Uint64
}

var counters lifecycleCounters

func RecordAcquireRequest() { counters.acquireRequests.Add(1) }
func RecordAcquireFailure() { counters.acquireFailures.Add(1) }
func RecordSessionStarted() { counters.sessionsStarted.Add(1) }
func RecordSessionKilled()  { counters.sessionsKilled.Add(1) }

type LifecycleSnapshot struct {
	AcquireRequests uint64
	AcquireFailures uint64
	SessionsStarted uint64
	SessionsKilled  uint64
}

func SnapshotLifecycleCounters() LifecycleSnapshot {
	return LifecycleSnapshot{
		AcquireRequests: counters.acquireRequests.Load(),
		AcquireFailures: counters.acquireFailures.Load(),
		SessionsStarted: counters.sessionsStarted.Load(),
		SessionsKilled:  counters.sessionsKilled.Load(),
	}
}
