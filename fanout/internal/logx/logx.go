// Package logx emits structured JSON logs to stdout (SPEC §12.4). Every line
// carries ts, level, service, request_id (echoed from the §9 body), provider
// where applicable, and msg, so a single grep traces a request across services.
package logx

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

const service = "fanout"

// Logger writes JSON log lines. It is safe for concurrent use.
type Logger struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// New returns a logger writing to stdout.
func New() *Logger {
	return &Logger{w: os.Stdout, now: time.Now}
}

// NewWith returns a logger writing to w with an injectable clock (tests).
func NewWith(w io.Writer, now func() time.Time) *Logger {
	if now == nil {
		now = time.Now
	}
	return &Logger{w: w, now: now}
}

// Fields is an ordered-ish bag of extra structured fields.
type Fields map[string]interface{}

func (l *Logger) log(level, requestID, provider, msg string, extra Fields) {
	rec := make(map[string]interface{}, len(extra)+6)
	for k, v := range extra {
		rec[k] = v
	}
	rec["ts"] = l.now().UTC().Format(time.RFC3339Nano)
	rec["level"] = level
	rec["service"] = service
	rec["msg"] = msg
	if requestID != "" {
		rec["request_id"] = requestID
	}
	if provider != "" {
		rec["provider"] = provider
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

// Info logs at info level.
func (l *Logger) Info(requestID, provider, msg string, extra Fields) {
	l.log("info", requestID, provider, msg, extra)
}

// Warn logs at warn level.
func (l *Logger) Warn(requestID, provider, msg string, extra Fields) {
	l.log("warn", requestID, provider, msg, extra)
}

// Error logs at error level.
func (l *Logger) Error(requestID, provider, msg string, extra Fields) {
	l.log("error", requestID, provider, msg, extra)
}
