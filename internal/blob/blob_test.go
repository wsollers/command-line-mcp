package blob

import (
	"errors"
	"testing"
	"time"
)

func TestPutGetRoundTrip(t *testing.T) {
	s := New(time.Minute)
	handle, err := s.Put([]byte("hello"), "text/plain")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if handle == "" {
		t.Fatal("Put returned an empty handle")
	}
	data, mediaType, err := s.Get(handle)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("data = %q, want %q", data, "hello")
	}
	if mediaType != "text/plain" {
		t.Errorf("mediaType = %q, want %q", mediaType, "text/plain")
	}
}

func TestGetUnknownHandle(t *testing.T) {
	s := New(time.Minute)
	_, _, err := s.Get("blob_does_not_exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestHandlesAreDistinctAndUnguessable(t *testing.T) {
	s := New(time.Minute)
	h1, err := s.Put([]byte("a"), "")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := s.Put([]byte("b"), "")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Errorf("two Put calls returned the same handle: %q", h1)
	}
}

func TestExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := New(10 * time.Second)
	s.now = func() time.Time { return now }

	handle, err := s.Put([]byte("payload"), "")
	if err != nil {
		t.Fatal(err)
	}

	// Still valid just before expiry.
	now = now.Add(9 * time.Second)
	if _, _, err := s.Get(handle); err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}

	// Expired just after.
	now = now.Add(2 * time.Second)
	_, _, err = s.Get(handle)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v, want ErrExpired", err)
	}

	// And it's gone for good — a second Get reports not-found, not
	// expired-again, since the expired entry was removed as a side
	// effect of the first Get.
	_, _, err = s.Get(handle)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err after re-Get = %v, want ErrNotFound", err)
	}
}

func TestSweepOnPutRemovesExpiredEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := New(10 * time.Second)
	s.now = func() time.Time { return now }

	if _, err := s.Put([]byte("old"), ""); err != nil {
		t.Fatal(err)
	}
	if got := s.Len(); got != 1 {
		t.Fatalf("Len after first Put = %d, want 1", got)
	}

	now = now.Add(time.Hour) // well past the 10s TTL
	if _, err := s.Put([]byte("new"), ""); err != nil {
		t.Fatal(err)
	}
	// The expired first entry should have been swept as a side effect
	// of the second Put, leaving only the new one.
	if got := s.Len(); got != 1 {
		t.Errorf("Len after sweep = %d, want 1 (expired entry not swept)", got)
	}
}

func TestRelease(t *testing.T) {
	s := New(time.Minute)
	handle, err := s.Put([]byte("x"), "")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Release(handle) {
		t.Error("Release on a live handle returned false")
	}
	if _, _, err := s.Get(handle); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Release: err = %v, want ErrNotFound", err)
	}
	// Releasing again (or an unknown handle) is not an error, just false.
	if s.Release(handle) {
		t.Error("second Release on an already-released handle returned true")
	}
	if s.Release("blob_never_issued") {
		t.Error("Release on an unknown handle returned true")
	}
}

func TestReleaseExpiredReportsFalse(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := New(10 * time.Second)
	s.now = func() time.Time { return now }

	handle, err := s.Put([]byte("x"), "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if s.Release(handle) {
		t.Error("Release on an already-expired (but not yet swept) handle returned true, want false")
	}
}
