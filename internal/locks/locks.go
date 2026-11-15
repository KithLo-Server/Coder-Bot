// Package locks provides reentrant per-repository locks.
package locks

import (
	"context"
	"sync"
)

type heldKey struct{}

type heldSet struct {
	mu     sync.Mutex
	counts map[string]int
}

type entry struct {
	sem chan struct{}
}

// Manager owns one lock per repository.
type Manager struct {
	mu    sync.Mutex
	locks map[string]*entry
}

// NewManager returns an empty lock manager.
func NewManager() *Manager {
	return &Manager{locks: make(map[string]*entry)}
}

// Acquire locks a repository. The returned context carries the reentrancy
// marker and must be used for nested operations on the same repository.
func (m *Manager) Acquire(ctx context.Context, name string) (context.Context, func(), error) {
	held, _ := ctx.Value(heldKey{}).(*heldSet)
	if held != nil {
		held.mu.Lock()
		if held.counts[name] > 0 {
			held.counts[name]++
			held.mu.Unlock()
			return ctx, func() { m.releaseHeld(held, name) }, nil
		}
		held.mu.Unlock()
	}

	lock := m.entryFor(name)
	select {
	case lock.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx, nil, ctx.Err()
	}

	if held == nil {
		held = &heldSet{counts: make(map[string]int)}
		ctx = context.WithValue(ctx, heldKey{}, held)
	}
	held.mu.Lock()
	held.counts[name]++
	held.mu.Unlock()

	return ctx, func() { m.releaseHeld(held, name) }, nil
}

func (m *Manager) entryFor(name string) *entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.locks[name]
	if !ok {
		lock = &entry{sem: make(chan struct{}, 1)}
		m.locks[name] = lock
	}
	return lock
}

func (m *Manager) releaseHeld(held *heldSet, name string) {
	held.mu.Lock()
	held.counts[name]--
	remaining := held.counts[name]
	if remaining <= 0 {
		delete(held.counts, name)
	}
	held.mu.Unlock()
	if remaining <= 0 {
		lock := m.entryFor(name)
		<-lock.sem
	}
}
