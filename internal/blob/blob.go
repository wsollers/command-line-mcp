// Package blob is an in-memory, server-side holding area for binary
// payloads too large to inline into an MCP tool result as base64 — see
// docs/api-spec.md §3.2 and §18. read_file returns a handle instead of
// inline base64 once content exceeds a size threshold; write_file (and,
// later, copy-from-elsewhere operations) accept that handle in place of
// inline content, so a large value never has to round-trip through the
// caller's context to move from one read/write call to another.
//
// Handles are opaque, random, and expire after a fixed TTL from the
// moment they're issued (Get does not extend it — a handle is meant to be
// consumed promptly within one task, not held indefinitely). Nothing about
// a handle string reveals anything about its content or origin path.
package blob

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var (
	// ErrNotFound means the handle was never issued by this store
	// instance, or was already released/swept.
	ErrNotFound = errors.New("blob not found")

	// ErrExpired means the handle was issued but its TTL has elapsed.
	// Distinct from ErrNotFound so a caller can give a more specific
	// message ("that blob is gone, read the file again" vs "unknown
	// handle").
	ErrExpired = errors.New("blob expired")
)

type entry struct {
	data      []byte
	mediaType string
	expiresAt time.Time
}

// Store holds blobs in memory for up to ttl each. The zero value is not
// usable; construct with New.
type Store struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]entry
	now     func() time.Time // injectable for tests; defaults to time.Now
}

// New returns a Store whose entries expire ttl after being Put.
func New(ttl time.Duration) *Store {
	return &Store{ttl: ttl, entries: make(map[string]entry), now: time.Now}
}

// Put stores data under a freshly generated handle, valid for this
// Store's TTL from now. mediaType is advisory (e.g. from a content
// sniff) and is returned alongside the data on Get; pass "" if unknown.
func (s *Store) Put(data []byte, mediaType string) (handle string, err error) {
	h, err := randomHandle()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.entries[h] = entry{data: data, mediaType: mediaType, expiresAt: s.now().Add(s.ttl)}
	return h, nil
}

// Get returns the bytes and media type stored under handle. Returns
// ErrNotFound if handle was never issued (or was released/swept), or
// ErrExpired if it was issued but its TTL has since elapsed — an expired
// entry is removed as a side effect of this check.
func (s *Store) Get(handle string) ([]byte, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[handle]
	if !ok {
		return nil, "", ErrNotFound
	}
	if s.now().After(e.expiresAt) {
		delete(s.entries, handle)
		return nil, "", ErrExpired
	}
	return e.data, e.mediaType, nil
}

// Release removes handle before its TTL elapses, freeing the bytes early.
// Reports whether a live (not already expired or absent) entry was
// actually removed; not an error to release an unknown or already-expired
// handle.
func (s *Store) Release(handle string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[handle]
	if !ok {
		return false
	}
	delete(s.entries, handle)
	return !s.now().After(e.expiresAt)
}

// Len reports the number of live entries, for tests.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// sweepLocked removes every expired entry. Called opportunistically from
// Put so a long-running server doesn't accumulate abandoned entries
// without needing a background goroutine; s.mu must already be held.
func (s *Store) sweepLocked() {
	now := s.now()
	for h, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, h)
		}
	}
}

func randomHandle() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "blob_" + hex.EncodeToString(b), nil
}
