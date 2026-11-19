// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gacha"
)

func (c *Client) Roles(ctx context.Context, a Access) (RoleList, error) {
	if !validAccess(a) {
		return RoleList{}, ErrInvalidInput
	}
	v, e := c.request(ctx, false, http.MethodGet, "/apihub/api/getGameBindRole", map[string]string{"uid": a.CenterUID, "gameId": gameID}, a)
	if e != nil {
		return RoleList{}, e
	}
	bound, ok := object(v)
	if !ok {
		return RoleList{}, ErrProtocolChanged
	}
	boundID, ok := optionalRole(bound["roleId"])
	if !ok {
		return RoleList{}, ErrProtocolChanged
	}
	v, e = c.request(ctx, false, http.MethodGet, "/usercenter/api/v2/getGameRoles", map[string]string{"gameId": gameID}, a)
	if e != nil {
		return RoleList{}, e
	}
	var roles []any
	if m, ok := object(v); ok {
		b, exists := m["bindRole"]
		if !exists {
			return RoleList{}, ErrProtocolChanged
		}
		boundID, ok = optionalRole(b)
		if !ok {
			return RoleList{}, ErrProtocolChanged
		}
		roles, ok = m["roles"].([]any)
		if !ok {
			return RoleList{}, ErrProtocolChanged
		}
	} else {
		var ok bool
		roles, ok = v.([]any)
		if !ok {
			return RoleList{}, ErrProtocolChanged
		}
	}
	out := RoleList{BoundRoleID: boundID, Roles: []Role{}}
	seen := map[string]bool{}
	for _, v := range roles {
		m, ok := object(v)
		if !ok {
			return RoleList{}, ErrProtocolChanged
		}
		id, ok := identifier(m["roleId"])
		name, okName := text(m["roleName"])
		if !ok || !okName || seen[id] {
			return RoleList{}, ErrProtocolChanged
		}
		seen[id] = true
		out.Roles = append(out.Roles, Role{ID: id, Name: name})
	}
	return out, nil
}
func optionalRole(v any) (string, bool) {
	if s, ok := text(v); ok && s == "0" {
		return "", true
	}
	if n, ok := integer(v); ok && n == 0 {
		return "", true
	}
	return identifier(v)
}

// BindRole changes the main binding. Only an explicit user action may call it.
func (c *Client) BindRole(ctx context.Context, a Access, roleID string) error {
	if !validAccess(a) || !digits(roleID) || roleID == "0" {
		return ErrInvalidInput
	}
	v, e := c.request(ctx, false, http.MethodPost, "/usercenter/api/bindGameRole", map[string]string{"gameId": gameID, "roleId": roleID}, a)
	if e != nil {
		return e
	}
	b, ok := v.(bool)
	if !ok || !b {
		return ErrProtocolChanged
	}
	return nil
}
func (c *Client) Gacha(ctx context.Context, a Access) (Snapshot, error) {
	if !validAccess(a) {
		return Snapshot{}, ErrInvalidInput
	}
	if a.RoleID == "" || a.RoleID == "0" {
		return Snapshot{}, ErrNoBoundRole
	}
	if !digits(a.RoleID) {
		return Snapshot{}, ErrInvalidInput
	}
	v, e := c.request(ctx, false, http.MethodGet, "/apihub/awapi/yh/gacha", nil, a)
	if e != nil {
		return Snapshot{}, e
	}
	return snapshot(v, a, c.now())
}
func snapshot(v any, a Access, now time.Time) (Snapshot, error) {
	m, ok := object(v)
	if !ok {
		return Snapshot{}, ErrProtocolChanged
	}
	user, okUser := text(m["userid"])
	role, okRole := text(m["roleid"])
	if !okUser || !okRole {
		return Snapshot{}, ErrProtocolChanged
	}
	if role == "" || role == "0" {
		return Snapshot{}, ErrNoBoundRole
	}
	if user != "9_"+a.LaohuUserID || role != a.RoleID {
		return Snapshot{}, ErrIdentityMismatch
	}
	pools, ok := m["gachaDetails"].([]any)
	luck, okLuck := integer(m["luckType"])
	title, okTitle := text(m["luckTitle"])
	if !ok || !okLuck || !okTitle {
		return Snapshot{}, ErrProtocolChanged
	}
	out := Snapshot{RoleID: role, FetchedAt: time.UnixMilli(now.UnixMilli()).UTC(), LuckType: luck, LuckTitle: title, Pools: []gacha.PoolSnapshot{}}
	seenPools := map[gacha.Pool]bool{}
	for _, v := range pools {
		m, ok := object(v)
		if !ok {
			return Snapshot{}, ErrProtocolChanged
		}
		tab, ok := text(m["tab"])
		if !ok {
			return Snapshot{}, ErrProtocolChanged
		}
		pool, e := gacha.PoolFromTab(tab)
		if e != nil || seenPools[pool] {
			return Snapshot{}, ErrProtocolChanged
		}
		seenPools[pool] = true
		ceiling, okM := integer(m["m"])
		draw, okDraw := integer(m["drawCount"])
		rare, okRare := integer(m["rareCount"])
		average, okAverage := text(m["average"])
		over, okOver := text(m["playerOver"])
		details, okDetails := m["details"].([]any)
		if !okM || !okDraw || !okRare || !okAverage || !okOver || !okDetails || ceiling < 0 || draw < 0 || rare < 0 {
			return Snapshot{}, ErrProtocolChanged
		}
		p := gacha.PoolSnapshot{Pool: pool, M: ceiling, DrawCount: draw, RareCount: rare, Average: average, PlayerOver: over, Records: []gacha.Record{}}
		type key struct {
			item string
			ms   int64
			pity int
		}
		seen := map[key]int{}
		for _, v := range details {
			d, ok := object(v)
			if !ok {
				return Snapshot{}, ErrProtocolChanged
			}
			id, okID := text(d["charid"])
			pity, okPity := integer(d["rareCount"])
			luck, okLuck := integer(d["luckyType"])
			stamp, okStamp := d["timeStamp"].(interface{ Int64() (int64, error) })
			if !okID || !okPity || !okLuck || !okStamp || len(id) == 0 || len(id) > 64 || luck < 0 || luck > gacha.LuckMax {
				return Snapshot{}, ErrProtocolChanged
			}
			for _, b := range id {
				if b < 0x20 || b > 0x7e {
					return Snapshot{}, ErrProtocolChanged
				}
			}
			ms, e := stamp.Int64()
			if e != nil || ms <= 0 {
				return Snapshot{}, ErrProtocolChanged
			}
			k := key{id, ms, pity}
			if old, exists := seen[k]; exists {
				if old != luck {
					return Snapshot{}, ErrProtocolChanged
				}
				continue
			}
			seen[k] = luck
			p.Records = append(p.Records, gacha.Record{Pool: pool, ItemID: id, At: time.UnixMilli(ms).UTC(), Pity: pity, Luck: luck})
		}
		sort.Slice(p.Records, func(i, j int) bool {
			l, r := p.Records[i], p.Records[j]
			if !l.At.Equal(r.At) {
				return l.At.After(r.At)
			}
			if l.ItemID != r.ItemID {
				return l.ItemID < r.ItemID
			}
			return l.Pity < r.Pity
		})
		out.Pools = append(out.Pools, p)
	}
	return out, nil
}
func (c *Client) Characters(ctx context.Context, a Access) (Catalog, error) {
	if !validAccess(a) {
		return Catalog{}, ErrInvalidInput
	}
	if a.RoleID == "" || a.RoleID == "0" {
		return Catalog{}, ErrNoBoundRole
	}
	if !digits(a.RoleID) {
		return Catalog{}, ErrInvalidInput
	}
	v, e := c.request(ctx, false, http.MethodGet, "/apihub/awapi/yh/characters", map[string]string{"roleId": a.RoleID}, a)
	if e != nil {
		return Catalog{}, e
	}
	list, ok := v.([]any)
	if !ok {
		return Catalog{}, ErrProtocolChanged
	}
	out := Catalog{FetchedAt: c.now().UTC(), Items: map[string]gacha.Item{}}
	add := func(m map[string]any, allowEmpty bool) error {
		id, ok := text(m["id"])
		name, okName := text(m["name"])
		if allowEmpty && ok && id == "" {
			return nil
		}
		if !ok || !okName || strings.TrimSpace(name) == "" {
			return ErrProtocolChanged
		}
		kind := gacha.Kind(id)
		if kind == gacha.Unknown {
			return nil
		}
		name = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(name), "角色卡"))
		if name == "" {
			return ErrProtocolChanged
		}
		if old, exists := out.Items[id]; exists && old.Name != name {
			return ErrProtocolChanged
		}
		out.Items[id] = gacha.Item{Kind: kind, Name: name}
		return nil
	}
	for _, v := range list {
		m, ok := object(v)
		if !ok {
			return Catalog{}, ErrProtocolChanged
		}
		if e := add(m, false); e != nil {
			return Catalog{}, e
		}
		if v, exists := m["fork"]; exists {
			f, ok := object(v)
			if !ok {
				return Catalog{}, ErrProtocolChanged
			}
			if e := add(f, true); e != nil {
				return Catalog{}, e
			}
		}
	}
	return out, nil
}
