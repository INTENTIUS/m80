package clock

import (
	"testing"
	"time"
)

var epoch = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

func TestTestClockDoesNotMoveOnItsOwn(t *testing.T) {
	c := NewTest(epoch)
	fired := false
	c.After(time.Second, func() { fired = true })
	if !c.Now().Equal(epoch) {
		t.Errorf("now moved to %v", c.Now())
	}
	if fired {
		t.Error("timer fired without an Advance")
	}
}

func TestAdvanceRunsDueTimersInOrder(t *testing.T) {
	c := NewTest(epoch)
	var order []string
	c.After(3*time.Second, func() { order = append(order, "third") })
	c.After(time.Second, func() { order = append(order, "first") })
	c.After(2*time.Second, func() { order = append(order, "second") })

	c.Advance(2 * time.Second)
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("ran %v, want first then second", order)
	}
	if c.Pending() != 1 {
		t.Errorf("pending %d, want the un-due timer to remain", c.Pending())
	}

	c.Advance(time.Second)
	if len(order) != 3 || order[2] != "third" {
		t.Fatalf("ran %v", order)
	}
	if c.Pending() != 0 {
		t.Errorf("pending %d, want 0", c.Pending())
	}
}

// Equal deadlines must not reorder, or a build that schedules two follow-ups
// at the same instant settles differently run to run.
func TestEqualDeadlinesKeepScheduleOrder(t *testing.T) {
	c := NewTest(epoch)
	var order []string
	for _, name := range []string{"a", "b", "c", "d"} {
		c.After(time.Second, func() { order = append(order, name) })
	}
	c.Advance(time.Second)
	if len(order) != 4 || order[0] != "a" || order[3] != "d" {
		t.Fatalf("ran %v, want a b c d", order)
	}
}

// A state machine hop schedules the next hop. One Advance past both deadlines
// must run the whole chain, or every test has to know the hop count.
func TestAdvanceDrainsChainedTimers(t *testing.T) {
	c := NewTest(epoch)
	var states []string
	c.After(time.Second, func() {
		states = append(states, "IN_PROGRESS")
		c.After(time.Second, func() {
			states = append(states, "SUCCESSFUL")
		})
	})

	c.Advance(5 * time.Second)
	if len(states) != 2 || states[1] != "SUCCESSFUL" {
		t.Fatalf("states %v, want the chain to settle in one Advance", states)
	}
	if c.Pending() != 0 {
		t.Errorf("pending %d, want the machine at rest", c.Pending())
	}
}

// A chained timer scheduled beyond the advance window must not fire early.
func TestChainedTimerBeyondWindowStaysPending(t *testing.T) {
	c := NewTest(epoch)
	var states []string
	c.After(time.Second, func() {
		states = append(states, "SUSPENDING")
		c.After(time.Hour, func() { states = append(states, "TERMINATED") })
	})

	c.Advance(2 * time.Second)
	if len(states) != 1 {
		t.Fatalf("states %v, want only the first hop", states)
	}
	if c.Pending() != 1 {
		t.Errorf("pending %d, want the far timer still scheduled", c.Pending())
	}
}

func TestRealClockAdvances(t *testing.T) {
	var c Clock = Real{}
	before := c.Now()
	done := make(chan struct{})
	c.After(time.Millisecond, func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("real clock timer never fired")
	}
	if !c.Now().After(before) && c.Now().Equal(before) {
		t.Error("real clock did not advance")
	}
}
