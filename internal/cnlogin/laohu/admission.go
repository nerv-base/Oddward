// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// admissionTTL is how long one allowed pre-login decision stays reusable, so a
// repeated game query for the same account does not repeat the request.
const admissionTTL = time.Minute

// errAdmissionNoClient reports an Admission that cannot check anything. It
// fails closed: no identity is delivered without a pre-login result.
var errAdmissionNoClient = errors.New("laohu: admission requires a client")

// Decision is the pure outcome of the pre-login admission table.
type Decision struct {
	Allow bool
	Phase Phase
}

// Decide applies the pre-login admission table (spec §3.9). The first matching
// row wins: a service rejection is reported before any identity problem, a
// missing real-name registration before a restriction, and only a fully known
// state (realUser 1, status 0, bannedType 0) admits.
func Decide(r PreLoginResult) Decision {
	switch {
	case r.Code != 0:
		return Decision{Phase: PhasePreLoginServiceRejected}
	case r.RealUser == 0:
		return Decision{Phase: PhaseRealNameRequired}
	case r.Status == 1 || r.BannedType > 0:
		return Decision{Phase: PhaseRealNameRestricted}
	case r.RealUser != 1 || r.Status != 0 || r.BannedType < 0:
		return Decision{Phase: PhasePreLoginStateUnavailable}
	default:
		return Decision{Allow: true}
	}
}

// Admission serialises the pre-login checks that gate every identity delivery
// to the game and caches one allowed decision for a minute. It holds at most
// one identity at a time: an admission for another UID replaces the entry.
// Admit holds the lock across the request, so concurrent callers serialise
// instead of racing.
type Admission struct {
	mu     sync.Mutex
	client *Client
	now    func() time.Time
	cached admissionEntry
}

// admissionEntry is one allowed decision. uid and result.Body are copies, so
// neither the caller's identity nor the caller's result can alias the cache.
type admissionEntry struct {
	uid    []byte
	result PreLoginResult
	at     time.Time
	valid  bool
}

// NewAdmission returns an Admission that checks identities through c. A nil now
// falls back to time.Now; a nil c is refused, because an admission without a
// client could only fail closed.
func NewAdmission(c *Client, now func() time.Time) (*Admission, error) {
	if c == nil {
		return nil, errAdmissionNoClient
	}
	if now == nil {
		now = time.Now
	}
	return &Admission{client: c, now: now}, nil
}

// Admit returns the admitting pre-login result for id. A cached allowed result
// for the same UID younger than one minute is returned without a request. Any
// other outcome asks the service: a transport or parse failure is returned
// unchanged and caches nothing; a denied identity returns its result plus an
// *Error carrying the table's phase and never leaves an allow cached. The
// returned result owns its Body and the caller must Zero it.
func (a *Admission) Admit(ctx context.Context, id GameIdentity) (PreLoginResult, error) {
	if a == nil {
		return PreLoginResult{}, errAdmissionNoClient
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil {
		return PreLoginResult{}, errAdmissionNoClient
	}
	now := a.clock()
	if a.cached.valid && bytes.Equal(a.cached.uid, id.UID) && now.Before(a.cached.at.Add(admissionTTL)) {
		return clonePreLoginResult(a.cached.result), nil
	}
	result, err := a.client.PreLogin(ctx, id)
	if err != nil {
		return PreLoginResult{}, err
	}
	if decision := Decide(result); !decision.Allow {
		// A denial must never leave an earlier allow visible to Cached.
		a.dropCacheLocked()
		return result, &Error{Phase: decision.Phase}
	}
	a.dropCacheLocked()
	// Timestamp the entry after the response, so a slow request does not shorten
	// the reusable window.
	a.cached = admissionEntry{uid: bytes.Clone(id.UID), result: clonePreLoginResult(result), at: a.clock(), valid: true}
	return result, nil
}

// Cached reports the currently reusable allowed decision. It returns false when
// nothing was admitted, once the decision is a minute old, and after Reset. The
// returned result is a copy the caller must Zero.
func (a *Admission) Cached() (PreLoginResult, bool) {
	if a == nil {
		return PreLoginResult{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cached.valid {
		return PreLoginResult{}, false
	}
	if !a.clock().Before(a.cached.at.Add(admissionTTL)) {
		a.dropCacheLocked()
		return PreLoginResult{}, false
	}
	return clonePreLoginResult(a.cached.result), true
}

// Reset forgets the cached decision and clears its UID and body.
func (a *Admission) Reset() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropCacheLocked()
}

// clock reads the injected clock; the zero Admission falls back to time.Now.
func (a *Admission) clock() time.Time {
	if a.now == nil {
		return time.Now()
	}
	return a.now()
}

// dropCacheLocked clears the cached identity and body, then the entry.
func (a *Admission) dropCacheLocked() {
	clear(a.cached.uid)
	clear(a.cached.result.Body)
	a.cached = admissionEntry{}
}

// clonePreLoginResult copies the body so the cache and the caller never share
// one buffer. Message is an immutable string.
func clonePreLoginResult(r PreLoginResult) PreLoginResult {
	r.Body = bytes.Clone(r.Body)
	return r
}

// String omits the cached identity and body.
func (a *Admission) String() string { return "laohu.Admission{redacted}" }

// GoString omits the cached identity and body.
func (a *Admission) GoString() string { return a.String() }

func (a *Admission) Format(f fmt.State, verb rune) { io.WriteString(f, a.String()) }
