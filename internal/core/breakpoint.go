package core

import (
	"sync"
	"sync/atomic"
	"time"
)

// Options holds runtime toggles shared between the UI and the proxy engine.
type Options struct {
	BreakRequests  atomic.Bool
	BreakResponses atomic.Bool
	AutoJump       atomic.Bool
	Throttle       atomic.Int64 // milliseconds of artificial latency, 0 = off
}

// DefaultOptions returns the default runtime configuration.
func DefaultOptions() *Options {
	o := &Options{}
	o.AutoJump.Store(true)
	return o
}

// Phase identifies which half of an exchange a breakpoint pauses.
type Phase string

const (
	PhaseRequest  Phase = "request"
	PhaseResponse Phase = "response"
)

// Verdict is what the operator decided at a breakpoint.
type Verdict struct {
	Drop   bool   // abort the exchange
	Raw    string // edited raw HTTP message (empty = keep original)
	Reason string
}

// Breakpoint is a paused exchange waiting for operator input.
type Breakpoint struct {
	ID      int64
	Flow    *Flow
	Phase   Phase
	Raw     string
	Created time.Time

	ch chan Verdict
}

// Breaker queues breakpoints and blocks the proxy goroutine until the
// operator releases them.
type Breaker struct {
	mu      sync.Mutex
	pending []*Breakpoint
	nextID  int64
	notify  chan struct{}
}

// NewBreaker creates an empty breakpoint queue.
func NewBreaker() *Breaker {
	return &Breaker{notify: make(chan struct{}, 1)}
}

// Hold enqueues the breakpoint and blocks until it is released. The returned
// verdict tells the proxy what to do next.
func (b *Breaker) Hold(bp *Breakpoint) Verdict {
	b.mu.Lock()
	b.nextID++
	bp.ID = b.nextID
	bp.Created = time.Now()
	if bp.ch == nil {
		bp.ch = make(chan Verdict, 1)
	}
	b.pending = append(b.pending, bp)
	b.mu.Unlock()
	b.wake()
	return <-bp.ch
}

// Pending returns a snapshot of the queued breakpoints.
func (b *Breaker) Pending() []*Breakpoint {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*Breakpoint, len(b.pending))
	copy(out, b.pending)
	return out
}

// Len returns the number of queued breakpoints.
func (b *Breaker) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}

// Submit releases the breakpoint with the given id.
func (b *Breaker) Submit(id int64, v Verdict) bool {
	b.mu.Lock()
	for i, bp := range b.pending {
		if bp.ID == id {
			b.pending = append(b.pending[:i], b.pending[i+1:]...)
			b.mu.Unlock()
			bp.ch <- v
			b.wake()
			return true
		}
	}
	b.mu.Unlock()
	return false
}

// ReleaseAll drops every queued breakpoint, letting the proxy continue.
func (b *Breaker) ReleaseAll() {
	b.mu.Lock()
	pending := b.pending
	b.pending = nil
	b.mu.Unlock()
	for _, bp := range pending {
		bp.ch <- Verdict{}
	}
	b.wake()
}

// Changes returns a channel signalled whenever the queue changes.
func (b *Breaker) Changes() <-chan struct{} { return b.notify }

func (b *Breaker) wake() {
	select {
	case b.notify <- struct{}{}:
	default:
	}
}
