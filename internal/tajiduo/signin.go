// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// GameSignState is the server's own answer about this calendar month for one
// account and game. It carries no role identity, so it can only prove that
// somebody on this account signed; the role check happens on the sign call.
type GameSignState struct {
	Day       int // Server's current day of the month; anchors "today".
	Days      int // Signed days this month.
	Month     int
	ReSignCnt int
	TodaySign bool
}

// SignReward is one entry of the monthly reward table.
type SignReward struct {
	Item  string
	Count int
}

// CommunityReward is what the community sign-in reports back.
type CommunityReward struct {
	Exp, Coin int
	Known     bool
}

// RoleHome is the role status the official role-home page shows.
type RoleHome struct {
	UserID, RoleID, RoleName       string
	ServerID, ServerName, Avatar   string
	Level, WorldLevel, TycoonLevel int
	LoginDays, CharCount           int
	Stamina, StaminaMax            int
	CityStamina, CityStaminaMax    int
	DayValue, WeekCopiesRemain     int
	Achievement, AchievementTotal  int
	FetchedAt                      time.Time
}

func (c *Client) GameSignState(ctx context.Context, a Access) (GameSignState, error) {
	if !validAccess(a) {
		return GameSignState{}, ErrInvalidInput
	}
	v, e := c.request(ctx, false, http.MethodGet, "/apihub/awapi/signin/state", map[string]string{"gameId": gameID}, a)
	if e != nil {
		return GameSignState{}, e
	}
	m, ok := object(v)
	if !ok {
		return GameSignState{}, ErrProtocolChanged
	}
	day, okDay := integer(m["day"])
	days, okDays := integer(m["days"])
	month, okMonth := integer(m["month"])
	resign, okResign := integer(m["reSignCnt"])
	today, okToday := m["todaySign"].(bool)
	if !okDay || !okDays || !okMonth || !okResign || !okToday {
		return GameSignState{}, ErrProtocolChanged
	}
	if day < 0 || day > 31 || days < 0 || days > 31 || resign < 0 || resign > 31 || month < 0 || month > 12 {
		return GameSignState{}, ErrProtocolChanged
	}
	return GameSignState{Day: day, Days: days, Month: month, ReSignCnt: resign, TodaySign: today}, nil
}

// GameSign submits today's game check-in. A non-zero business code that means
// "already signed" is reported as ErrAlreadySigned instead of a failure, so the
// caller can distinguish "already done" from "rejected" without retrying.
func (c *Client) GameSign(ctx context.Context, a Access) error {
	if e := c.checkRole(a); e != nil {
		return e
	}
	_, e := c.writeSign(ctx, "/apihub/awapi/sign", map[string]string{"roleId": a.RoleID, "gameId": gameID}, a)
	return e
}

// GameRewards lists the monthly game rewards. Index i is the reward for the
// i-th signed day, so the freshly claimed reward is at Days-1 after signing.
func (c *Client) GameRewards(ctx context.Context, a Access) ([]SignReward, error) {
	if e := c.checkRole(a); e != nil {
		return nil, e
	}
	v, e := c.request(ctx, false, http.MethodGet, "/apihub/awapi/sign/rewards", map[string]string{"gameId": gameID, "roleId": a.RoleID}, a)
	if e != nil {
		return nil, e
	}
	list, ok := v.([]any)
	if !ok {
		m, okObject := object(v)
		if !okObject {
			return nil, ErrProtocolChanged
		}
		for _, key := range []string{"items", "rewards", "list"} {
			if l, exists := m[key].([]any); exists {
				list, ok = l, true
				break
			}
		}
		if !ok {
			return nil, ErrProtocolChanged
		}
	}
	out := make([]SignReward, 0, len(list))
	for _, v := range list {
		m, ok := object(v)
		if !ok {
			return nil, ErrProtocolChanged
		}
		name, okName := text(m["name"])
		count, okCount := integer(m["num"])
		if name == "" {
			// A day the server has not filled in yet. Keep the slot: callers
			// index this table by the day of the month.
			out = append(out, SignReward{})
			continue
		}
		if !okName || !okCount || count < 0 || count > 1000000 {
			return nil, ErrProtocolChanged
		}
		out = append(out, SignReward{Item: strings.TrimSpace(name), Count: count})
	}
	return out, nil
}

// CommunitySignState is the account-wide community sign-in answer. The value is
// a bare JSON boolean; anything else is treated as a protocol change.
func (c *Client) CommunitySignState(ctx context.Context, a Access) (bool, error) {
	if !validAccess(a) {
		return false, ErrInvalidInput
	}
	v, e := c.request(ctx, false, http.MethodGet, "/apihub/api/getSignState", map[string]string{"communityId": communityID}, a)
	if e != nil {
		return false, e
	}
	signed, ok := v.(bool)
	if !ok {
		return false, ErrProtocolChanged
	}
	return signed, nil
}

// CommunitySign submits today's community check-in. Community sign-in is
// account-scoped; unlike the game sign-in it carries no role.
func (c *Client) CommunitySign(ctx context.Context, a Access) (CommunityReward, error) {
	if !validAccess(a) {
		return CommunityReward{}, ErrInvalidInput
	}
	root, e := c.writeSign(ctx, "/apihub/api/signin", map[string]string{"communityId": communityID}, a)
	if e != nil {
		return CommunityReward{}, e
	}
	out := CommunityReward{}
	m, ok := object(root["data"])
	if !ok {
		return out, nil
	}
	exp, okExp := integer(m["exp"])
	coin, okCoin := integer(m["goldCoin"])
	if okExp && okCoin && exp >= 0 && coin >= 0 && exp <= 1000000 && coin <= 1000000 {
		out = CommunityReward{Exp: exp, Coin: coin, Known: true}
	}
	return out, nil
}

func (c *Client) RoleHome(ctx context.Context, a Access) (RoleHome, error) {
	if e := c.checkRole(a); e != nil {
		return RoleHome{}, e
	}
	v, e := c.request(ctx, false, http.MethodGet, "/apihub/awapi/yh/roleHome", map[string]string{"roleId": a.RoleID}, a)
	if e != nil {
		return RoleHome{}, e
	}
	m, ok := object(v)
	if !ok {
		return RoleHome{}, ErrProtocolChanged
	}
	user, okUser := text(m["userid"])
	role, okRole := text(m["roleid"])
	name, okName := text(m["rolename"])
	if !okUser || !okRole || !okName || strings.TrimSpace(name) == "" {
		return RoleHome{}, ErrProtocolChanged
	}
	if role == "" || role == "0" {
		return RoleHome{}, ErrNoBoundRole
	}
	if user != "9_"+a.LaohuUserID || role != a.RoleID {
		return RoleHome{}, ErrIdentityMismatch
	}
	level, okLevel := integer(m["lev"])
	world, okWorld := integer(m["worldlevel"])
	stamina, okStamina := integer(m["staminaValue"])
	staminaMax, okStaminaMax := integer(m["staminaMaxValue"])
	city, okCity := integer(m["citystaminaValue"])
	cityMax, okCityMax := integer(m["citystaminaMaxValue"])
	dayValue, okDay := integer(m["dayvalue"])
	week, okWeek := integer(m["weekcopiesremainCnt"])
	if !okLevel || !okWorld || !okStamina || !okStaminaMax || !okCity || !okCityMax || !okDay || !okWeek {
		return RoleHome{}, ErrProtocolChanged
	}
	if level < 0 || level > 1000 || world < 0 || world > 100 || dayValue < 0 || dayValue > 100 {
		return RoleHome{}, ErrProtocolChanged
	}
	if stamina < 0 || staminaMax <= 0 || stamina > 10000000 || staminaMax > 10000000 {
		return RoleHome{}, ErrProtocolChanged
	}
	if city < 0 || cityMax <= 0 || city > 10000000 || cityMax > 10000000 {
		return RoleHome{}, ErrProtocolChanged
	}
	if week < 0 || week > 3 {
		return RoleHome{}, ErrProtocolChanged
	}
	out := RoleHome{
		UserID: user, RoleID: role, RoleName: strings.TrimSpace(name),
		Level: level, WorldLevel: world,
		Stamina: stamina, StaminaMax: staminaMax,
		CityStamina: city, CityStaminaMax: cityMax,
		DayValue: dayValue, WeekCopiesRemain: week,
		FetchedAt: c.now().UTC(),
	}
	// Everything below is decoration: absent values stay zero and the UI omits
	// them instead of inventing numbers.
	if s, ok := text(m["serverid"]); ok {
		out.ServerID = strings.TrimSpace(s)
	}
	if s, ok := text(m["servername"]); ok {
		out.ServerName = strings.TrimSpace(s)
	}
	if s, ok := text(m["avatar"]); ok {
		out.Avatar = s
	}
	if tycoon, ok := integer(m["tycoonLevel"]); ok && tycoon >= 0 && tycoon <= 100 {
		out.TycoonLevel = tycoon
	}
	if days, ok := integer(m["roleloginDays"]); ok && days >= 0 && days <= 100000 {
		out.LoginDays = days
	}
	if count, ok := integer(m["charidCnt"]); ok && count >= 0 && count <= 10000 {
		out.CharCount = count
	}
	if p, ok := object(m["achieveProgress"]); ok {
		got, okGot := integer(p["achievementCnt"])
		total, okTotal := integer(p["total"])
		if okGot && okTotal && got >= 0 && total >= 0 && got <= total {
			out.Achievement, out.AchievementTotal = got, total
		}
	}
	return out, nil
}

func (c *Client) checkRole(a Access) error {
	if !validAccess(a) {
		return ErrInvalidInput
	}
	if a.RoleID == "" || a.RoleID == "0" {
		return ErrNoBoundRole
	}
	if !digits(a.RoleID) {
		return ErrInvalidInput
	}
	return nil
}

// writeSign performs a sign-in write and classifies the business code. A code
// that says the write already happened is a distinct answer, never a retry.
func (c *Client) writeSign(ctx context.Context, path string, fields map[string]string, a Access) (map[string]any, error) {
	root, status, e := c.exchange(ctx, false, http.MethodPost, path, fields, a)
	if e != nil {
		return nil, e
	}
	code, ok := codeValue(root["code"])
	if !ok {
		return nil, ErrProtocolChanged
	}
	if code == 0 {
		if status < 200 || status >= 300 {
			return nil, ErrProtocolChanged
		}
		return root, nil
	}
	if alreadySigned(root) {
		return nil, ErrAlreadySigned
	}
	return nil, ErrProtocolChanged
}

func alreadySigned(root map[string]any) bool {
	msg, ok := text(root["msg"])
	if !ok {
		msg, _ = text(root["message"])
	}
	for _, hint := range []string{"已签到", "签到过", "重复签到", "已经签到"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}
