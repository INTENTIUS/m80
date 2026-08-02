package sts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h := &Handler{RequestID: func() string { return "11111111-2222-4333-8444-555555555555" }}
	h.ServeHTTP(w, r)
	return w
}

// The whole point of this package is that one document. It is compared in
// full against what a real AWS emulator returns on the wire, because the
// operator's SDK parses it and a missing element fails the startup gate with
// nothing useful in the log.
func TestGetCallerIdentityMatchesTheWireShape(t *testing.T) {
	w := post(t, "Action=GetCallerIdentity&Version=2011-06-15")

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/xml" {
		t.Errorf("Content-Type %q, want text/xml", ct)
	}

	want := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">` +
		`<GetCallerIdentityResult>` +
		`<UserId>000000000000</UserId>` +
		`<Account>000000000000</Account>` +
		`<Arn>arn:aws:iam::000000000000:root</Arn>` +
		`</GetCallerIdentityResult>` +
		`<ResponseMetadata><RequestId>11111111-2222-4333-8444-555555555555</RequestId></ResponseMetadata>` +
		`</GetCallerIdentityResponse>`
	if got := w.Body.String(); got != want {
		t.Errorf("body mismatch\n got: %s\nwant: %s", got, want)
	}
}

// The account has to be m80's own, or every ARN the operator reads back from
// a MicroVM response names a different account than the one it authenticated
// as, and drift detection has a field that never converges.
func TestAccountMatchesTheRestOfM80(t *testing.T) {
	w := post(t, "Action=GetCallerIdentity&Version=2011-06-15")
	for _, want := range []string{
		"<Account>000000000000</Account>",
		"arn:aws:iam::000000000000:root",
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// Refusing loudly is the feature. A shim that quietly grew AssumeRole would
// be an STS emulator nobody decided to write.
func TestEveryOtherActionIsRefusedAsUnimplemented(t *testing.T) {
	for _, action := range []string{"AssumeRole", "GetSessionToken", "AssumeRoleWithWebIdentity", ""} {
		w := post(t, "Action="+action+"&Version=2011-06-15")
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%q: status %d, want 501", action, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "<Code>InvalidAction</Code>") {
			t.Errorf("%q: body has no InvalidAction code: %s", action, body)
		}
		if !strings.Contains(body, "not an STS emulator") {
			t.Errorf("%q: refusal does not say what m80 is: %s", action, body)
		}
	}
}

// Claims decides what the pre-route hook swallows. It must not take requests
// the rest of m80 answers, and there is no rest-json operation on POST /, so
// the content type is what separates them.
func TestClaimsOnlyTakesQueryProtocolPostsToRoot(t *testing.T) {
	cases := []struct {
		name, method, path, contentType string
		want                            bool
	}{
		{"sts query post", http.MethodPost, "/", "application/x-www-form-urlencoded", true},
		{"charset suffix", http.MethodPost, "/", "application/x-www-form-urlencoded; charset=utf-8", true},
		{"json post to root", http.MethodPost, "/", "application/json", false},
		{"a real operation", http.MethodPost, "/2025-09-09/microvms", "application/json", false},
		{"form post elsewhere", http.MethodPost, "/2025-09-09/microvms", "application/x-www-form-urlencoded", false},
		{"get on root", http.MethodGet, "/", "application/x-www-form-urlencoded", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(""))
		r.Header.Set("Content-Type", tc.contentType)
		if got := Claims(r); got != tc.want {
			t.Errorf("%s: Claims = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Request ids are logged by clients, so a malformed one would be noticed.
func TestRequestIDIsAWellFormedUUID(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("Action=GetCallerIdentity"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	(&Handler{}).ServeHTTP(w, r)

	body := w.Body.String()
	i := strings.Index(body, "<RequestId>")
	if i < 0 {
		t.Fatal("no RequestId")
	}
	id := body[i+len("<RequestId>"):]
	id = id[:strings.Index(id, "<")]
	if len(id) != 36 || strings.Count(id, "-") != 4 || id[14] != '4' {
		t.Errorf("RequestId %q is not a v4 UUID", id)
	}
}
