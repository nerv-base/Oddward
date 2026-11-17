// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRolesReadOnlyAndExplicitBind(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			var writes atomic.Int32
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/apihub/api/getGameBindRole":
					if r.URL.Query().Get("uid") != "51" || r.URL.Query().Get("gameId") == "" {
						t.Error("missing role query")
					}
					reply(w, map[string]any{"roleId": 123})
				case "/usercenter/api/v2/getGameRoles":
					roles := []any{map[string]any{"roleId": 123, "roleName": "名字"}}
					if legacy {
						reply(w, roles)
					} else {
						reply(w, map[string]any{"bindRole": 123, "roles": roles})
					}
				case "/usercenter/api/bindGameRole":
					writes.Add(1)
					if r.Method != "POST" {
						t.Error("binding must POST")
					}
					r.ParseForm()
					if r.Form.Get("roleId") != "123" {
						t.Error("missing explicit role")
					}
					reply(w, true)
				default:
					t.Error("unexpected endpoint")
				}
			})
			roles, e := c.Roles(context.Background(), testAccess())
			if e != nil || roles.BoundRoleID != "123" || len(roles.Roles) != 1 {
				t.Fatalf("roles=%v err=%v", roles, e)
			}
			if writes.Load() != 0 {
				t.Fatal("Roles performed a binding")
			}
			if e = c.BindRole(context.Background(), testAccess(), "123"); e != nil {
				t.Fatal(e)
			}
			if writes.Load() != 1 {
				t.Fatal("missing explicit binding")
			}
		})
	}
}
func TestUnboundRoleRequiresExplicitChoice(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apihub/api/getGameBindRole":
			reply(w, map[string]any{"roleId": 0})
		case "/usercenter/api/v2/getGameRoles":
			reply(w, map[string]any{"bindRole": 0, "roles": []any{map[string]any{"roleId": 123, "roleName": "名字"}}})
		default:
			t.Fatal("unbound data must not fetch")
		}
	})
	roles, e := c.Roles(context.Background(), testAccess())
	if e != nil || roles.BoundRoleID != "" || len(roles.Roles) != 1 {
		t.Fatalf("roles=%v err=%v", roles, e)
	}
	a := testAccess()
	a.RoleID = ""
	if _, e = c.Gacha(context.Background(), a); !errors.Is(e, ErrNoBoundRole) {
		t.Fatal(e)
	}
	if _, e = c.Characters(context.Background(), a); !errors.Is(e, ErrNoBoundRole) {
		t.Fatal(e)
	}
}
func TestSessionExpiredIsTerminal(t *testing.T) {
	var count atomic.Int32
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); w.WriteHeader(401) })
	s := NewSession(c, testGrant(), func(Grant) error { t.Error("persisted failure"); return nil })
	for range 2 {
		if _, e := s.Access(context.Background()); !errors.Is(e, ErrAuthExpired) {
			t.Fatal(e)
		}
	}
	if count.Load() != 1 || !s.NeedsRelink() {
		t.Fatal("expired grant retried or not marked")
	}
}
func TestCancelledWaiterDoesNotCancelRefresh(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; refreshed(w) })
	s := NewSession(c, testGrant(), func(Grant) error { return nil })
	done := make(chan error, 1)
	go func() { _, e := s.Access(context.Background()); done <- e }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := s.Access(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
func TestPersistenceBlocksConcurrentAccess(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { refreshed(w) })
	s := NewSession(c, testGrant(), func(Grant) error { close(entered); <-release; return nil })
	done := make(chan Access, 2)
	go func() { a, _ := s.Access(context.Background()); done <- a }()
	<-entered
	go func() { a, _ := s.Access(context.Background()); done <- a }()
	select {
	case <-done:
		t.Error("new access escaped before persistence")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if a := <-done; a.Token != "access-new-secret" {
			t.Fatal("missing new access")
		}
	}
}
func TestProtocolResponseBoundaries(t *testing.T) {
	for name, raw := range map[string]string{
		"missing-code": `{"data":{}}`, "missing-data": `{"code":0}`, "wrong-root": `[]`, "duplicate": `{"code":0,"code":0,"data":{}}`, "trailing": `{"code":0,"data":{}} {}`, "wrong-token-type": `{"code":0,"data":{"accessToken":5,"refreshToken":"new"}}`, "null": `{"code":0,"data":null}`, "same-refresh": `{"code":0,"data":{"accessToken":"new-access","refreshToken":"refresh-old-secret"}}`, "signature-refused": `{"code":1009,"msg":"signature rejected"}`, "oversize": strings.Repeat(" ", maxResponseBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, raw) })
			g, a, e := c.Refresh(context.Background(), testGrant())
			if !errors.Is(e, ErrProtocolChanged) || g.RefreshToken != "" || a.Token != "" {
				t.Fatalf("grant=%v access=%v error=%v", g, a, e)
			}
		})
	}
}
func TestAdditiveFieldsAndStringBusinessCode(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"code":"0","future":{"value":[1,true,null]},"data":{"accessToken":"new-access","refreshToken":"new-refresh","future":"allowed"}}`)
	})
	if _, _, e := c.Refresh(context.Background(), testGrant()); e != nil {
		t.Fatal(e)
	}
}
func TestNoRedirectLeaksCredentials(t *testing.T) {
	var calls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer sink.Close()
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	})
	if _, _, e := c.Refresh(context.Background(), testGrant()); !errors.Is(e, ErrProtocolChanged) {
		t.Fatal(e)
	}
	if calls.Load() != 0 {
		t.Fatal("followed redirect with authorization")
	}
}
func TestNetworkErrorIsSafeAndClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	c, e := NewClient(Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = c.Refresh(context.Background(), testGrant()); !errors.Is(e, ErrNetwork) || strings.Contains(fmt.Sprint(e), testGrant().RefreshToken) {
		t.Fatal(e)
	}
}
func TestConfigRejectsUnsafeBases(t *testing.T) {
	for _, base := range []string{"http://example.invalid", "https://user:secret@example.invalid", "https://example.invalid?secret=1", "https://example.invalid/path", "ftp://example.invalid"} {
		if _, e := NewClient(Config{HTTPClient: &http.Client{}, BaseURL: base}); !errors.Is(e, ErrInvalidInput) {
			t.Fatal("accepted unsafe base")
		}
	}
	if _, e := NewClient(Config{}); !errors.Is(e, ErrInvalidInput) {
		t.Fatal("default network client created")
	}
}
func TestGachaPityAnomalyRetained(t *testing.T) {
	p := gachaPayload()
	d := p["gachaDetails"].([]any)[0].(map[string]any)
	records := d["details"].([]any)
	records[0].(map[string]any)["rareCount"] = 0
	records[1].(map[string]any)["rareCount"] = 91
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, p) })
	s, e := c.Gacha(context.Background(), testAccess())
	if e != nil || len(s.Pools[0].Records) != 2 {
		t.Fatalf("snapshot=%+v error=%v", s, e)
	}
}
