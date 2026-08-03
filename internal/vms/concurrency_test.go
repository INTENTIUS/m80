package vms

import (
	"sync"
	"testing"
	"time"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

// The rest of this package's tests use clock.Test, whose callbacks run on the
// test goroutine. That is what makes them deterministic, and it is also why
// they cannot see the collision this package's doc comment describes:
//
//	The transitions run on clock callbacks, which under clock.Real are
//	separate goroutines, while handlers read the same fields to build a
//	response. Tests use clock.Test, whose callbacks run on the test
//	goroutine, so -race cannot see that collision; it is real in the
//	shipped binary regardless.
//
// So `go test -race ./internal/vms` was passing over the concurrency it was
// meant to be checking. These tests run against clock.Real, where a transition
// really is a separate goroutine, and hammer every exported entry point at
// once. They are non-deterministic by design: they do not assert an outcome,
// they give the race detector something to look at.

func realService() (*Service, *store.Store) {
	st := store.New()
	// Short enough that a transition lands inside the test's window, long
	// enough that a caller is genuinely mid-flight when it does.
	return NewService(clock.Real{}, st, 2*time.Millisecond), st
}

func idlePolicy(maxIdle, suspended int) *IdlePolicy {
	return &IdlePolicy{AutoResumeEnabled: true, MaxIdleDurationSeconds: &maxIdle, SuspendedDurationSeconds: &suspended}
}

// TestConcurrentCallersAgainstRealTransitions drives one VM from every
// direction while its own transitions fire underneath.
func TestConcurrentCallersAgainstRealTransitions(t *testing.T) {
	svc, _ := realService()
	vm := svc.Run("us-east-1", "arn:image", "1.0", 2048, idlePolicy(0, 0))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	spin := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					fn()
				}
			}
		}()
	}

	spin(func() { svc.Snapshot(vm) })
	spin(func() { svc.Suspend(vm) })
	spin(func() { svc.Resume(vm) })
	spin(func() { svc.Touch(vm) })
	spin(func() { svc.Snapshots("us-east-1") })
	spin(func() { svc.Allocated("us-east-1") })
	spin(func() { svc.HasRunningVMs("us-east-1", "arn:image") })
	spin(func() { svc.Status("us-east-1", vm.ID) })

	time.Sleep(150 * time.Millisecond)
	close(stop)
	wg.Wait()

	// No assertion on the final state: which of suspend, resume and the idle
	// timer won is genuinely undefined. What is asserted is that it is one of
	// the states the machine has.
	final := svc.Snapshot(vm)
	switch final.State {
	case StatePending, StateRunning, StateSuspending, StateSuspended, StateTerminating, StateTerminated:
	default:
		t.Fatalf("VM settled outside the state machine: %q", final.State)
	}
}

// TestConcurrentRunsAgainstRealClock creates VMs from many goroutines while
// readers walk the collection. Run publishes into the store before it finishes
// reading the VM it just created, which is the narrowest window in the package.
func TestConcurrentRunsAgainstRealTransitions(t *testing.T) {
	svc, _ := realService()

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				svc.Snapshots("us-east-1")
				svc.Allocated("us-east-1")
				svc.List("us-east-1")
			}
		}
	}()

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vm := svc.Run("us-east-1", "arn:image", "1.0", 2048, idlePolicy(0, 0))
			svc.Touch(vm)
			svc.Suspend(vm)
			svc.Snapshot(vm)
			svc.Terminate(vm)
		}()
	}

	// Let the transitions land while the reader is still walking.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	if _, count := svc.Allocated("us-east-1"); count > 32 {
		t.Fatalf("Allocated counted %d VMs, more than were created", count)
	}
}

// TestTagsAreNotHandedOutLive guards the aliasing this review flagged: Tags
// returns the service's own map, so a caller that writes to it writes to VM
// state with no lock held. A copy costs nothing at this size.
func TestTagsAreNotHandedOutLive(t *testing.T) {
	svc, _ := realService()
	vm := svc.Run("us-east-1", "arn:image", "1.0", 2048, nil)
	arn := "arn:aws:lambda:us-east-1:000000000000:microvm:" + vm.ID

	svc.SetTags("us-east-1", arn, map[string]string{"owner": "platform"})
	got, ok := svc.Tags("us-east-1", arn)
	if !ok {
		t.Fatal("Tags did not resolve the VM")
	}

	got["owner"] = "mutated-through-the-returned-map"

	after, _ := svc.Tags("us-east-1", arn)
	if after["owner"] != "platform" {
		t.Errorf("a caller mutated VM tags through the map Tags returned: owner = %q", after["owner"])
	}
}
