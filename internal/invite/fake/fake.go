// Package fake is an in-memory invite.Inviter for tests.
package fake

import (
	"context"
	"sync"
)

// Inviter records invitations and can be told to fail.
type Inviter struct {
	mu    sync.Mutex
	calls []string
	err   error
}

// Invite records username, or returns the error set with SetError.
func (f *Inviter) Invite(_ context.Context, username string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, username)
	return f.err
}

// SetError makes every following Invite fail with err (nil to succeed again).
func (f *Inviter) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// Calls returns the usernames Invite was called with, including failed calls.
func (f *Inviter) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// ResetCalls forgets the recorded calls.
func (f *Inviter) ResetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}
