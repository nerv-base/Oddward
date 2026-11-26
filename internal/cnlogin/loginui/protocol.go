// SPDX-License-Identifier: GPL-3.0-or-later
// Package loginui is the presentation-only bridge to Oddward's isolated login UI helper.
// Models never contain credentials. Events containing phone/captcha must not be logged.
package loginui

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxFrame = 65536

var ErrProtocol = errors.New("login UI protocol rejected")

type Section struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}
type Account struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	MaskedPhone string `json:"maskedPhone"`
}
type Model struct {
	Screen            string    `json:"screen"`
	Title             string    `json:"title,omitempty"`
	Subtitle          string    `json:"subtitle,omitempty"`
	Status            string    `json:"status,omitempty"`
	Busy              bool      `json:"busy,omitempty"`
	PrimaryLabel      string    `json:"primaryLabel,omitempty"`
	SMSLabel          string    `json:"smsLabel,omitempty"`
	CanSendSMS        bool      `json:"canSendSms,omitempty"`
	Remember          bool      `json:"remember,omitempty"`
	CanBack           bool      `json:"canBack,omitempty"`
	CanOfficial       bool      `json:"canOfficial,omitempty"`
	Banner            string    `json:"banner,omitempty"`
	Notice            []Section `json:"notice,omitempty"`
	Accounts          []Account `json:"accounts,omitempty"`
	SelectedAccountID string    `json:"selectedAccountId,omitempty"`
	// The helper queries only this process's public window geometry. FILETIME
	// stays a decimal string so JSON clients cannot round the process identity.
	GameProcessID    uint32 `json:"gameProcessId,omitempty"`
	GameCreationTime string `json:"gameCreationTime,omitempty"`
}
type Event struct {
	Kind       string `json:"kind"`
	Phone      string `json:"phone,omitempty"`
	Captcha    string `json:"captcha,omitempty"`
	AccountID  string `json:"accountId,omitempty"`
	Label      string `json:"label,omitempty"`
	Remember   bool   `json:"remember,omitempty"`
	Window     string `json:"window,omitempty"`
	GameWindow string `json:"gameWindow,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
}

// Envelope is one length-prefixed JSON wire frame between the launcher session
// and the login UI helper process.
type Envelope struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	Model   *Model `json:"model,omitempty"`
	Event   *Event `json:"event,omitempty"`
}

type envelope = Envelope

// ReadFrame reads, bounds-checks and validates one wire frame from r.
func ReadFrame(r io.Reader) (Envelope, error) { return readFrame(r) }

// WriteFrame validates and writes one bounded wire frame to w.
func WriteFrame(w io.Writer, m Envelope) error { return writeFrame(w, m) }

func readFrame(r io.Reader) (envelope, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if err == io.EOF {
			return envelope{}, io.EOF
		}
		return envelope{}, ErrProtocol
	}
	n := binary.LittleEndian.Uint32(h[:])
	if n == 0 || n > MaxFrame {
		return envelope{}, ErrProtocol
	}
	data := make([]byte, n)
	defer clear(data)
	if _, err := io.ReadFull(r, data); err != nil {
		return envelope{}, ErrProtocol
	}
	var m envelope
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if !utf8.Valid(data) || d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || validateEnvelope(m) != nil {
		return envelope{}, ErrProtocol
	}
	if m.Event != nil {
		var raw struct {
			Event map[string]json.RawMessage `json:"event"`
		}
		if json.Unmarshal(data, &raw) != nil {
			return envelope{}, ErrProtocol
		}
		if m.Event.Kind == "bind_overlay" || m.Event.Kind == "embed_overlay" {
			// A repeated null must not retain a preceding string through Go's
			// null-to-string decoding rule. The wire field itself must be valid.
			var requestID string
			if json.Unmarshal(raw.Event["requestId"], &requestID) != nil || requestID != m.Event.RequestID {
				return envelope{}, ErrProtocol
			}
		} else {
			_, hasWindow := raw.Event["window"]
			_, hasGame := raw.Event["gameWindow"]
			if hasWindow || hasGame {
				return envelope{}, ErrProtocol
			}
			for field := range raw.Event {
				if strings.EqualFold(field, "requestId") {
					return envelope{}, ErrProtocol
				}
			}
		}
	}
	return m, nil
}
func writeFrame(w io.Writer, m envelope) error {
	if err := validateEnvelope(m); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return ErrProtocol
	}
	defer clear(data)
	if len(data) == 0 || len(data) > MaxFrame {
		return ErrProtocol
	}
	var h [4]byte
	binary.LittleEndian.PutUint32(h[:], uint32(len(data)))
	if err = writeAll(w, h[:]); err != nil {
		return err
	}
	return writeAll(w, data)
}
func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return errors.New("login UI write failed")
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
func validateEnvelope(m envelope) error {
	if m.Version != 1 {
		return ErrProtocol
	}
	switch m.Type {
	case "model":
		if m.Model != nil && m.Event == nil {
			return validateModel(*m.Model)
		}
	case "event":
		if m.Event != nil && m.Model == nil {
			return validateEvent(*m.Event)
		}
	case "window_bound":
		if m.Event != nil && m.Model == nil && (m.Event.Kind == "bind_overlay" || m.Event.Kind == "embed_overlay") {
			return validateEvent(*m.Event)
		}
	case "close":
		if m.Model == nil && m.Event == nil {
			return nil
		}
	}
	return ErrProtocol
}
func nativeWindow(s string) (uintptr, bool) {
	n, err := strconv.ParseUint(s, 10, strconv.IntSize)
	return uintptr(n), err == nil && n != 0 && strconv.FormatUint(n, 10) == s
}
func windowRequestID(s string) (uint64, bool) {
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil && n != 0 && strconv.FormatUint(n, 10) == s
}
func bounded(s string, n int) bool { return len(s) <= n && utf8.ValidString(s) }
func validateModel(m Model) error {
	if m.Screen != "notice" && m.Screen != "phone" && m.Screen != "accounts" && m.Screen != "welcome" {
		return ErrProtocol
	}
	if !bounded(m.Title, 256) || !bounded(m.Subtitle, 512) || !bounded(m.Status, 4096) || !bounded(m.PrimaryLabel, 128) || !bounded(m.SMSLabel, 128) || !bounded(m.SelectedAccountID, 64) || len(m.Accounts) > 16 || len(m.Notice) > 8 {
		return ErrProtocol
	}
	// The welcome banner is the only screen that carries text outside the login
	// window, so the field is exclusive to it and must never arrive empty.
	if m.Screen == "welcome" {
		if m.Banner == "" || !bounded(m.Banner, 256) {
			return ErrProtocol
		}
	} else if m.Banner != "" {
		return ErrProtocol
	}
	if m.GameProcessID != 0 || m.GameCreationTime != "" {
		created, err := strconv.ParseUint(m.GameCreationTime, 10, 64)
		if m.GameProcessID == 0 || err != nil || created == 0 || strconv.FormatUint(created, 10) != m.GameCreationTime {
			return ErrProtocol
		}
	}
	for _, a := range m.Accounts {
		if a.ID == "" || !bounded(a.ID, 64) || !bounded(a.Label, 128) || !bounded(a.MaskedPhone, 32) {
			return ErrProtocol
		}
	}
	for _, s := range m.Notice {
		if !bounded(s.Title, 256) || !bounded(s.Body, 4096) {
			return ErrProtocol
		}
	}
	return nil
}
func validateEvent(e Event) error {
	if !bounded(e.Phone, 32) || !bounded(e.Captcha, 16) || !bounded(e.AccountID, 64) || !bounded(e.Label, 128) {
		return ErrProtocol
	}
	// Window binding is an internal request, never an account or login action.
	if e.Kind == "bind_overlay" || e.Kind == "embed_overlay" {
		_, own := nativeWindow(e.Window)
		_, game := nativeWindow(e.GameWindow)
		_, request := windowRequestID(e.RequestID)
		if own && game && request && e.Phone == "" && e.Captcha == "" && e.AccountID == "" && e.Label == "" && !e.Remember {
			return nil
		}
		return ErrProtocol
	}
	if e.Window != "" || e.GameWindow != "" || e.RequestID != "" {
		return ErrProtocol
	}
	// Field allowlists prevent accidental secret-bearing payloads on unrelated actions.
	switch e.Kind {
	case "ready", "acknowledge", "official", "cancel", "help", "back", "other_account":
		if e.Phone == "" && e.Captcha == "" && e.AccountID == "" && e.Label == "" && !e.Remember {
			return nil
		}
	case "send_sms":
		if e.Captcha == "" && e.AccountID == "" && e.Label == "" && !e.Remember {
			return nil
		}
	case "login_phone":
		if e.AccountID == "" && e.Label == "" {
			return nil
		}
	case "select_account", "login_saved", "delete_account":
		if e.AccountID != "" && e.Phone == "" && e.Captcha == "" && e.Label == "" && !e.Remember {
			return nil
		}
	case "rename_account":
		if e.AccountID != "" && e.Phone == "" && e.Captcha == "" && !e.Remember {
			return nil
		}
	}
	return ErrProtocol
}
