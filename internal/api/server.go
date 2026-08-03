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

	// Intercept is consulted before the route table and reports whether it
	// served the request. The per-VM endpoint (#12) uses it: a VM's endpoint
	// is a different host answered by the same process, and ServeMux host
	// patterns cannot carry a wildcard, so it cannot be expressed as a route.
	Intercept func(http.ResponseWriter, *http.Request) bool

	// Gate is consulted before every operation handler, with the operation
	// name, and reports whether it answered the request itself. Throttling
	// (#15) uses it: a rate limit applies across the whole surface rather
	// than inside any one resource package, and the throttle's wire shape
	// depends on which service the operation belongs to.
	Gate func(operation string, w http.ResponseWriter, r *http.Request) bool

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

// Handle attaches a raw handler to a mux pattern, for the non-service surface
// under /_m80/. Operations go through Register instead — they are routed from
// the Routes table so an unimplemented one is a deliberate 501, and nothing
// outside that table should be able to claim an operation's path.
func (s *Server) Handle(pattern string, h http.HandlerFunc) {
	s.mux.HandleFunc(pattern, h)
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
		// The gate runs after the 501 check so an unimplemented operation
		// still reports as unimplemented rather than as throttled, which the
		// conformance runner distinguishes.
		if s.Gate != nil && s.Gate(operation, w, r) {
			return
		}
		h(w, r)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Intercept != nil && s.Intercept(w, r) {
		return
	}
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

// WriteJSON writes a success response. Resource packages use it so the
// content type and encoding stay in one place.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteError writes an error response with the modeled error type in the
// header the SDKs read.
//
// The type rides X-Amzn-Errortype and not the body: the live service does not
// put __type in these bodies, the recorded fixtures confirm it, and an
// emulator that adds one diverges on every error case. Bodies are per-error
// and passed in by the caller, since their members differ.
func WriteError(w http.ResponseWriter, status int, errorType string, body any) {
	w.Header().Set("X-Amzn-Errortype", errorType)
	WriteJSON(w, status, body)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	WriteJSON(w, status, body)
}
