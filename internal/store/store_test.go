package store

import (
	"fmt"
	"sync"
	"testing"
)

type image struct {
	Name  string
	State string
}

// The property that matters most: an image in one region must be invisible in
// another. An emulator sharing one namespace passes tests real clients fail.
func TestRegionsAreIsolated(t *testing.T) {
	s := New()
	east := CollectionOf[image](s.Region("us-east-2"), "images")
	west := CollectionOf[image](s.Region("us-west-2"), "images")

	east.Put("img1", image{Name: "img1", State: "CREATED"})
	if _, ok := west.Get("img1"); ok {
		t.Error("image created in us-east-2 is visible in us-west-2")
	}
	if west.Len() != 0 {
		t.Errorf("us-west-2 has %d images", west.Len())
	}
	if _, ok := east.Get("img1"); !ok {
		t.Error("image missing from the region that created it")
	}
}

func TestRegionAndCollectionAreStable(t *testing.T) {
	s := New()
	if s.Region("us-east-2") != s.Region("us-east-2") {
		t.Error("Region returned a different region for the same name")
	}
	r := s.Region("us-east-2")
	if CollectionOf[image](r, "images") != CollectionOf[image](r, "images") {
		t.Error("CollectionOf returned a different collection for the same name")
	}
	// Different names in one region are different collections.
	if any(CollectionOf[image](r, "images")) == any(CollectionOf[image](r, "vms")) {
		t.Error("distinct collection names share storage")
	}
}

// Silently handing back an empty collection on a type mismatch would drop
// every write and surface much later as missing state.
func TestCollectionOfPanicsOnTypeMismatch(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("reusing a collection name with a different type did not panic")
		}
	}()
	r := New().Region("us-east-2")
	CollectionOf[image](r, "things")
	CollectionOf[string](r, "things")
}

func TestDeleteReportsWhetherItExisted(t *testing.T) {
	c := CollectionOf[image](New().Region("us-east-2"), "images")
	c.Put("img1", image{Name: "img1"})
	if !c.Delete("img1") {
		t.Error("Delete reported miss on a present key")
	}
	if c.Delete("img1") {
		t.Error("Delete reported hit on an absent key")
	}
}

func TestUpdateAppliesUnderLock(t *testing.T) {
	c := CollectionOf[image](New().Region("us-east-2"), "images")
	c.Put("img1", image{Name: "img1", State: "CREATING"})
	if !c.Update("img1", func(i image) image { i.State = "CREATED"; return i }) {
		t.Fatal("Update reported miss on a present key")
	}
	got, _ := c.Get("img1")
	if got.State != "CREATED" {
		t.Errorf("state %q", got.State)
	}
	if c.Update("ghost", func(i image) image { return i }) {
		t.Error("Update reported hit on an absent key")
	}
}

func TestKeysReturnsEverything(t *testing.T) {
	c := CollectionOf[image](New().Region("us-east-2"), "images")
	for _, n := range []string{"a", "b", "c"} {
		c.Put(n, image{Name: n})
	}
	if got := c.Keys(); len(got) != 3 {
		t.Errorf("keys %v", got)
	}
}

// The conformance runner and a reconciler both issue overlapping requests, so
// this runs under -race in CI where it earns its keep.
func TestConcurrentAccessIsSafe(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Deliberately hammering region and collection creation too,
			// since those lazily construct shared state.
			c := CollectionOf[image](s.Region(fmt.Sprintf("region-%d", i%2)), "images")
			for j := range 50 {
				key := fmt.Sprintf("img-%d-%d", i, j)
				c.Put(key, image{Name: key, State: "CREATING"})
				c.Update(key, func(im image) image { im.State = "CREATED"; return im })
				c.Get(key)
				c.Keys()
				c.Delete(key)
			}
		}(i)
	}
	wg.Wait()
	for _, region := range s.Regions() {
		if n := CollectionOf[image](s.Region(region), "images").Len(); n != 0 {
			t.Errorf("region %s kept %d images", region, n)
		}
	}
	if len(s.Regions()) != 2 {
		t.Errorf("regions %v", s.Regions())
	}
}
