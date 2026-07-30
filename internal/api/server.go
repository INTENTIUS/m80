package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"

	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/store"
)

// Server is the emulator's HTTP surface. Resource packages register their
// handlers against it; whatever is unregistered answers 501.
type Server struct {
	Clock clock.Clock
	Store *store.Store

	mu       sync.RWMutex
	handlers map[string]Handler // keyed by operation name
	mux      *http.ServeMux
	version  string
}

// NewServer wires the route table. Every operation is routed immediately, so
// an unimplemented one is a deliberate 501 rather than an accidental 404.
func NewServer(c clock.Clock, s *store.Store, version string) *Server {
	srv := &Server{
		Clock:    c,
		Store:    s,
		handlers: map[string]Handler{},
		mux:      http.NewServeMux(),
		version:  version,
	}
	for _, r := range Routes {
		srv.mux.HandleFunc(r.Method+" "+r.Pattern, srv.dispatch(r.Operation))
	}
	srv.mux.HandleFunc("GET /_m80/health", srv.health)
	return srv
}

// Register attaches an implementation to an operation. Registering an unknown
// operation panics: a typo would otherwise leave the real route on 501 while
// the handler sat unreachable.
func (s *Server) Register(operation string, h Handler) {
	known := false
	for _, r := range Routes {
		if r.Operation == operation {
			known = true
			break
		}
	}
	if !known {
		panic("api: no route for operation " + operation)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[operation] = h
}

// Implemented lists the operations that have handlers, sorted.
func (s *Server) Implemented() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.handlers))
	for op := range s.handlers {
		out = append(out, op)
	}
	sort.Strings(out)
	return out
}

func (s *Server) dispatch(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		h, ok := s.handlers[operation]
		s.mu.RUnlock()
		if !ok {
			writeJSON(w, http.StatusNotImplemented, map[string]any{
				"__type":  "NotImplemented",
				"message": operation + " is routed but not implemented yet",
			})
			return
		}
		h(w, r)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// health reports coverage against the operation inventory, which is how a
// user of the container tells at a glance how much of the service this build
// actually emulates.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	impl := s.Implemented()
	pending := make([]string, 0, len(Routes)-len(impl))
	done := map[string]bool{}
	for _, op := range impl {
		done[op] = true
	}
	for _, rt := range Routes {
		if !done[rt.Operation] {
			pending = append(pending, rt.Operation)
		}
	}
	sort.Strings(pending)
	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.version,
		"coverage": map[string]any{
			"implemented":       len(impl),
			"total":             len(Routes),
			"operations":        impl,
			"notImplementedYet": pending,
		},
		"regions": s.Store.Regions(),
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
