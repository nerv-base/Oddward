// SPDX-License-Identifier: GPL-3.0-or-later
package laohu

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const configJSON = `{"meta":{"version":"9.8.7","packageName":"com.example.nte","channelId":9,"platform":"4"},"init":{"channel":{"channelAppId":10001,"channelAppKey":"0123456789abcdefghijklmnopqrstuv"}}}`
const channelJSON = `{"code":0,"result":{"userId":42,"token":"synthetic-token"}}`
const gameJSON = `{"code":0,"uid":"42","token":"synthetic-game-token","extraOne":{"note":"中文<&","n":1}}`
const preLoginJSON = `{"code":0,"message":"ok","result":{"status":"0","realUser":1,"bannedType":null,"appId":1289,"userId":"42"}}`

func TestParseConfigStrict(t *testing.T) {
	got, err := ParseConfig([]byte(configJSON))
	if err != nil || got != testConfig() {
		t.Fatalf("config mismatch: %v", err)
	}
	for _, body := range []string{
		strings.Replace(configJSON, `"platform":"4"`, `"platform":"4","platform":4`, 1),
		strings.Replace(configJSON, `"channelId":9`, `"channelId":8`, 1),
		strings.Replace(configJSON, `"channelAppId":10001`, `"channelAppId":null`, 1),
		strings.Replace(configJSON, `"version":"9.8.7"`, `"version":{}`, 1),
		configJSON + `{}`, `[]`,
	} {
		if _, err := ParseConfig([]byte(body)); err == nil {
			t.Errorf("invalid config accepted")
		}
	}
}
func TestIdentityParsersStrict(t *testing.T) {
	ch, err := ParseChannelIdentity([]byte(channelJSON))
	if err != nil || string(ch.UID) != "42" || string(ch.Token) != "synthetic-token" {
		t.Fatalf("channel identity: %v", err)
	}
	game, err := ParseGameIdentity([]byte(gameJSON))
	if err != nil || string(game.UID) != "42" || string(game.ExtraOne) != `{"n":1,"note":"中文<&"}` {
		t.Fatalf("game identity: %v", err)
	}
	for _, body := range []string{
		`{"code":0,"code":0,"result":{"userId":42,"token":"x"}}`,
		`{"code":0,"result":{"userId":42,"token":"x","token":"y"}}`,
		`{"code":0,"result":{"userId":0,"token":"x"}}`,
		`{"code":0,"result":{"userId":-1,"token":"x"}}`,
		`{"code":0,"result":{"userId":1.5,"token":"x"}}`,
		`{"code":0,"result":{"userId":" ","token":"x"}}`,
		`{"code":0,"result":{"userId":"x","token":"a\u0001"}}`,
		`{"code":0,"result":{"userId":42,"token":123}}`,
		`{"code":0,"result":{"userId":42,"token":"` + strings.Repeat("x", 128) + `"}}`,
		`{"code":"0","result":{"userId":42,"token":"x"}}`,
		`{"code":0,"result":{"userId":42,"token":"x"},"other":{"a":1,"a":2}}`,
		channelJSON + `null`,
	} {
		if _, err := ParseChannelIdentity([]byte(body)); err == nil {
			t.Errorf("invalid channel response accepted: %s", body)
		}
	}
	for _, body := range []string{
		strings.Replace(gameJSON, `"uid":"42"`, `"uid":"42","uid":"43"`, 1),
		strings.Replace(gameJSON, `"extraOne":{"note":"中文<&","n":1}`, `"extraOne":[]`, 1),
		`{"code":17,"uid":"42","token":"x"}`,
	} {
		if _, err := ParseGameIdentity([]byte(body)); err == nil {
			t.Error("invalid game identity accepted")
		}
	}
	var e *Error
	_, err = ParseChannelIdentity([]byte(`{"code":17}`))
	if !errors.As(err, &e) || e.Phase != PhaseChannelLoginRejected || !e.HasAPICode || e.APICode != 17 {
		t.Fatalf("rejection code lost: %v", err)
	}
}
func TestParseValidation(t *testing.T) {
	for _, tc := range []struct {
		body string
		want ValidationStatus
	}{
		{`{"code":0}`, Validated}, {`{"code":0,"result":null}`, Validated},
		{`{"code":0,"result":{"privacyAgreement":null}}`, Validated},
		{`{"code":0,"result":{"privacyAgreement":false}}`, RequiresOfficialInteraction},
		{`{"code":15}`, ServiceRejected}, {`{"code":0,"result":[]}`, Unknown},
		{`{"code":0,"code":0}`, Unknown}, {`{"code":"0"}`, Unknown},
	} {
		if got := ParseValidation([]byte(tc.body)); got.Status != tc.want {
			t.Errorf("validation %s: %v", tc.body, got.Status)
		}
	}
}
func TestPreLoginBindingAndStrictParsing(t *testing.T) {
	raw := []byte(preLoginJSON)
	r, err := ParsePreLogin(raw, []byte("42"))
	if err != nil || r.Code != 0 || r.RealUser != 1 || r.Status != 0 || r.BannedType != 0 || !bytes.Equal(r.Body, raw) {
		t.Fatalf("prelogin: %v", err)
	}
	raw[0] = '!'
	if r.Body[0] != '{' {
		t.Fatal("response aliases caller buffer")
	}
	for _, body := range []string{
		strings.Replace(preLoginJSON, `"userId":"42"`, `"userId":"43"`, 1),
		strings.Replace(preLoginJSON, `"userId":"42"`, `"userId":42`, 1),
		strings.Replace(preLoginJSON, `"appId":1289`, `"appId":1288`, 1),
		strings.Replace(preLoginJSON, `"status":"0"`, `"status":"0","status":0`, 1),
		strings.Replace(preLoginJSON, `"bannedType":null,`, ``, 1),
		strings.Replace(preLoginJSON, `"bannedType":null`, `"bannedType":"0"`, 1),
		strings.Replace(preLoginJSON, `"message":"ok"`, `"message":"\u0000"`, 1),
		strings.Replace(preLoginJSON, `"message":"ok"`, `"message":"`+strings.Repeat("界", 512)+`"`, 1),
	} {
		if _, err := ParsePreLogin([]byte(body), []byte("42")); err == nil {
			t.Error("invalid prelogin accepted")
		}
	}
	r, err = ParsePreLogin([]byte(`{"code":19,"message":"no"}`), []byte("42"))
	if err != nil || r.Code != 19 {
		t.Fatal("service rejection should be a parsed response")
	}
}
func TestSecretsAlwaysRedactedAndWiped(t *testing.T) {
	k := testKey(t)
	derived := NewDerivedAppKey(k.tail, k.preLogin)
	if derived != k {
		t.Fatal("derived key differs")
	}
	ch, _ := ParseChannelIdentity([]byte(channelJSON))
	game, _ := ParseGameIdentity([]byte(gameJSON))
	r, _ := ParsePreLogin([]byte(preLoginJSON), []byte("42"))
	for _, v := range []any{testConfig(), &k, k, ch, &ch, game, &game, r, &r} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
			text := fmt.Sprintf(format, v)
			for _, secret := range []string{testChannelAppKey, "QRSTUVWXYZ012345", "synthetic-token", "synthetic-game-token", "中文<&", `"realUser"`} {
				if strings.Contains(text, secret) {
					t.Errorf("format %s leaks secret", format)
				}
			}
		}
	}
	uid, token, info, body := ch.UID, ch.Token, game.ExtraOne, r.Body
	ch.Zero()
	game.Zero()
	r.Zero()
	k.Zero()
	for _, b := range [][]byte{uid, token, info, body, k.tail[:], k.preLogin[:]} {
		if !bytes.Equal(b, make([]byte, len(b))) {
			t.Fatal("secret not cleared")
		}
	}
	if k.valid {
		t.Fatal("cleared key still valid")
	}
}
func TestErrorExplanationSanitization(t *testing.T) {
	s := SafeExplanation("联系 test@example.com https://x.test/token 13800138000 ABCDEFGHIJKLMNOPQRSTUVWXYZ012345\n重试")
	if strings.Contains(s, "test@example") || strings.Contains(s, "https") || strings.Contains(s, "13800138000") || strings.Contains(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") || strings.Contains(s, "\n") {
		t.Fatalf("unsafe explanation: %q", s)
	}
	if SafeExplanation(strings.Repeat("x", 257)) != "" {
		t.Fatal("oversize explanation accepted")
	}
	e := &Error{Phase: PhaseHTTPFailure, StatusCode: 400, APICode: 17, HasAPICode: true, explanation: "local display only"}
	if !strings.Contains(e.Message(), "local display only") {
		t.Fatal("HTTP message lost explanation")
	}
	for _, f := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		if strings.Contains(fmt.Sprintf(f, e), "local display") {
			t.Fatal("formatted error leaks display text")
		}
	}
}
func TestAnonymousAndDeviceEncoding(t *testing.T) {
	c := testConfig()
	c.PackageName = "中文<&>'+"
	p := anonymousParams(c, testTime, testDeviceID, testOSVersion)
	if p["t"] != "1700000000" || p["deviceType"] != testOSVersion+" 64bit" || p["appVersion"] != "9.8.7" {
		t.Fatal("anonymous parameters differ")
	}
	d := deviceJSON(c, testTime, testDeviceID, testOSVersion)
	if !strings.Contains(d, `\u4E2D\u6587\u003C\u0026\u003E\u0027\u002B`) || strings.Contains(d, `"sign"`) || strings.Contains(d, `"appId"`) || strings.Contains(d, `"channelId"`) || strings.Contains(d, `"t"`) {
		t.Fatalf("device JSON: %s", d)
	}
}
