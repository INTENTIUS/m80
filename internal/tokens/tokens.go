// Package tokens implements the two auth-token operations and the per-VM
// endpoint they authorize.
//
// The recorded token is a JWE in compact serialization, five parts with an
// empty encrypted-key segment because the header says alg "dir":
//
//	{"kid":"<uuid>","alg":"dir","enc":"A256GCM"}..<iv>.<ciphertext>.<tag>
//
// m80 mints the same shape from random bytes and validates by table lookup
// rather than by decrypting. A client that only carries the token around
// cannot tell the difference; one that parses the header for a kid finds the
// member where it expects it. What m80 does not do is pretend the token is
// meaningful — nothing is encrypted in it, and nothing should be read out of
// it beyond the header.
//
// The endpoint stub's answers are largely unrecorded. Two targets could not be
// recorded rather than merely not having been: the endpoint probe needs the
// conformance runner to address a host that is not the control plane, and
// CreateMicrovmShellAuthToken's success path needs a SHELL_INGRESS connector,
// a type absent from the service model entirely. Everything this package
// guesses is marked at the point of the guess.
package tokens

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/intentius/m80/internal/clock"
)

// MaxExpirationMinutes is the ceiling the model documents on
// CreateMicrovmAuthToken's expirationInMinutes.
const MaxExpirationMinutes = 60

// VMSource is what tokens needs from vms. It trades only in primitives so
// that vms implements it without importing this package.
type VMSource interface {
	// LookupEndpoint resolves a per-VM endpoint hostname.
	LookupEndpoint(host string) (region, id string, ok bool)
	// LookupID finds which region holds a VM.
	LookupID(id string) (region string, ok bool)
	// Status reports the VM's state and whether endpoint traffic may wake it.
	Status(region, id string) (state string, autoResume bool, ok bool)
	// RecordTraffic bumps the state marker and resets the idle timer.
	RecordTraffic(region, id string) (marker uint64, ok bool)
	// Wake resumes a suspended VM.
	Wake(region, id string)
}

// Token is one issued credential. Ports is the set the token grants, which
// the endpoint checks the request against; allPorts skips the check.
type Token struct {
	Value     string
	Region    string
	VMID      string
	IssuedAt  time.Time
	ExpiresAt time.Time
	AllPorts  bool
	Ports     []portRange
	Shell     bool
}

type portRange struct{ lo, hi int }

func (p portRange) contains(port int) bool { return port >= p.lo && port <= p.hi }

// Allows reports whether the token grants a port.
func (t *Token) Allows(port int) bool {
	if t.AllPorts {
		return true
	}
	for _, p := range t.Ports {
		if p.contains(port) {
			return true
		}
	}
	return false
}

type Service struct {
	clock clock.Clock

	mu sync.Mutex
	// byValue is the whole validation story: m80 does not decrypt, it looks
	// up. Keyed by the token string a client presents.
	byValue map[string]*Token
}

func NewService(c clock.Clock) *Service {
	return &Service{clock: c, byValue: map[string]*Token{}}
}

// Issue mints a token for a VM and remembers it for validation.
func (s *Service) Issue(region, vmID string, expiresIn time.Duration, allPorts bool, ports []portRange, shell bool) *Token {
	now := s.clock.Now()
	t := &Token{
		Value:     mintJWE(),
		Region:    region,
		VMID:      vmID,
		IssuedAt:  now,
		ExpiresAt: now.Add(expiresIn),
		AllPorts:  allPorts,
		Ports:     ports,
		Shell:     shell,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byValue[t.Value] = t
	return t
}

// Validate resolves a presented token. It reports the token and whether it is
// both known and unexpired; an expired one is returned alongside false so a
// caller can tell the two apart if it ever needs to.
func (s *Service) Validate(value string) (*Token, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byValue[value]
	if !ok {
		return nil, false
	}
	if !s.clock.Now().Before(t.ExpiresAt) {
		return t, false
	}
	return t, true
}

// mintJWE builds a token with the recorded shape: a real JWE header carrying
// a kid, an empty encrypted-key segment for alg "dir", then random iv,
// ciphertext and tag. The bytes past the header are noise on purpose — there
// is no key and nothing is encrypted.
func mintJWE() string {
	header, _ := json.Marshal(struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
		Enc string `json:"enc"`
	}{Kid: newUUID(), Alg: "dir", Enc: "A256GCM"})

	enc := base64.RawURLEncoding.EncodeToString
	// 12-byte iv and 16-byte tag are what A256GCM uses; the ciphertext length
	// is arbitrary and sized to look like the recorded one.
	return strings.Join([]string{
		enc(header),
		"", // alg "dir" carries no encrypted key, hence the ".." in the wire form
		enc(randomBytes(12)),
		enc(randomBytes(384)),
		enc(randomBytes(16)),
	}, ".")
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("tokens: no entropy: " + err.Error())
	}
	return b
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("tokens: no entropy: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return strings.Join([]string{h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]}, "-")
}
