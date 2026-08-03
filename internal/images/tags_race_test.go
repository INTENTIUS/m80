package images

import (
	"sync"
	"testing"
	"time"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

// SetTags writes img.Tags under the service mutex and Tags read it without
// taking the mutex at all, so a concurrent TagResource and ListTags against
// the same image raced on the map reference. Nothing in the suite exercised
// tag reads and writes at the same time, so -race had nothing to look at.
func TestTagReadsAndWritesDoNotRace(t *testing.T) {
	st := store.New()
	svc := NewService(clock.Real{}, st, time.Millisecond)
	svc.Create("us-east-1", "img", Spec{})
	arn := "arn:aws:lambda:us-east-1:000000000000:microvm-image:img"

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				svc.SetTags("us-east-1", arn, map[string]string{"n": string(rune('a' + i%26))})
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if tags, ok := svc.Tags("us-east-1", arn); ok {
					for k := range tags {
						_ = tags[k]
					}
				}
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
