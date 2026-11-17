// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// admissionIdentity is a synthetic game identity. UID "42" matches the shared
// preLoginJSON fixture; the token is never real.
func admissionIdentity(uid string) GameIdentity {
	return GameIdentity{UID: []byte(uid), Token: []byte("synthetic-game-token")}
}

// preLoginAllowed renders an admitting pre-login reply for uid.
func preLoginAllowed(uid string) string {
	return `{"code":0,"message":"ok","result":{"status":"0","realUser":1,"bannedType":null,"appId":1289,"userId":"` + uid + `"}}`
}

// admissionClient returns an initialised client whose pre-login replies come
// from reply. Only the boot-config request is answered locally.
func admissionClient(t *testing.T, clock *fakeClock, reply func(*http.Request) (*http.Response, error)) *Client {
	t.Helper()
	c := newTestClient(t, clock, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/m/newApi/initConfig":
			return response(200, `{"code":0,"result":{"loginOptionConfig":[92]}}`), nil
		case "/sdkapi/preLogin":
			return reply(r)
		}
		t.Fatalf("unexpected request path %q", r.URL.Path)
		return nil, nil
	})
	if err := c.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return c
}

func TestDecideTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    PreLoginResult
		want Decision
	}{
		{"service-rejected", PreLoginResult{Code: 7, RealUser: 1}, Decision{Phase: PhasePreLoginServiceRejected}},
		{"code-precedes-identity", PreLoginResult{Code: 7, Status: 1, RealUser: 0, BannedType: 2}, Decision{Phase: PhasePreLoginServiceRejected}},
		{"real-name-required", PreLoginResult{RealUser: 0}, Decision{Phase: PhaseRealNameRequired}},
		{"real-name-precedes-status", PreLoginResult{Status: 1, RealUser: 0}, Decision{Phase: PhaseRealNameRequired}},
		{"restricted-status", PreLoginResult{RealUser: 1, Status: 1}, Decision{Phase: PhaseRealNameRestricted}},
		{"restricted-ban", PreLoginResult{RealUser: 1, BannedType: 2}, Decision{Phase: PhaseRealNameRestricted}},
		{"unknown-real-user", PreLoginResult{RealUser: 2}, Decision{Phase: PhasePreLoginStateUnavailable}},
		{"unknown-status", PreLoginResult{RealUser: 1, Status: -1}, Decision{Phase: PhasePreLoginStateUnavailable}},
		{"unknown-ban", PreLoginResult{RealUser: 1, BannedType: -1}, Decision{Phase: PhasePreLoginStateUnavailable}},
		{"allowed", PreLoginResult{RealUser: 1}, Decision{Allow: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.r); got != tc.want {
				t.Fatalf("Decide(%+v) = %+v, want %+v", tc.r, got, tc.want)
			}
		})
	}
}

func TestAdmitBlocksDeniedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		phase Phase
	}{
		{"service-rejected", `{"code":7,"message":"rejected"}`, PhasePreLoginServiceRejected},
		{"real-name-required", `{"code":0,"result":{"status":"0","realUser":0,"bannedType":null,"appId":1289,"userId":"42"}}`, PhaseRealNameRequired},
		{"restricted-status", `{"code":0,"result":{"status":"1","realUser":1,"bannedType":null,"appId":1289,"userId":"42"}}`, PhaseRealNameRestricted},
		{"restricted-ban", `{"code":0,"result":{"status":"0","realUser":1,"bannedType":2,"appId":1289,"userId":"42"}}`, PhaseRealNameRestricted},
		{"unknown-real-user", `{"code":0,"result":{"status":"0","realUser":2,"bannedType":null,"appId":1289,"userId":"42"}}`, PhasePreLoginStateUnavailable},
		{"unknown-status", `{"code":0,"result":{"status":"-1","realUser":1,"bannedType":null,"appId":1289,"userId":"42"}}`, PhasePreLoginStateUnavailable},
		{"unknown-ban", `{"code":0,"result":{"status":"0","realUser":1,"bannedType":-1,"appId":1289,"userId":"42"}}`, PhasePreLoginStateUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{testTime}
			c := admissionClient(t, clock, func(*http.Request) (*http.Response, error) {
				return response(200, tc.body), nil
			})
			a, err := NewAdmission(c, clock.Now)
			if err != nil {
				t.Fatalf("NewAdmission: %v", err)
			}
			result, err := a.Admit(context.Background(), admissionIdentity("42"))
			defer result.Zero()
			if err == nil {
				t.Fatal("denied identity admitted")
			}
			assertPhase(t, err, tc.phase)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatal("denial is not a *Error")
			}
			if e.Message() == "" {
				t.Fatal("denial has no explanation")
			}
			if _, ok := a.Cached(); ok {
				t.Fatal("denial cached")
			}
		})
	}
}

func TestAdmitCachesAllowedDecisionForOneMinute(t *testing.T) {
	clock := &fakeClock{testTime}
	requests := 0
	c := admissionClient(t, clock, func(*http.Request) (*http.Response, error) {
		requests++
		return response(200, preLoginJSON), nil
	})
	a, err := NewAdmission(c, clock.Now)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	uid := []byte("42")
	first, err := a.Admit(context.Background(), GameIdentity{UID: uid, Token: []byte("synthetic-game-token")})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if !bytes.Equal(first.Body, []byte(preLoginJSON)) {
		t.Fatalf("body = %s", first.Body)
	}
	first.Zero()    // the caller zeroing its result must not damage the cache
	copy(uid, "43") // the cache must not alias the caller's UID

	again, err := a.Admit(context.Background(), admissionIdentity("42"))
	if err != nil {
		t.Fatalf("cached Admit: %v", err)
	}
	if requests != 1 {
		t.Fatalf("pre-login requests = %d, want 1", requests)
	}
	if !bytes.Equal(again.Body, []byte(preLoginJSON)) {
		t.Fatal("cached body was damaged")
	}
	again.Zero()

	cached, ok := a.Cached()
	if !ok {
		t.Fatal("allowed decision not cached")
	}
	cached.Zero() // Cached must hand out a copy too

	clock.Advance(59 * time.Second)
	inside, err := a.Admit(context.Background(), admissionIdentity("42"))
	if err != nil {
		t.Fatalf("Admit inside the minute: %v", err)
	}
	inside.Zero()
	if requests != 1 {
		t.Fatalf("cache expired before one minute: %d requests", requests)
	}

	clock.Advance(2 * time.Second)
	after, err := a.Admit(context.Background(), admissionIdentity("42"))
	if err != nil {
		t.Fatalf("Admit after the minute: %v", err)
	}
	after.Zero()
	if requests != 2 {
		t.Fatalf("pre-login requests = %d, want 2", requests)
	}
}

func TestAdmitDoesNotCacheDenial(t *testing.T) {
	clock := &fakeClock{testTime}
	requests := 0
	c := admissionClient(t, clock, func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return response(200, `{"code":0,"result":{"status":"0","realUser":0,"bannedType":null,"appId":1289,"userId":"42"}}`), nil
		}
		return response(200, preLoginJSON), nil
	})
	a, err := NewAdmission(c, clock.Now)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	denied, err := a.Admit(context.Background(), admissionIdentity("42"))
	defer denied.Zero()
	assertPhase(t, err, PhaseRealNameRequired)
	if _, ok := a.Cached(); ok {
		t.Fatal("denial cached")
	}
	allowed, err := a.Admit(context.Background(), admissionIdentity("42"))
	if err != nil {
		t.Fatalf("retry after denial: %v", err)
	}
	defer allowed.Zero()
	if requests != 2 {
		t.Fatalf("pre-login requests = %d, want 2 (a denial must be re-checked)", requests)
	}
	if !bytes.Equal(allowed.Body, []byte(preLoginJSON)) {
		t.Fatalf("body = %s", allowed.Body)
	}
}

func TestAdmitPropagatesPreLoginFailure(t *testing.T) {
	clock := &fakeClock{testTime}
	c := admissionClient(t, clock, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("pre-login transport secret " + testPhone)
	})
	a, err := NewAdmission(c, clock.Now)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	result, err := a.Admit(context.Background(), admissionIdentity("42"))
	defer result.Zero()
	if err == nil {
		t.Fatal("transport failure admitted")
	}
	assertPhase(t, err, PhaseRequestFailed)
	if result.Code != 0 || result.Body != nil {
		t.Fatal("transport failure returned a result")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), testPhone) {
		t.Fatal("unsafe error text")
	}
	if _, ok := a.Cached(); ok {
		t.Fatal("transport failure cached")
	}
	if _, direct := c.PreLogin(context.Background(), admissionIdentity("42")); direct == nil || direct.Error() != err.Error() {
		t.Fatalf("Admit altered the transport error: %v", direct)
	}

	invalid := admissionClient(t, clock, func(*http.Request) (*http.Response, error) {
		return response(200, `{"code":0}`), nil
	})
	b, err := NewAdmission(invalid, clock.Now)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	bad, err := b.Admit(context.Background(), admissionIdentity("42"))
	defer bad.Zero()
	assertPhase(t, err, PhaseInvalidResponse)
	if _, ok := b.Cached(); ok {
		t.Fatal("invalid response cached")
	}
}

func TestAdmitRequestsNewIdentitySeparately(t *testing.T) {
	clock := &fakeClock{testTime}
	requests := map[string]int{}
	c := admissionClient(t, clock, func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		uid := r.Form.Get("userId")
		requests[uid]++
		return response(200, preLoginAllowed(uid)), nil
	})
	a, err := NewAdmission(c, clock.Now)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	first, err := a.Admit(context.Background(), admissionIdentity("42"))
	if err != nil {
		t.Fatalf("Admit 42: %v", err)
	}
	defer first.Zero()
	other, err := a.Admit(context.Background(), admissionIdentity("43"))
	if err != nil {
		t.Fatalf("Admit 43: %v", err)
	}
	defer other.Zero()
	if requests["42"] != 1 || requests["43"] != 1 {
		t.Fatalf("requests = %v, want one per identity", requests)
	}
	// The cache holds one identity; the second 43 is served from it.
	repeat, err := a.Admit(context.Background(), admissionIdentity("43"))
	if err != nil {
		t.Fatalf("cached Admit 43: %v", err)
	}
	defer repeat.Zero()
	if requests["42"] != 1 || requests["43"] != 1 {
		t.Fatalf("requests = %v, want the second 43 to be cached", requests)
	}
	// Admitting 43 replaced the entry for 42, so 42 must be checked again.
	third, err := a.Admit(context.Background(), admissionIdentity("42"))
	if err != nil {
		t.Fatalf("Admit 42 again: %v", err)
	}
	defer third.Zero()
	if requests["42"] != 2 || requests["43"] != 1 {
		t.Fatalf("requests = %v, want 42 re-checked after the cache moved to 43", requests)
	}
}

func TestAdmissionResetDropsCache(t *testing.T) {
	clock := &fakeClock{testTime}
	requests := 0
	c := admissionClient(t, clock, func(*http.Request) (*http.Response, error) {
		requests++
		return response(200, preLoginJSON), nil
	})
	a, err := NewAdmission(c, clock.Now)
	if err != nil {
		t.Fatalf("NewAdmission: %v", err)
	}
	allowed, err := a.Admit(context.Background(), admissionIdentity("42"))
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	allowed.Zero()
	if _, ok := a.Cached(); !ok {
		t.Fatal("allowed decision not cached")
	}
	a.Reset()
	if _, ok := a.Cached(); ok {
		t.Fatal("Reset kept the cache")
	}
	if a.cached.valid || len(a.cached.uid) != 0 || len(a.cached.result.Body) != 0 {
		t.Fatal("Reset retained cached identity or body")
	}
	if _, err := a.Admit(context.Background(), admissionIdentity("42")); err != nil {
		t.Fatalf("Admit after Reset: %v", err)
	}
	if requests != 2 {
		t.Fatalf("pre-login requests = %d, want 2 (Reset must force a new request)", requests)
	}
}

func TestNewAdmissionRequiresClient(t *testing.T) {
	if _, err := NewAdmission(nil, nil); err == nil {
		t.Fatal("nil client accepted")
	}
	clock := &fakeClock{testTime}
	c := admissionClient(t, clock, func(*http.Request) (*http.Response, error) {
		return response(200, preLoginJSON), nil
	})
	a, err := NewAdmission(c, nil) // nil clock falls back to time.Now, documented
	if err != nil {
		t.Fatalf("nil clock rejected: %v", err)
	}
	if a == nil {
		t.Fatal("NewAdmission returned no admission")
	}
}
