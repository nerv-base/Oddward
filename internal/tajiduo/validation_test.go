// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestAuthCodeWinsOverChallengeMessage(t *testing.T) {
	for _, code := range []string{"401", "402", "403"} {
		t.Run(code, func(t *testing.T) {
			c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, `{"code":`+code+`,"msg":"challenge required","data":{"captchaUrl":"https://invalid/"}}`)
			})
			if _, _, e := c.Refresh(context.Background(), testGrant()); !errors.Is(e, ErrAuthExpired) {
				t.Fatalf("got %v", e)
			}
		})
	}
}
func TestInvalidUTF8IsProtocolChange(t *testing.T) {
	c := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "{\"code\":0,\"data\":{\"accessToken\":\"new\",\"refreshToken\":\"bad\xfftoken\"}}")
	})
	if _, _, e := c.Refresh(context.Background(), testGrant()); !errors.Is(e, ErrProtocolChanged) {
		t.Fatalf("got %v", e)
	}
}
