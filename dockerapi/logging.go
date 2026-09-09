package dockerapi

import (
	"log/slog"
	"time"
)

// Logger is the minimal structured logging surface mongotest needs. It is
// satisfied directly by *slog.Logger, and by the adapters in the log/
// sub-modules for logrus, zap and zerolog. The mongod and mongotest packages
// alias this type so callers see one Logger everywhere.
type Logger interface {
	Debug(msg string, keysAndValues ...any)
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
	Error(msg string, keysAndValues ...any)
}

var _ Logger = (*slog.Logger)(nil)

// NopLogger returns a Logger that discards everything; it is the default.
func NopLogger() Logger { return slog.New(slog.DiscardHandler) }

// now and since are indirections so tests can pin durations if needed.
var (
	now   = time.Now
	since = time.Since
)
