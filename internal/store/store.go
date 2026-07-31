// Package store holds m80's state.
//
// Two properties matter and both are easy to get wrong later if the scaffold
// does not fix them now. State is region-scoped, because the service is: an
// image created in us-east-2 is invisible in us-west-2, and an emulator that
// shares one namespace across regions will pass tests that real clients fail
// against. And every access is serialized, because the conformance runner and
// KubeMicroVM's reconcilers both issue overlapping requests, so a data race
// here surfaces as a flake nobody can reproduce.
//
// Resource types themselves belong to the issues that implement them (#8
// onward). This package deliberately stops at the generic container so those
// issues are free to shape their own structs.
package store

import "sync"

// Collection is a concurrency-safe map of resources of one kind, within one
// region. Zero value is not usable; get one from a Region.
type Collection[T any] struct {
	mu    sync.RWMutex
	items map[string]T
}

func newCollection[T any]() *Collection[T] {
	return &Collection[T]{items: map[string]T{}}
}

func (c *Collection[T]) Put(key string, v T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = v
}

func (c *Collection[T]) Get(key string) (T, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.items[key]
	return v, ok
}

func (c *Collection[T]) Delete(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; !ok {
		return false
	}
	delete(c.items, key)
	return true
}

func (c *Collection[T]) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Keys returns the keys held. Order is unspecified; callers that serialize a
// list response sort it themselves, since the wire order is a service
// behavior and not the store's to decide.
func (c *Collection[T]) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.items))
	for k := range c.items {
		out = append(out, k)
	}
	return out
}

// Update applies fn to the stored value under the write lock, so a
// read-modify-write on one resource cannot interleave with another writer.
// Reports whether the key existed.
func (c *Collection[T]) Update(key string, fn func(T) T) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	if !ok {
		return false
	}
	c.items[key] = fn(v)
	return true
}

// Region is one region's slice of the world.
type Region struct {
	Name string

	mu          sync.Mutex
	collections map[string]any
}

// Store is every region m80 has been asked about.
type Store struct {
	mu      sync.Mutex
	regions map[string]*Region
}

func New() *Store {
	return &Store{regions: map[string]*Region{}}
}

// Region returns the named region, creating it on first use. Regions are
// created lazily because the emulator learns which region it is serving from
// the sigv4 credential scope of whatever request arrives first.
func (s *Store) Region(name string) *Region {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.regions[name]
	if !ok {
		r = &Region{Name: name, collections: map[string]any{}}
		s.regions[name] = r
	}
	return r
}

// Regions returns the regions that have been touched, for the health report.
func (s *Store) Regions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.regions))
	for k := range s.regions {
		out = append(out, k)
	}
	return out
}

// CollectionOf vends the named collection of T within a region, creating it
// on first use. It is a function rather than a method because Go does not
// allow methods to introduce type parameters.
//
// Reusing a name with a different T is a programming error and panics here
// rather than silently handing back a zero collection, which would drop
// writes and be found much later.
func CollectionOf[T any](r *Region, name string) *Collection[T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.collections[name]
	if !ok {
		c := newCollection[T]()
		r.collections[name] = c
		return c
	}
	c, ok := existing.(*Collection[T])
	if !ok {
		panic("store: collection " + name + " in region " + r.Name + " already exists with a different element type")
	}
	return c
}
