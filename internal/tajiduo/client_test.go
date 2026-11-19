// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/netcfg"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testTime = time.Unix(1730000000, 0).UTC()

func testGrant() Grant {
	return Grant{DeviceID: strings.Repeat("A", 32), CenterUID: "51", RefreshToken: "refresh-old-secret", LaohuUserID: "42", RoleID: "123"}
}
func testAccess() Access {
	g := testGrant()
	return Access{Token: "access-secret", DeviceID: g.DeviceID, CenterUID: g.CenterUID, LaohuUserID: g.LaohuUserID, RoleID: g.RoleID}
}
func fakeClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	n := netcfg.New(netcfg.Settings{Proxy: netcfg.Direct}, nil)
	c, err := NewClient(Config{HTTPClient: n.Client(time.Second * 5), BaseURL: srv.URL, LaohuBaseURL: srv.URL, Now: func() time.Time { return testTime }, Rand: strings.NewReader(strings.Repeat("a", 4096))})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func reply(w http.ResponseWriter, data any) {
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}
func refreshed(w http.ResponseWriter) {
	reply(w, map[string]any{"accessToken": "access-new-secret", "refreshToken": "refresh-new-secret"})
}
func TestDSFakeVector(t *testing.T) {
	nonce := [8]byte{'A', 'b', '0', '1', '2', '3', 'x', 'Z'}
	const want = "1730000000,Ab0123xZ,2dc82a47b35b12eb6383f7fbc699cb31"
	if got := dsWithSalt(testTime, nonce, "test-version", "fake-salt"); got != want {
		t.Fatalf("DS got %q want %q", got, want)
	}
}
func TestLoginTwoSteps(t *testing.T) {
	var paths []string
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		r.ParseForm()
		switch r.URL.Path {
		case "/openApi/sms/new/login":
			if r.Form.Get("cellphone") == "13800000000" || r.Form.Get("sign") == "" {
				t.Error("phone not encrypted or missing signature")
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "result": map[string]any{"userId": 42, "token": "laohu-only-memory"}})
		case "/usercenter/api/login":
			if r.Form.Get("token") != "laohu-only-memory" || r.Form.Get("userIdentity") != "42" || r.Header.Get("ds") == "" {
				t.Error("missing exchange fields")
			}
			reply(w, map[string]any{"accessToken": "login-access-secret", "refreshToken": "login-refresh-secret", "uid": 51})
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	})
	g, err := c.LoginSMS(context.Background(), "13800000000", "123456", strings.Repeat("A", 32))
	if err != nil {
		t.Fatal(err)
	}
	if g.RefreshToken != "login-refresh-secret" || g.LaohuUserID != "42" || g.CenterUID != "51" {
		t.Fatalf("bad grant %v", g)
	}
	if len(paths) != 2 {
		t.Fatalf("requests %v", paths)
	}
}
func TestExchangeGameCredential(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usercenter/api/login" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		r.ParseForm()
		if r.Form.Get("token") != "channel-token-secret" || r.Form.Get("userIdentity") != "42" || r.Form.Get("appId") != centerAppID {
			t.Error("missing exchange fields")
		}
		if r.Header.Get("deviceid") != strings.Repeat("A", 32) || r.Header.Get("authorization") != "" {
			t.Error("wrong device or pre-existing authorization")
		}
		reply(w, map[string]any{"accessToken": "access-secret", "refreshToken": "refresh-secret", "uid": 51})
	})
	g, err := c.ExchangeGameCredential(context.Background(), "channel-token-secret", "42", strings.Repeat("A", 32))
	if err != nil {
		t.Fatal(err)
	}
	if g.CenterUID != "51" || g.LaohuUserID != "42" || g.RefreshToken != "refresh-secret" || !validGrant(g) {
		t.Fatalf("bad grant %+v", g)
	}
}

func TestExchangeGameCredentialRejected(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"code": 1001, "message": "token invalid"})
	})
	_, err := c.ExchangeGameCredential(context.Background(), "channel-token-secret", "42", strings.Repeat("A", 32))
	if !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("got %v", err)
	}
}

func TestRefreshPersistBeforeAccess(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != "refresh-old-secret" {
			t.Error("wrong refresh credential")
		}
		refreshed(w)
	})
	var s *Session
	var called bool
	s = NewSession(c, testGrant(), func(g Grant) error {
		called = true
		if g.RefreshToken != "refresh-new-secret" {
			t.Error("rotation not persisted")
		}
		if s.Grant().RefreshToken != "refresh-old-secret" {
			t.Error("enabled before persist")
		}
		return nil
	})
	a, err := s.Access(context.Background())
	if err != nil || !called || a.Token != "access-new-secret" {
		t.Fatalf("refresh err=%v called=%v access=%v", err, called, a)
	}
	if s.Grant().RefreshToken != "refresh-new-secret" {
		t.Fatal("new grant not active")
	}
}
func TestConcurrentRefreshSingleFlight(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
		}
		<-release
		refreshed(w)
	})
	s := NewSession(c, testGrant(), func(Grant) error { return nil })
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Go(func() { _, err := s.Access(context.Background()); errs <- err })
	}
	<-entered
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("refresh requests %d", requests.Load())
	}
}
func TestPersistFailureKeepsMemoryAndMarksRelink(t *testing.T) {
	var n atomic.Int32
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); refreshed(w) })
	s := NewSession(c, testGrant(), func(Grant) error { return errors.New("disk secret-do-not-wrap") })
	for range 2 {
		a, err := s.Access(context.Background())
		if !errors.Is(err, ErrNeedsRelink) || a.Token != "access-new-secret" || !s.NeedsRelink() {
			t.Fatalf("access=%v err=%v", a, err)
		}
		if strings.Contains(fmt.Sprint(err), "secret-do-not-wrap") {
			t.Fatal("unsafe persist error")
		}
	}
	if n.Load() != 1 {
		t.Fatal("retried after persist failure")
	}
}
func TestAuthExpiryHTTPAndBusiness(t *testing.T) {
	for _, code := range []int{401, 402, 403} {
		for _, httpStatus := range []bool{false, true} {
			t.Run(fmt.Sprint(code, httpStatus), func(t *testing.T) {
				c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
					if httpStatus {
						w.WriteHeader(code)
					}
					fmt.Fprintf(w, `{"code":%d,"msg":"access-secret"}`, code)
				})
				_, _, err := c.Refresh(context.Background(), testGrant())
				if !errors.Is(err, ErrAuthExpired) {
					t.Fatalf("got %v", err)
				}
			})
		}
	}
}
func gachaPayload() map[string]any {
	return map[string]any{"userid": "9_42", "roleid": "123", "luckType": 7, "luckTitle": "称号", "gachaDetails": []any{map[string]any{"tab": "限定卡池", "m": 91, "drawCount": 100, "rareCount": 2, "average": "50", "playerOver": "60%", "details": []any{map[string]any{"charid": "1001", "rareCount": 50, "luckyType": 2, "timeStamp": 1730000000000}, map[string]any{"charid": "1001", "rareCount": 50, "luckyType": 2, "timeStamp": 1730000000000}}}}}
}
func TestGachaConversionDedupAndMetadata(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Error("gacha query must be empty")
		}
		reply(w, gachaPayload())
	})
	s, err := c.Gacha(context.Background(), testAccess())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Pools) != 1 || len(s.Pools[0].Records) != 1 || s.Pools[0].Records[0].Pool != gacha.Limited || s.LuckType != 7 || s.Pools[0].DrawCount != 100 {
		t.Fatalf("bad snapshot %+v", s)
	}
	a, _, err := gacha.Merge(gacha.Archive{RoleID: "123"}, s)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Snapshots[0].Pools[0].RuleMismatch {
		t.Fatal("missing rule mismatch")
	}
}
func TestGachaAcceptsNewLuckTier(t *testing.T) {
	p := gachaPayload()
	for _, detail := range p["gachaDetails"].([]any)[0].(map[string]any)["details"].([]any) {
		detail.(map[string]any)["luckyType"] = 4
	}
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, p) })
	s, err := c.Gacha(context.Background(), testAccess())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Pools) != 1 || len(s.Pools[0].Records) != 1 || s.Pools[0].Records[0].Luck != 4 {
		t.Fatalf("bad snapshot %+v", s)
	}
}

func TestGachaRejectsIdentityAndStructure(t *testing.T) {
	for _, kind := range []string{"userid", "roleid", "tab", "structure", "details"} {
		t.Run(kind, func(t *testing.T) {
			p := gachaPayload()
			want := ErrProtocolChanged
			switch kind {
			case "userid", "roleid":
				p[kind] = "wrong"
				want = ErrIdentityMismatch
			case "tab":
				p["gachaDetails"].([]any)[0].(map[string]any)["tab"] = "new-pool"
			case "structure":
				delete(p, "gachaDetails")
			case "details":
				delete(p["gachaDetails"].([]any)[0].(map[string]any), "details")
			}
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, p) })
			s, err := c.Gacha(context.Background(), testAccess())
			if !errors.Is(err, want) || len(s.Pools) != 0 {
				t.Fatalf("snapshot=%+v err=%v", s, err)
			}
		})
	}
}
func TestChallengeStopsWithoutRetry(t *testing.T) {
	var n int
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		io.WriteString(w, `{"code":1001,"message":"需要滑块验证","result":{"captchaUrl":"https://invalid/"}}`)
	})
	if err := c.SendSMS(context.Background(), "13800000000"); !errors.Is(err, ErrChallengeRequired) {
		t.Fatalf("got %v", err)
	}
	if n != 1 {
		t.Fatal("retried challenge")
	}
}
func TestCredentialRedaction(t *testing.T) {
	g := testGrant()
	a := testAccess()
	s := NewSession(&Client{}, g, nil)
	for _, v := range []any{g, &g, a, &a, s, Config{BaseURL: "refresh-old-secret"}} {
		var b bytes.Buffer
		fmt.Fprintf(&b, "%v %+v %#v %s %q", v, v, v, v, v)
		slog.New(slog.NewJSONHandler(&b, nil)).Info("credentials", "value", v)
		for _, secret := range []string{g.RefreshToken, a.Token} {
			if strings.Contains(b.String(), secret) {
				t.Fatalf("credential leak for %T", v)
			}
		}
	}
}
func TestCharactersNamesAndFork(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("roleId") != "123" {
			t.Error("missing selected role")
		}
		reply(w, []any{map[string]any{"id": "1001", "name": "名字角色卡", "fork": map[string]any{"id": "fork_test", "name": "弧盘"}}})
	})
	cat, err := c.Characters(context.Background(), testAccess())
	if err != nil || cat.Items["1001"].Name != "名字" || cat.Items["fork_test"].Kind != gacha.Arc {
		t.Fatalf("catalog=%+v err=%v", cat, err)
	}
}
