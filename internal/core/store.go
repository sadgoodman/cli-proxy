package core

import (
	"sync"
	"time"
)

// Store is a bounded, concurrency safe ring of captured flows with a
// change-notification channel the UI can select on.
type Store struct {
	mu     sync.RWMutex
	flows  []*Flow
	nextID int64
	limit  int

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

// NewStore creates a store retaining at most limit flows.
func NewStore(limit int) *Store {
	if limit <= 0 {
		limit = 2000
	}
	return &Store{
		flows: make([]*Flow, 0, 256),
		limit: limit,
		subs:  make(map[chan struct{}]struct{}),
	}
}

// Add registers a new flow and returns it. The returned pointer is owned by
// the store; callers must mutate it through Update.
func (s *Store) Add(f *Flow) *Flow {
	s.mu.Lock()
	s.nextID++
	f.ID = s.nextID
	if f.Start.IsZero() {
		f.Start = time.Now()
	}
	s.flows = append(s.flows, f)
	if len(s.flows) > s.limit {
		drop := len(s.flows) - s.limit
		s.flows = append(s.flows[:0], s.flows[drop:]...)
	}
	s.mu.Unlock()
	s.Notify()
	return f
}

// Update mutates the flow with the given id under the store lock.
func (s *Store) Update(id int64, fn func(*Flow)) {
	s.mu.Lock()
	for _, f := range s.flows {
		if f.ID == id {
			fn(f)
			break
		}
	}
	s.mu.Unlock()
	s.Notify()
}

// Get returns a copy of the flow with the given id.
func (s *Store) Get(id int64) (*Flow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, f := range s.flows {
		if f.ID == id {
			return f.Clone(), true
		}
	}
	return nil, false
}

// Snapshot returns copies of all retained flows in arrival order.
func (s *Store) Snapshot() []*Flow {
	s.mu.RLock()
	out := make([]*Flow, len(s.flows))
	for i, f := range s.flows {
		out[i] = f.Clone()
	}
	s.mu.RUnlock()
	return out
}

// Len returns the number of retained flows.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.flows)
}

// Clear removes every retained flow.
func (s *Store) Clear() {
	s.mu.Lock()
	s.flows = s.flows[:0]
	s.mu.Unlock()
	s.Notify()
}

// Subscribe returns a channel that receives a signal whenever the store
// changes, plus an unsubscribe function.
func (s *Store) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		delete(s.subs, ch)
		s.subMu.Unlock()
	}
}

// Notify wakes every subscriber without blocking.
func (s *Store) Notify() {
	s.subMu.Lock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	s.subMu.Unlock()
}
