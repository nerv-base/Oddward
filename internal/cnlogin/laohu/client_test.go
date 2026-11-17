// SPDX-License-Identifier: GPL-3.0-or-later
package laohu

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}
func newTestClient(t *testing.T, clock *fakeClock, handler transportFunc) *Client {
	t.Helper()
	c, err := NewClient(testConfig(), Options{HTTPClient: &http.Client{Transport: handler}, InstalledVersion: testVersion, DeviceID: testDeviceID, OSVersion: testOSVersion, AppKey: testKey(t), Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func assertPhase(t *testing.T, err error, want Phase) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Phase != want {
		t.Fatalf("phase = %v, want %s", err, want)
	}
}
func TestClientAuthenticationSequence(t *testing.T) {
	clock := &fakeClock{testTime}
	step := 0
	key := testKey(t)
	gameWire, err := encodeEnvelope([]byte(gameJSON), key.tail)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"/m/newApi/initConfig", "/m/newApi/query/whetherSetPassword", "/m/newApi/sendPhoneCaptchaWithOutLogin", "/openApi/sms/new/login", "/openApi/sdkValidate", "/sdkapi/v2/user/login", "/sdkapi/preLogin"}
	c := newTestClient(t, clock, func(r *http.Request) (*http.Response, error) {
		if step >= len(paths) {
			t.Fatal("unexpected retry")
		}
		if r.URL.Scheme != "https" || r.URL.Path != paths[step] {
			t.Fatalf("step %d request path %s", step, r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Cookie") != "" {
			t.Fatal("unexpected accept/cookie")
		}
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 20*time.Second {
			t.Fatal("request lacks bounded deadline")
		}
		if step == 4 {
			if r.Header.Get("User-Agent") != validationUserAgent {
				t.Fatal("validation UA missing")
			}
		} else if r.Header.Get("User-Agent") != "" {
			t.Fatal("unexpected UA")
		}
		body, _ := io.ReadAll(r.Body)
		if step == 1 {
			if r.Method != "GET" || len(body) != 0 || r.URL.Query().Get("cellphone") != testPhone {
				t.Fatal("wrong password-state query")
			}
		} else if r.Method != "POST" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Fatal("wrong POST encoding")
		}
		if step != 5 {
			encoded := string(body)
			if step == 1 {
				encoded = r.URL.RawQuery
			}
			v, err := url.ParseQuery(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if v.Encode() != encoded {
				t.Fatal("form is not ordinal sorted")
			}
			if step < 6 && v.Get("appId") != "10001" {
				t.Fatal("wrong channel appId")
			}
			switch step {
			case 0:
				if v.Get("sign") != "a3dde382c3ddba17a746728a5f4ba826" {
					t.Fatal("init signature differs")
				}
			case 2:
				if v.Get("cellphone") != testPhone || v.Get("type") != "16" || v.Get("sign") != "f728f46fe8bd94538c084c15933574fd" {
					t.Fatal("SMS challenge differs")
				}
			case 3:
				if v.Get("cellphone") != "iQF6i+uWVp5hMHbiKjbi+w==" || v.Get("captcha") != "OHWmvBk3Be7jbSCY0BFy1Q==" || v.Get("areaCodeId") != "1" || v.Get("type") != "16" {
					t.Fatal("SMS encrypted fields differ")
				}
			case 4:
				if v.Get("userId") != "42" || v.Get("token") != "synthetic-token" || v.Get("osType") != "4" {
					t.Fatal("validation fields differ")
				}
			case 6:
				if v.Get("appId") != "1289" || v.Get("userId") != "42" || v.Get("appVersion") != testVersion || v.Get("ndid") != testDeviceID {
					t.Fatal("prelogin fields differ")
				}
			}
		} else {
			if r.URL.Host != "apidev.laohu.com" || r.Header.Get("appId") != "1289" {
				t.Fatal("OneLogin endpoint headers differ")
			}
			plain, err := decodeEnvelope(body, key.tail)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(plain)
			var v map[string]string
			if json.Unmarshal(plain, &v) != nil || v["channelUid"] != "42" || v["channelToken"] != "synthetic-token" || v["device"] != deviceJSON(testConfig(), testTime, testDeviceID, testOSVersion) {
				t.Fatal("OneLogin envelope fields differ")
			}
		}
		replies := []string{`{"code":0,"result":{"loginOptionConfig":[16,92]}}`, `{"code":0,"result":{"havePwd":false}}`, `{"code":0}`, channelJSON, `{"code":0}`, string(gameWire), preLoginJSON}
		reply := response(200, replies[step])
		reply.Header.Set("Set-Cookie", "unexpected=secret")
		step++
		return reply, nil
	})
	if err := c.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.SendSMS(context.Background(), testPhone); err != nil {
		t.Fatal(err)
	}
	ch, err := c.LoginSMS(context.Background(), testPhone, testCaptcha)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Zero()
	validation, err := c.Validate(context.Background(), ch)
	if err != nil || validation.Status != Validated {
		t.Fatalf("validation: %v", err)
	}
	game, err := c.OneLogin(context.Background(), ch)
	if err != nil {
		t.Fatal(err)
	}
	defer game.Zero()
	pre, err := c.PreLogin(context.Background(), game)
	if err != nil || !bytes.Equal(pre.Body, []byte(preLoginJSON)) {
		t.Fatalf("prelogin: %v", err)
	}
	defer pre.Zero()
	if step != len(paths) {
		t.Fatalf("executed %d steps", step)
	}
}
func TestClientRequiresInitAndValidInput(t *testing.T) {
	calls := 0
	clock := &fakeClock{testTime}
	c := newTestClient(t, clock, func(r *http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"code":0,"result":{"loginOptionConfig":[92]}}`), nil
	})
	ch := ChannelIdentity{UID: []byte("42"), Token: []byte("token")}
	assertPhase(t, c.SendSMS(context.Background(), testPhone), PhaseNotInitialized)
	_, err := c.LoginSMS(context.Background(), testPhone, testCaptcha)
	assertPhase(t, err, PhaseNotInitialized)
	_, err = c.Validate(context.Background(), ch)
	assertPhase(t, err, PhaseNotInitialized)
	_, err = c.OneLogin(context.Background(), ch)
	assertPhase(t, err, PhaseNotInitialized)
	_, err = c.PreLogin(context.Background(), GameIdentity{UID: []byte("42"), Token: []byte("tok")})
	assertPhase(t, err, PhaseNotInitialized)
	if calls != 0 {
		t.Fatal("network before init")
	}
	if err := c.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPhase(t, c.SendSMS(context.Background(), "138bad"), PhaseInvalidMainlandPhone)
	_, err = c.LoginSMS(context.Background(), testPhone, "1")
	assertPhase(t, err, PhaseInvalidCaptcha)
	_, err = c.OneLogin(context.Background(), ChannelIdentity{})
	assertPhase(t, err, PhaseChannelIdentityUnavailable)
	if calls != 1 {
		t.Fatal("invalid inputs sent")
	}
	c.Close()
	assertPhase(t, c.Init(context.Background()), PhaseGameKeyUnavailable)
	if c.key.valid || c.config.ChannelAppKey != "" {
		t.Fatal("Close retains key")
	}
}
func TestSMSCooldownConsumedOnFailureAndChallengeRequery(t *testing.T) {
	clock := &fakeClock{testTime}
	queries, sends := 0, 0
	c := newTestClient(t, clock, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/m/newApi/initConfig":
			return response(200, `{"code":0,"result":{"loginOptionConfig":[92]}}`), nil
		case "/m/newApi/query/whetherSetPassword":
			queries++
			return response(200, `{"code":0,"result":{"havePwd":1}}`), nil
		case "/m/newApi/sendPhoneCaptchaWithOutLogin":
			sends++
			return nil, errors.New("transport secret " + testPhone)
		case "/openApi/sms/new/login":
			r.ParseForm()
			if r.Form.Get("type") != "18" {
				t.Fatal("missing queried challenge")
			}
			return response(200, channelJSON), nil
		}
		t.Fatal("unexpected request")
		return nil, nil
	})
	if err := c.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertPhase(t, c.SendSMS(context.Background(), testPhone), PhaseRequestFailed)
	assertPhase(t, c.SendSMS(context.Background(), testPhone), PhaseSMSCooldown)
	if queries != 1 || sends != 1 {
		t.Fatal("failure automatically retried")
	}
	_, err := c.LoginSMS(context.Background(), testPhone, testCaptcha)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 2 {
		t.Fatal("failed SMS incorrectly established challenge")
	}
	clock.Advance(60 * time.Second)
	assertPhase(t, c.SendSMS(context.Background(), testPhone), PhaseRequestFailed)
	if sends != 2 {
		t.Fatal("cooldown boundary not reopened")
	}
}
func TestInitAndPasswordStateFailures(t *testing.T) {
	for _, tc := range []struct {
		body  string
		phase Phase
	}{
		{`{"code":0,"result":{"loginOptionConfig":["92"]}}`, PhasePhoneLoginUnavailable},
		{`{"code":1}`, PhaseServerRejected}, {`{"code":0,"code":0}`, PhaseInvalidResponse},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			c := newTestClient(t, &fakeClock{testTime}, func(*http.Request) (*http.Response, error) { return response(200, tc.body), nil })
			assertPhase(t, c.Init(context.Background()), tc.phase)
		})
	}
	for _, tc := range []struct {
		value string
		phase Phase
	}{{`{}`, PhasePasswordStateUnavailable}, {`{"havePwd":"1"}`, PhaseInvalidPasswordState}, {`{"havePwd":null}`, PhaseInvalidPasswordState}} {
		c := newTestClient(t, &fakeClock{testTime}, func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "initConfig") {
				return response(200, `{"code":0,"result":{"loginOptionConfig":[92]}}`), nil
			}
			return response(200, `{"code":0,"result":`+tc.value+`}`), nil
		})
		if err := c.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertPhase(t, c.SendSMS(context.Background(), testPhone), tc.phase)
	}
}
func TestTransportLimitsErrorsAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		length     int64
		phase      Phase
	}{
		{"http", `{"code":17,"message":"contact a@b.test 13800138000"}`, 400, -1, PhaseHTTPFailure},
		{"redirect", `{}`, 302, -1, PhaseHTTPFailure},
		{"length", `{}`, 200, 1<<20 + 1, PhaseInvalidResponse},
		{"stream", strings.Repeat("x", 1<<20+1), 200, -1, PhaseInvalidResponse},
		{"invalid", `<html>secret</html>`, 200, -1, PhaseInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := newTestClient(t, &fakeClock{testTime}, func(*http.Request) (*http.Response, error) {
				calls++
				r := response(tc.status, tc.body)
				r.ContentLength = tc.length
				r.Header.Set("Location", "https://other.example/leak")
				return r, nil
			})
			err := c.Init(context.Background())
			assertPhase(t, err, tc.phase)
			if calls != 1 {
				t.Fatal("request retried or redirected")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), testPhone) {
				t.Fatal("unsafe error")
			}
			if tc.name == "http" {
				var e *Error
				errors.As(err, &e)
				if e.APICode != 17 || !e.HasAPICode || strings.Contains(e.Message(), testPhone) || strings.Contains(e.Message(), "a@b") {
					t.Fatal("unsafe/lost API metadata")
				}
			}
		})
	}
	c := newTestClient(t, &fakeClock{testTime}, func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertPhase(t, c.Init(ctx), PhaseCancelled)
}
func TestTransportDecompression(t *testing.T) {
	good := `{"code":0,"result":{"loginOptionConfig":[92]}}`
	for _, encoding := range []string{"gzip", "deflate", "raw-deflate", "br", "huge-gzip"} {
		t.Run(encoding, func(t *testing.T) {
			var b bytes.Buffer
			payload := good
			if encoding == "huge-gzip" {
				payload = strings.Repeat("x", 1<<20+1)
			}
			var w io.WriteCloser
			switch encoding {
			case "gzip", "huge-gzip":
				w = gzip.NewWriter(&b)
			case "deflate":
				w = zlib.NewWriter(&b)
			case "raw-deflate":
				w, _ = flate.NewWriter(&b, flate.DefaultCompression)
			default:
				w = nopWriter{&b}
			}
			io.WriteString(w, payload)
			w.Close()
			c := newTestClient(t, &fakeClock{testTime}, func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Accept-Encoding") != "gzip, deflate" {
					t.Fatal("unsupported encoding advertised")
				}
				reply := response(200, b.String())
				e := encoding
				if e == "raw-deflate" {
					e = "deflate"
				}
				if e == "huge-gzip" {
					e = "gzip"
				}
				reply.Header.Set("Content-Encoding", e)
				return reply, nil
			})
			err := c.Init(context.Background())
			if encoding == "br" || encoding == "huge-gzip" {
				assertPhase(t, err, PhaseInvalidResponse)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type nopWriter struct{ io.Writer }

func (nopWriter) Close() error { return nil }
func TestValidationClassification(t *testing.T) {
	for _, tc := range []struct {
		status  int
		body    string
		network bool
		want    ValidationStatus
	}{
		{200, `{"code":0}`, false, Validated}, {200, `{"code":11}`, false, ServiceRejected},
		{200, `{"code":0,"result":{"privacyAgreement":{}}}`, false, RequiresOfficialInteraction},
		{400, `{"code":400}`, false, RequestRejected}, {403, `{"code":17}`, false, ServiceRejected},
		{400, `{"code":0}`, false, RequestRejected}, {400, `not-json`, false, RequestRejected},
		{200, `not-json`, false, Unknown}, {200, ``, true, NetworkFailure},
	} {
		c := newTestClient(t, &fakeClock{testTime}, func(r *http.Request) (*http.Response, error) {
			if strings.HasSuffix(r.URL.Path, "initConfig") {
				return response(200, `{"code":0,"result":{"loginOptionConfig":[92]}}`), nil
			}
			if tc.network {
				return nil, errors.New("private transport error")
			}
			return response(tc.status, tc.body), nil
		})
		if err := c.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
		r, err := c.Validate(context.Background(), ChannelIdentity{UID: []byte("42"), Token: []byte("tok")})
		if r.Status != tc.want || (err == nil) != (tc.want == Validated) {
			t.Fatalf("validation classification %d/%s = %v, %v", tc.status, tc.body, r.Status, err)
		}
	}
}
func TestNewClientClonesPolicyAndRejectsBadOptions(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	source := &http.Client{Jar: jar, Timeout: time.Minute}
	opts := Options{HTTPClient: source, InstalledVersion: testVersion, DeviceID: testDeviceID, OSVersion: testOSVersion, AppKey: testKey(t)}
	c, err := NewClient(testConfig(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if source.Jar != jar || source.Timeout != time.Minute || source.CheckRedirect != nil {
		t.Fatal("source HTTP policy modified")
	}
	if c.http.Jar != nil || c.http.Timeout != 20*time.Second {
		t.Fatal("unsafe HTTP policy")
	}
	for _, bad := range []Options{
		{InstalledVersion: "1", DeviceID: testDeviceID, OSVersion: testOSVersion, AppKey: testKey(t)},
		{InstalledVersion: testVersion, DeviceID: "not-a-device", OSVersion: testOSVersion, AppKey: testKey(t)},
		{InstalledVersion: testVersion, DeviceID: testDeviceID, OSVersion: testOSVersion},
	} {
		if c, err := NewClient(testConfig(), bad); err == nil {
			c.Close()
			t.Fatal("bad options accepted")
		}
	}
	opts.DeviceID = ""
	c2, err := NewClient(testConfig(), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if !deviceIDPattern.MatchString(c2.DeviceID()) {
		t.Fatal("device ID not generated")
	}
}
