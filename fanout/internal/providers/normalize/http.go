package normalize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mini-firefly/fanout/internal/adapter"
)

// MaxBodyBytes caps how much of a provider response we will read. Providers are
// hostile fakes; an unbounded read is a memory-DoS vector.
const MaxBodyBytes = 4 << 20 // 4 MiB

// DoSearch issues the POST and returns the raw body bytes, mapping transport
// and HTTP-status failures to the sentinel errors (SPEC §6.2/§5.5):
//
//	429        -> ErrRateLimited (no retry)
//	5xx        -> ErrUpstream    (retryable)
//	ctx done   -> ErrTimeout     (retryable)
//	conn reset -> ErrTimeout     (retryable; treated as transient transport)
//	other 4xx  -> ErrBadPayload  (provider rejected our request shape; no retry)
//
// Body-shape problems (truncated JSON) are the adapter's job to detect on the
// returned bytes and wrap in ErrBadPayload.
func DoSearch(ctx context.Context, client *http.Client, url, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		// A malformed URL is our own bug, not the provider's. Surface as
		// upstream so it is visible but does not trip bad_payload semantics.
		return nil, fmt.Errorf("%w: build request: %v", adapter.ErrUpstream, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		// Distinguish ctx-driven timeout/cancel from transport resets.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: %v", adapter.ErrTimeout, sanitize(err))
		}
		if isConnReset(err) {
			return nil, fmt.Errorf("%w: connection reset", adapter.ErrTimeout)
		}
		// Generic dial/transport failure (connection refused on a down
		// provider, etc.) -> treat as upstream so the breaker can react.
		return nil, fmt.Errorf("%w: %v", adapter.ErrUpstream, sanitize(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxBodyBytes))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: http 429", adapter.ErrRateLimited)
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: http %d", adapter.ErrUpstream, resp.StatusCode)
	case resp.StatusCode >= 400:
		return nil, fmt.Errorf("%w: http %d", adapter.ErrBadPayload, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("%w: read body: %v", adapter.ErrTimeout, sanitize(err))
		}
		if isConnReset(err) {
			// A socket closed mid-body is the flaky-profile "connection reset"
			// case; transient, retryable.
			return nil, fmt.Errorf("%w: connection reset reading body", adapter.ErrTimeout)
		}
		return nil, fmt.Errorf("%w: read body: %v", adapter.ErrUpstream, sanitize(err))
	}
	return raw, nil
}

// isConnReset reports whether err looks like a peer-initiated connection reset
// or an unexpected EOF mid-stream (the flaky-profile "close socket without
// response" hostility, SPEC §5.3).
func isConnReset(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection closed")
}

// sanitize strips anything that looks like an internal address from an error
// string before it can reach a ProviderResult.Error (SPEC §6.1: "no internal
// addrs"). It keeps the error class words but drops host:port fragments and
// URLs.
func sanitize(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Drop everything after the first "http://" or "https://" or "dial tcp"
	// occurrence — those carry internal hostnames/IPs.
	for _, marker := range []string{"http://", "https://", "dial tcp ", "dial tcp:"} {
		if i := strings.Index(msg, marker); i >= 0 {
			msg = strings.TrimSpace(msg[:i])
			if msg == "" {
				msg = "transport error"
			}
		}
	}
	return msg
}

// Sanitize is the exported form used by the fan-out when shaping
// ProviderResult.Error from a wrapped sentinel error.
func Sanitize(err error) string { return sanitize(err) }
