package dockerapi

import (
	"encoding/json/v2"
	"sync"
)

// syncMutex is an alias so test helpers read naturally.
type syncMutex = sync.Mutex

// decodeBodyBytes decodes a recorded request body into v.
func decodeBodyBytes(b []byte, v any) error { return json.Unmarshal(b, v) }

// recordLogger captures Logger calls so tests can assert on them.
type recordLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

// logEntry is one captured log call with its key/value pairs as a map.
type logEntry struct {
	level string
	msg   string
	kv    map[string]any
}

func (l *recordLogger) record(level, msg string, kv []any) {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{level: level, msg: msg, kv: m})
}

func (l *recordLogger) Debug(msg string, kv ...any) { l.record("debug", msg, kv) }
func (l *recordLogger) Info(msg string, kv ...any)  { l.record("info", msg, kv) }
func (l *recordLogger) Warn(msg string, kv ...any)  { l.record("warn", msg, kv) }
func (l *recordLogger) Error(msg string, kv ...any) { l.record("error", msg, kv) }
