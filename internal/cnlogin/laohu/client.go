// SPDX-License-Identifier: GPL-3.0-or-later
package laohu

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const validationUserAgent = "Mozilla/5.0 (Windows NT 5.1) AppleWebKit/534.30 (KHTML, like Gecko) Chrome/31.0.1650.57 Safari/534.30 AsiaArcClient/1.0"
const userEndpoint = "https://user.laohu.com"

// Options injects the shared application transport and immutable account context.
// OSVersion is the Windows version string (without " 64bit"). An empty DeviceID
// creates a fresh ID; saved accounts must restore their own ID here.
type Options struct {
	HTTPClient                            *http.Client
	InstalledVersion, DeviceID, OSVersion string
	AppKey                                AppKey
	Now                                   func() time.Time
}

// Client serializes authentication calls and owns a copy of its key material.
// Close clears that copy; callers must separately clear Options.AppKey.
type Client struct {
	mu                            sync.Mutex
	http                          *http.Client
	key                           AppKey
	config                        Config
	version, deviceID, osVersion  string
	now                           func() time.Time
	initialized, closed           bool
	challengePhone, challengeType string

	// smsMu protects the resend deadline independently of mu, which is held
	// during HTTP calls. UI snapshots must not wait for the network.
	smsMu   sync.Mutex
	nextSMS time.Time
}

func NewClient(config Config, options Options) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if !versionPattern.MatchString(options.InstalledVersion) || !metadataText(options.OSVersion) {
		return nil, errInvalidConfig
	}
	if !options.AppKey.valid {
		return nil, &Error{Phase: PhaseGameKeyUnavailable}
	}
	device := options.DeviceID
	if device == "" {
		var err error
		device, err = NewDeviceID()
		if err != nil {
			return nil, &Error{Phase: PhaseRequestFailed}
		}
	}
	if !deviceIDPattern.MatchString(device) {
		return nil, errInvalidConfig
	}
	h := http.Client{Transport: http.DefaultTransport}
	if options.HTTPClient != nil {
		h = *options.HTTPClient
	}
	h.Jar = nil
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if h.Timeout <= 0 || h.Timeout > 20*time.Second {
		h.Timeout = 20 * time.Second
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{http: &h, key: options.AppKey, config: config, version: options.InstalledVersion, deviceID: device, osVersion: options.OSVersion, now: now}, nil
}
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.key.Zero()
	c.config = Config{}
	c.challengePhone = ""
	c.challengeType = ""
	c.deviceID = ""
	c.initialized = false
	c.closed = true
}
func (c *Client) DeviceID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.deviceID }
func (c *Client) ready() error {
	if c.closed || !c.key.valid {
		return &Error{Phase: PhaseGameKeyUnavailable}
	}
	if !c.initialized {
		return &Error{Phase: PhaseNotInitialized}
	}
	return nil
}
func (c *Client) params() parameters { return channelParams(c.config, c.version, c.deviceID, c.now()) }
func (c *Client) Init(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return &Error{Phase: PhaseGameKeyUnavailable}
	}
	c.initialized = false
	raw, err := c.request(ctx, "POST", userEndpoint+"/m/newApi/initConfig", []byte(c.params().form()), "", 1<<20, false)
	if err != nil {
		return err
	}
	defer clear(raw)
	root, err := requireSuccess(raw)
	if err != nil {
		return err
	}
	options, _ := object(root["result"])["loginOptionConfig"].([]any)
	for _, v := range options {
		if n, ok := intValue(v, false); ok && n == 92 {
			c.initialized = true
			return nil
		}
	}
	return &Error{Phase: PhasePhoneLoginUnavailable}
}
func (c *Client) passwordType(ctx context.Context, phone string) (string, error) {
	p := c.params()
	p["cellphone"] = phone
	p.sign(c.config.ChannelAppKey)
	raw, err := c.request(ctx, "GET", userEndpoint+"/m/newApi/query/whetherSetPassword?"+p.form(), nil, "", 1<<20, false)
	if err != nil {
		return "", err
	}
	defer clear(raw)
	root, err := requireSuccess(raw)
	if err != nil {
		return "", err
	}
	value, exists := object(root["result"])["havePwd"]
	if !exists {
		return "", &Error{Phase: PhasePasswordStateUnavailable}
	}
	if b, ok := value.(bool); ok {
		if b {
			return "18", nil
		}
		return "16", nil
	}
	if n, ok := intValue(value, false); ok {
		if n == 1 {
			return "18", nil
		}
		if n == 0 {
			return "16", nil
		}
	}
	return "", &Error{Phase: PhaseInvalidPasswordState}
}

// SMSCooldownRemaining reports the resend window without waiting for an
// in-flight HTTP call. A valid send attempt consumes the window before any
// request, including attempts that fail; invalid phone numbers do not.
func (c *Client) SMSCooldownRemaining() time.Duration {
	c.smsMu.Lock()
	defer c.smsMu.Unlock()
	remaining := c.nextSMS.Sub(c.now())
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (c *Client) SendSMS(ctx context.Context, phone string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return err
	}
	if !phonePattern.MatchString(phone) {
		return &Error{Phase: PhaseInvalidMainlandPhone}
	}
	c.smsMu.Lock()
	now := c.now()
	if now.Before(c.nextSMS) {
		c.smsMu.Unlock()
		return &Error{Phase: PhaseSMSCooldown}
	}
	c.nextSMS = now.Add(60 * time.Second)
	c.smsMu.Unlock()
	c.challengePhone = ""
	c.challengeType = ""
	kind, err := c.passwordType(ctx, phone)
	if err != nil {
		return err
	}
	p := c.params()
	p["cellphone"] = phone
	p["type"] = kind
	p.sign(c.config.ChannelAppKey)
	raw, err := c.request(ctx, "POST", userEndpoint+"/m/newApi/sendPhoneCaptchaWithOutLogin", []byte(p.form()), "", 1<<20, false)
	if err != nil {
		return err
	}
	defer clear(raw)
	if _, err = requireSuccess(raw); err != nil {
		return err
	}
	c.challengePhone = phone
	c.challengeType = kind
	return nil
}
func (c *Client) LoginSMS(ctx context.Context, phone, captcha string) (ChannelIdentity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return ChannelIdentity{}, err
	}
	if !phonePattern.MatchString(phone) {
		return ChannelIdentity{}, &Error{Phase: PhaseInvalidMainlandPhone}
	}
	if !captchaPattern.MatchString(captcha) {
		return ChannelIdentity{}, &Error{Phase: PhaseInvalidCaptcha}
	}
	kind := c.challengeType
	if c.challengePhone != phone || kind == "" {
		var err error
		kind, err = c.passwordType(ctx, phone)
		if err != nil {
			return ChannelIdentity{}, err
		}
	}
	p := c.params()
	p["areaCodeId"] = "1"
	p["idfa"] = ""
	p["adm"] = ""
	p["mac"] = ""
	p["type"] = kind
	encryptedPhone, err := encryptField(phone, c.config.ChannelAppKey)
	if err != nil {
		return ChannelIdentity{}, &Error{Phase: PhaseRequestFailed}
	}
	encryptedCaptcha, err := encryptField(captcha, c.config.ChannelAppKey)
	if err != nil {
		return ChannelIdentity{}, &Error{Phase: PhaseRequestFailed}
	}
	p["cellphone"] = encryptedPhone
	p["captcha"] = encryptedCaptcha
	p.sign(c.config.ChannelAppKey)
	raw, err := c.request(ctx, "POST", userEndpoint+"/openApi/sms/new/login", []byte(p.form()), "", 1<<20, false)
	if err != nil {
		return ChannelIdentity{}, err
	}
	defer clear(raw)
	return ParseChannelIdentity(raw)
}
func validChannel(id ChannelIdentity) bool {
	return validIdentityField(id.UID, 63) && validIdentityField(id.Token, 127)
}
func validationError(r ValidationResult) *Error {
	phases := map[ValidationStatus]Phase{ServiceRejected: PhaseValidationRejected, NetworkFailure: PhaseValidationNetworkFailure, RequiresOfficialInteraction: PhaseValidationOfficialInteraction, RequestRejected: PhaseValidationRequestRejected, Unknown: PhaseValidationUnknownResponse}
	return &Error{Phase: phases[r.Status], APICode: r.APICode, HasAPICode: r.HasAPICode, StatusCode: r.HTTPStatus}
}
func (c *Client) Validate(ctx context.Context, id ChannelIdentity) (ValidationResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return ValidationResult{Status: Unknown}, err
	}
	if !validChannel(id) {
		return ValidationResult{Status: Unknown}, &Error{Phase: PhaseChannelIdentityUnavailable}
	}
	p := c.params()
	p["userId"] = string(id.UID)
	p["token"] = string(id.Token)
	p["osType"] = "4"
	p.sign(c.config.ChannelAppKey)
	raw, err := c.request(ctx, "POST", userEndpoint+"/openApi/sdkValidate", []byte(p.form()), validationUserAgent, 1<<20, false)
	if err != nil {
		r := ValidationResult{Status: NetworkFailure}
		if e, ok := err.(*Error); ok {
			r.APICode = e.APICode
			r.HasAPICode = e.HasAPICode
			r.HTTPStatus = e.StatusCode
			switch e.Phase {
			case PhaseCancelled:
				return r, e
			case PhaseHTTPFailure:
				r.Status = RequestRejected
				if e.HasAPICode && e.APICode != 0 && e.APICode != 400 {
					r.Status = ServiceRejected
				}
			case PhaseInvalidResponse:
				r.Status = Unknown
			}
		}
		return r, validationError(r)
	}
	defer clear(raw)
	r := ParseValidation(raw)
	if r.Status != Validated {
		return r, validationError(r)
	}
	return r, nil
}
func (c *Client) OneLogin(ctx context.Context, id ChannelIdentity) (GameIdentity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return GameIdentity{}, err
	}
	if !validChannel(id) {
		return GameIdentity{}, &Error{Phase: PhaseChannelIdentityUnavailable}
	}
	plain := marshalRelaxed(oneLoginFields(string(id.UID), string(id.Token), deviceJSON(c.config, c.now(), c.deviceID, c.osVersion)))
	defer clear(plain)
	wire, err := encodeEnvelope(plain, c.key.tail)
	if err != nil {
		return GameIdentity{}, &Error{Phase: PhaseRequestFailed}
	}
	raw, err := c.request(ctx, "POST", "https://apidev.laohu.com/sdkapi/v2/user/login", wire, "", 1<<20, true)
	if err != nil {
		return GameIdentity{}, err
	}
	defer clear(raw)
	decoded, err := decodeEnvelope(raw, c.key.tail)
	if err != nil {
		return GameIdentity{}, &Error{Phase: PhaseInvalidResponse}
	}
	defer clear(decoded)
	return ParseGameIdentity(decoded)
}
func (c *Client) PreLogin(ctx context.Context, id GameIdentity) (PreLoginResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return PreLoginResult{}, err
	}
	if !validIdentityField(id.UID, 63) || !validIdentityField(id.Token, 127) {
		return PreLoginResult{}, &Error{Phase: PhaseGameLoginRejected}
	}
	p := preLoginParams(c.version, c.deviceID, string(id.UID), c.now(), c.key.preLogin)
	raw, err := c.request(ctx, "POST", "https://antifatigue.laohu.com/sdkapi/preLogin", []byte(p.form()), "", 64478, false)
	if err != nil {
		return PreLoginResult{}, err
	}
	defer clear(raw)
	return ParsePreLogin(raw, id.UID)
}
func requireSuccess(raw []byte) (map[string]any, error) {
	root, err := strictObject(raw)
	if err != nil {
		return nil, &Error{Phase: PhaseInvalidResponse}
	}
	code, ok := intValue(root["code"], false)
	if !ok {
		return nil, &Error{Phase: PhaseInvalidResponse}
	}
	if code != 0 {
		return nil, &Error{Phase: PhaseServerRejected, APICode: code, HasAPICode: true}
	}
	return root, nil
}
func (c *Client) String() string                { return "laohu.Client{redacted}" }
func (c *Client) GoString() string              { return c.String() }
func (c *Client) Format(f fmt.State, verb rune) { io.WriteString(f, c.String()) }
func (o Options) Format(f fmt.State, verb rune) { io.WriteString(f, "laohu.Options{redacted}") }
