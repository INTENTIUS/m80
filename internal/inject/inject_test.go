package inject

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/connectors"
	"github.com/intentius/m80/internal/images"
	"github.com/intentius/m80/internal/store"
)

func enabled(t *testing.T) *api.Server {
	t.Helper()
	st := store.New()
	clk := clock.Real{}
	srv := api.NewServer(clk, st, "test")
	Register(srv, &Service{
		Images:     images.NewService(clk, st, time.Millisecond),
		Connectors: connectors.NewService(clk, st, time.Millisecond),
	})
	return srv
}

func post(t *testing.T, srv *api.Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest("POST", Path, strings.NewReader(body)))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return out
}

func TestArmsABuildFailure(t *testing.T) {
	rec := post(t, enabled(t), `{"target":"build","name":"img"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	// The field a consumer asserts on: a state m80 reached by itself never
	// carries it, so an injected FAILED cannot be mistaken for a real one.
	if body["injected"] != true {
		t.Errorf("injected = %v, want true", body["injected"])
	}
	if !strings.Contains(body["armed"].(string), "img") {
		t.Errorf("armed does not name the image: %q", body["armed"])
	}
}

func TestArmsAConnectorFailurePerReasonCode(t *testing.T) {
	// Every code, because the point of the seven is that a consumer's error
	// handling can be exercised against each — one working code would not
	// tell us the others are reachable through this surface.
	for _, code := range connectors.ReasonCodes {
		rec := post(t, enabled(t), `{"target":"connector","name":"conn","reasonCode":"`+code+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200 (%s)", code, rec.Code, rec.Body.String())
		}
		if got := decode(t, rec)["reasonCode"]; got != code {
			t.Errorf("reasonCode = %v, want %s", got, code)
		}
	}
}

func TestRejectsAMadeUpReasonCode(t *testing.T) {
	rec := post(t, enabled(t), `{"target":"connector","name":"conn","reasonCode":"NotAReasonCode"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	// The message has to list the real ones, or the caller has to go read
	// the model to find out what it should have said.
	if msg := decode(t, rec)["message"].(string); !strings.Contains(msg, "InvalidSubnet") {
		t.Errorf("message does not list the valid codes: %q", msg)
	}
}

func TestRejectsAReasonCodeOnABuild(t *testing.T) {
	// A failed build carries a message, not a code. Accepting one silently
	// would leave a caller believing they had selected a failure mode.
	rec := post(t, enabled(t), `{"target":"build","name":"img","reasonCode":"InvalidSubnet"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRejectsAMissingName(t *testing.T) {
	rec := post(t, enabled(t), `{"target":"build"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestRejectsAnUnknownTarget(t *testing.T) {
	rec := post(t, enabled(t), `{"target":"vm","name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestRejectsANonJSONBody(t *testing.T) {
	rec := post(t, enabled(t), `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestDisabledSaysSoRatherThanFourOhFouring(t *testing.T) {
	srv := api.NewServer(clock.Real{}, store.New(), "test")
	RegisterDisabled(srv)
	rec := post(t, srv, `{"target":"build","name":"img"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	// A bare 404 is indistinguishable from a typo in the path. The whole
	// value of registering the disabled route is this sentence.
	if msg := decode(t, rec)["message"].(string); !strings.Contains(msg, "-enable-injection") {
		t.Errorf("message does not name the flag: %q", msg)
	}
}

func TestUnregisteredMeansTheRouteIsAbsent(t *testing.T) {
	// Neither Register nor RegisterDisabled: nothing claims the path. Guards
	// against the route being wired somewhere central by accident, which
	// would put the levers on every m80 whether or not it asked.
	srv := api.NewServer(clock.Real{}, store.New(), "test")
	rec := post(t, srv, `{"target":"build","name":"img"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("an unregistered inject route answered 200")
	}
}
