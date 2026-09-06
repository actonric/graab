package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// errForbidden marks requests that are well-formed but not permitted by policy.
var errForbidden = errors.New("forbidden")

type forbiddenError struct{ msg string }

func forbidden(msg string) error          { return &forbiddenError{msg} }
func (e *forbiddenError) Error() string   { return e.msg }
func (e *forbiddenError) Is(t error) bool { return t == errForbidden }

// resolveOutgoingPath checks that a file the model asked us to send lives
// inside one of the allowed directories. Symlinks are resolved first so a
// link inside an allowed directory cannot point at the session database.
func resolveOutgoingPath(path string, roots []string) (string, error) {
	if path == "" {
		return "", badRequest("media_path is required")
	}
	if !filepath.IsAbs(path) {
		return "", badRequest("media_path must be an absolute path")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", badRequest(fmt.Sprintf("media file not found: %s", path))
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return "", badRequest(fmt.Sprintf("media_path is not a regular file: %s", path))
	}
	real = filepath.Clean(real)
	for _, root := range roots {
		rr, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue // allowed directory does not exist yet
		}
		rr = filepath.Clean(rr)
		if real == rr || strings.HasPrefix(real, rr+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", forbidden(fmt.Sprintf("media_path is outside the directories files may be sent from (%s); copy the file there first", strings.Join(roots, ", ")))
}

// recipientAllowed enforces the optional recipient allowlist.
func recipientAllowed(to types.JID, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	want := to.ToNonAD().String()
	for _, a := range allowed {
		jid, err := parseRecipient(a)
		if err != nil {
			continue
		}
		if jid.ToNonAD().String() == want {
			return true
		}
	}
	return false
}

// rateLimiter is a sliding one-minute window over send attempts.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	times  []time.Time
	now    func() time.Time
}

func newRateLimiter(perMinute int) *rateLimiter {
	return &rateLimiter{limit: perMinute, window: time.Minute, now: time.Now}
}

// allow records an attempt and reports whether it is within the limit.
func (r *rateLimiter) allow() bool {
	if r == nil || r.limit <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	cutoff := now.Add(-r.window)
	kept := r.times[:0]
	for _, t := range r.times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	r.times = kept
	if len(r.times) >= r.limit {
		return false
	}
	r.times = append(r.times, now)
	return true
}
