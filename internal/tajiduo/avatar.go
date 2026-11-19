// SPDX-License-Identifier: GPL-3.0-or-later

package tajiduo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultAvatarBase is the official community CDN path used by the game-record
// pages. Avatars are public pictures, not credentials, and the request carries
// no account header.
const defaultAvatarBase = "https://webstatic.tajiduo.com/bbs/yh-game-records-web-source/avatar/square/"

const maxAvatarBytes = 1 << 20

// Avatar downloads one in-game portrait by the role-home avatar id. Only a
// small, safe id is accepted so the value can never select another host or
// escape the avatar directory.
func (c *Client) Avatar(ctx context.Context, id string) ([]byte, error) {
	if c == nil || c.http == nil || c.avatarBase == "" || !validAvatarID(id) {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	u := strings.TrimRight(c.avatarBase, "/") + "/" + url.PathEscape(id) + ".PNG"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, ErrInvalidInput
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: avatar request failed", ErrNetwork)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: avatar status %d", ErrProtocolChanged, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		return nil, fmt.Errorf("%w: avatar content type", ErrProtocolChanged)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatarBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: avatar read failed", ErrNetwork)
	}
	if len(data) == 0 || len(data) > maxAvatarBytes {
		return nil, fmt.Errorf("%w: avatar size", ErrProtocolChanged)
	}
	return data, nil
}

func validAvatarID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
