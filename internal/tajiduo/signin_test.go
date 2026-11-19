// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func encode(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Error(err)
	}
}

func TestGameSignStateReadsServerAnswer(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apihub/awapi/signin/state" || r.Method != http.MethodGet {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("gameId"); got != gameID {
			t.Errorf("gameId = %q", got)
		}
		if r.Header.Get("authorization") != "access-secret" || r.Header.Get("deviceid") == "" {
			t.Error("missing auth headers")
		}
		reply(w, map[string]any{"day": 17, "days": 16, "month": 11, "reSignCnt": 1, "todaySign": true})
	})
	st, err := c.GameSignState(context.Background(), testAccess())
	if err != nil {
		t.Fatal(err)
	}
	if st.Day != 17 || st.Days != 16 || st.Month != 11 || st.ReSignCnt != 1 || !st.TodaySign {
		t.Fatalf("state = %+v", st)
	}
}
func TestGameSignStateRejectsMissingFields(t *testing.T) {
	for name, data := range map[string]any{
		"no todaySign": map[string]any{"day": 1, "days": 1, "month": 1, "reSignCnt": 0},
		"no days":      map[string]any{"day": 1, "month": 1, "reSignCnt": 0, "todaySign": false},
		"day text":     map[string]any{"day": "1", "days": 1, "month": 1, "reSignCnt": 0, "todaySign": false},
		"day range":    map[string]any{"day": 32, "days": 1, "month": 1, "reSignCnt": 0, "todaySign": false},
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, data) })
			if _, err := c.GameSignState(context.Background(), testAccess()); !errors.Is(err, ErrProtocolChanged) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
func TestGameSignSendsRoleAndDisambiguatesAlready(t *testing.T) {
	var calls atomic.Int32
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/apihub/awapi/sign" || r.Method != http.MethodPost {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		r.ParseForm()
		if r.Form.Get("roleId") != "123" || r.Form.Get("gameId") != gameID {
			t.Errorf("form = %v", r.Form)
		}
		encode(t, w, map[string]any{"code": 1001, "msg": "今日已签到，请勿重复签到"})
	})
	err := c.GameSign(context.Background(), testAccess())
	if !errors.Is(err, ErrAlreadySigned) {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
func TestGameSignSuccess(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		encode(t, w, map[string]any{"code": 0, "data": map[string]any{}})
	})
	if err := c.GameSign(context.Background(), testAccess()); err != nil {
		t.Fatal(err)
	}
}
func TestGameSignRejectsUnclassifiedBusinessCode(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		encode(t, w, map[string]any{"code": 2005, "msg": "系统繁忙"})
	})
	if err := c.GameSign(context.Background(), testAccess()); !errors.Is(err, ErrProtocolChanged) {
		t.Fatalf("err = %v", err)
	}
}
func TestGameSignAuthExpiryIsTyped(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		encode(t, w, map[string]any{"code": 401, "msg": "token expired"})
	})
	if err := c.GameSign(context.Background(), testAccess()); !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("err = %v", err)
	}
}
func TestGameSignRequiresRole(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	a := testAccess()
	a.RoleID = ""
	if err := c.GameSign(context.Background(), a); !errors.Is(err, ErrNoBoundRole) {
		t.Fatalf("err = %v", err)
	}
}
func TestCommunitySignStateReadsBareBoolean(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apihub/api/getSignState" || r.URL.Query().Get("communityId") != communityID {
			t.Errorf("got %s %v", r.URL.Path, r.URL.Query())
		}
		reply(w, true)
	})
	signed, err := c.CommunitySignState(context.Background(), testAccess())
	if err != nil || !signed {
		t.Fatalf("signed=%v err=%v", signed, err)
	}
}
func TestCommunitySignStateRejectsObject(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		reply(w, map[string]any{"signed": true})
	})
	if _, err := c.CommunitySignState(context.Background(), testAccess()); !errors.Is(err, ErrProtocolChanged) {
		t.Fatalf("err = %v", err)
	}
}
func TestCommunitySignSendsCommunityAndReadsReward(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apihub/api/signin" || r.Method != http.MethodPost {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		r.ParseForm()
		if r.Form.Get("communityId") != communityID {
			t.Errorf("communityId = %q", r.Form.Get("communityId"))
		}
		reply(w, map[string]any{"exp": 10, "goldCoin": 5})
	})
	rw, err := c.CommunitySign(context.Background(), testAccess())
	if err != nil {
		t.Fatal(err)
	}
	if !rw.Known || rw.Exp != 10 || rw.Coin != 5 {
		t.Fatalf("reward = %+v", rw)
	}
}
func TestCommunitySignAlreadySigned(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		encode(t, w, map[string]any{"code": 1002, "msg": "今天已经签到过了"})
	})
	if _, err := c.CommunitySign(context.Background(), testAccess()); !errors.Is(err, ErrAlreadySigned) {
		t.Fatalf("err = %v", err)
	}
}
func TestGameRewardsKeepTheirSlotPositions(t *testing.T) {
	cases := map[string]any{
		"list": []any{
			map[string]any{"icon": "a.png", "name": "金币", "num": 30},
			map[string]any{"icon": "", "name": "", "num": 0},
			map[string]any{"icon": "b.png", "name": "经验", "num": 5},
		},
		"wrapper": map[string]any{"rewards": []any{map[string]any{"name": "金币", "num": 30}}},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/apihub/awapi/sign/rewards" || r.URL.Query().Get("roleId") != "123" {
					t.Errorf("got %s %v", r.URL.Path, r.URL.Query())
				}
				reply(w, data)
			})
			got, err := c.GameRewards(context.Background(), testAccess())
			if err != nil {
				t.Fatal(err)
			}
			// The caller indexes this list by the day of the month, so an empty
			// slot has to keep its position instead of being dropped.
			if name == "list" {
				if len(got) != 3 {
					t.Fatalf("rewards = %+v, want one entry per server slot", got)
				}
				if got[0].Item != "金币" || got[0].Count != 30 || got[2].Item != "经验" || got[2].Count != 5 {
					t.Fatalf("rewards = %+v", got)
				}
			} else if len(got) != 1 || got[0].Item != "金币" || got[0].Count != 30 {
				t.Fatalf("rewards = %+v", got)
			}
		})
	}
}
func homePayload() map[string]any {
	return map[string]any{
		"userid": "9_42", "roleid": "123", "rolename": "夜莺", "serverid": "2001", "servername": "官服",
		"avatar": "https://example.invalid/a.png", "lev": 60, "worldlevel": 9, "tycoonLevel": 4,
		"roleloginDays": 88, "charidCnt": 27, "staminaValue": 240, "staminaMaxValue": 240,
		"citystaminaValue": 100, "citystaminaMaxValue": 160, "dayvalue": 100, "weekcopiesremainCnt": 2,
		"achieveProgress": map[string]any{"achievementCnt": 120, "total": 300},
		"areaProgress":    []any{}, "realestate": map[string]any{}, "vehicle": map[string]any{},
		"characters": []any{},
	}
}
func TestRoleHomeParsesStatus(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apihub/awapi/yh/roleHome" || r.URL.Query().Get("roleId") != "123" {
			t.Errorf("got %s %v", r.URL.Path, r.URL.Query())
		}
		reply(w, homePayload())
	})
	home, err := c.RoleHome(context.Background(), testAccess())
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case home.RoleName != "夜莺" || home.ServerName != "官服":
		t.Errorf("identity = %+v", home)
	case home.Level != 60 || home.WorldLevel != 9 || home.TycoonLevel != 4:
		t.Errorf("levels = %+v", home)
	case home.Stamina != 240 || home.StaminaMax != 240:
		t.Errorf("stamina = %d/%d", home.Stamina, home.StaminaMax)
	case home.CityStamina != 100 || home.CityStaminaMax != 160:
		t.Errorf("city = %d/%d", home.CityStamina, home.CityStaminaMax)
	case home.DayValue != 100 || home.WeekCopiesRemain != 2:
		t.Errorf("activity = %d week = %d", home.DayValue, home.WeekCopiesRemain)
	case home.Achievement != 120 || home.AchievementTotal != 300:
		t.Errorf("achievements = %d/%d", home.Achievement, home.AchievementTotal)
	case !home.FetchedAt.Equal(testTime):
		t.Errorf("fetched = %v", home.FetchedAt)
	}
}
func TestRoleHomeTreatsAbsentWeekCopiesAsZero(t *testing.T) {
	payload := homePayload()
	delete(payload, "weekcopiesremainCnt")
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, payload) })
	home, err := c.RoleHome(context.Background(), testAccess())
	if err != nil {
		t.Fatal(err)
	}
	if home.WeekCopiesRemain != 0 {
		t.Fatalf("week copies = %d", home.WeekCopiesRemain)
	}
}

func TestRoleHomeAllowsOverflowButRejectsIdentity(t *testing.T) {
	overflow := homePayload()
	overflow["staminaValue"] = 300 // Beyond the cap: real overflow, keep it.
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, overflow) })
	home, err := c.RoleHome(context.Background(), testAccess())
	if err != nil || home.Stamina != 300 || home.StaminaMax != 240 {
		t.Fatalf("home = %+v err = %v", home, err)
	}

	for name, mutate := range map[string]func(map[string]any){
		"other user":  func(m map[string]any) { m["userid"] = "9_99" },
		"other role":  func(m map[string]any) { m["roleid"] = "999" },
		"no role":     func(m map[string]any) { m["rolename"] = " " },
		"day range":   func(m map[string]any) { m["dayvalue"] = 101 },
		"week range":  func(m map[string]any) { m["weekcopiesremainCnt"] = 4 },
		"no stamina":  func(m map[string]any) { delete(m, "staminaMaxValue") },
		"zero max":    func(m map[string]any) { m["citystaminaMaxValue"] = 0 },
		"level range": func(m map[string]any) { m["lev"] = 5000 },
	} {
		t.Run(name, func(t *testing.T) {
			payload := homePayload()
			mutate(payload)
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, payload) })
			_, err := c.RoleHome(context.Background(), testAccess())
			want := ErrProtocolChanged
			if name == "other user" || name == "other role" {
				want = ErrIdentityMismatch
			}
			if !errors.Is(err, want) {
				t.Fatalf("err = %v want %v", err, want)
			}
		})
	}
}
func TestRoleHomeOmitsAbsentDecorations(t *testing.T) {
	payload := homePayload()
	for _, key := range []string{"serverid", "servername", "avatar", "tycoonLevel", "roleloginDays", "charidCnt", "achieveProgress"} {
		delete(payload, key)
	}
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) { reply(w, payload) })
	home, err := c.RoleHome(context.Background(), testAccess())
	if err != nil {
		t.Fatal(err)
	}
	if home.ServerName != "" || home.TycoonLevel != 0 || home.LoginDays != 0 || home.AchievementTotal != 0 {
		t.Fatalf("absent fields invented: %+v", home)
	}
}
func TestSignWriteRejectsNonSuccessStatus(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		encode(t, w, map[string]any{"code": 0, "data": map[string]any{}})
	})
	if err := c.GameSign(context.Background(), testAccess()); !errors.Is(err, ErrProtocolChanged) {
		t.Fatalf("err = %v", err)
	}
}
func TestWriteSignKeepsServerMessageOut(t *testing.T) {
	var seen string
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		encode(t, w, map[string]any{"code": 9, "msg": "详见 " + strings.Repeat("x", 32)})
	})
	err := c.GameSign(context.Background(), testAccess())
	seen = err.Error()
	if strings.Contains(seen, "xxx") || strings.Contains(seen, "access-secret") {
		t.Fatalf("error leaks upstream text: %q", seen)
	}
}
