package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegionFromRequest(t *testing.T) {
	for _, tc := range []struct {
		name, auth, want string
	}{
		{
			name: "sigv4 scope",
			auth: "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260730/us-east-2/lambda/aws4_request, SignedHeaders=host;x-amz-date, Signature=abc",
			want: "us-east-2",
		},
		{
			name: "another region",
			auth: "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260730/eu-west-1/lambda/aws4_request, SignedHeaders=host, Signature=abc",
			want: "eu-west-1",
		},
		{
			// curl by hand: answer plausibly rather than erroring.
			name: "no authorization header",
			auth: "",
			want: DefaultRegion,
		},
		{
			name: "not sigv4",
			auth: "Bearer some-token",
			want: DefaultRegion,
		},
		{
			name: "truncated credential scope",
			auth: "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260730, SignedHeaders=host, Signature=abc",
			want: DefaultRegion,
		},
		{
			name: "empty region segment",
			auth: "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260730//lambda/aws4_request, SignedHeaders=host, Signature=abc",
			want: DefaultRegion,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			if got := RegionFromRequest(r); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The live service does not put __type in error bodies; the type rides the
// header. An emulator that adds one diverges on every error case.
func TestWriteErrorPutsTypeInHeaderNotBody(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusNotFound, "ResourceNotFoundException", map[string]any{"message": "nope"})

	if got := rec.Header().Get("X-Amzn-Errortype"); got != "ResourceNotFoundException" {
		t.Errorf("error type header %q", got)
	}
	if body := rec.Body.String(); contains(body, "__type") {
		t.Errorf("body carries __type: %s", body)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
