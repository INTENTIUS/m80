// Package clock is the seam that makes m80's state machines testable.
//
// Every transient state in the service — a build running, a VM suspending,
// the eight-hour session cap — is a delay. Wiring those to wall time would
// make the emulator's own tests take as long as the behavior they cover, and
// would make a suspend-after-fifteen-minutes policy effectively untestable.
// Transitions are therefore scheduled against a Clock, and tests swap in one
// they can advance by hand. This is the mudflaps pattern, which held there.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock is the subset of time the emulator is allowed to touch. Nothing in
// m80 calls time.Now directly.
type Clock interface {
	Now() time.Time
	// After schedules fn to run once d has elapsed. Callers use it for state
	// transitions, so ordering at equal deadlines must be stable.
	After(d time.Duration, fn func())
}

// Real is wall-clock time, used by the running binary.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

func (Real) After(d time.Duration, fn func()) {
	time.AfterFunc(d, fn)
}

type timer struct {
	deadline time.Time
	seq      uint64
	fn       func()
}

// Test is a clock that only moves when a test moves it. The zero value is not
// usable; call NewTest.
type Test struct {
	mu     sync.Mutex
	now    time.Time
	seq    uint64
	timers []timer
}

// NewTest returns a clock parked at start.
func NewTest(start time.Time) *Test {
	return &Test{now: start}
}

func (c *Test) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Test) After(d time.Duration, fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	c.timers = append(c.timers, timer{deadline: c.now.Add(d), seq: c.seq, fn: fn})
}

// Advance moves time forward and runs everything that comes due, in deadline
// order. Callbacks run outside the lock so a transition may schedule the next
// one, and anything that becomes due inside the window runs in the same
// Advance — a build finishing must be able to settle its image without the
// test having to know how many hops the chain takes.
//
// Time steps to each timer's deadline before that timer runs, rather than
// jumping straight to the target. A callback that schedules the next hop is
// therefore measuring from the instant it conceptually fired, which is what
// time.AfterFunc does; jumping first would push every chained transition one
// full window into the future.
func (c *Test) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()

	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool {
			if c.timers[i].deadline.Equal(c.timers[j].deadline) {
				return c.timers[i].seq < c.timers[j].seq
			}
			return c.timers[i].deadline.Before(c.timers[j].deadline)
		})
		var due func()
		for i, t := range c.timers {
			if !t.deadline.After(target) {
				due = t.fn
				c.now = t.deadline
				c.timers = append(c.timers[:i], c.timers[i+1:]...)
				break
			}
		}
		if due == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		due()
	}
}

// Pending reports how many timers are still scheduled, so a test can assert a
// state machine came to rest rather than merely looking settled.
func (c *Test) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}
