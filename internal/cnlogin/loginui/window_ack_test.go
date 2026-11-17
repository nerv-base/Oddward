// SPDX-License-Identifier: GPL-3.0-or-later
package loginui

import (
	"bytes"
	"errors"
	"testing"
)

func TestWindowBoundFrameCarriesRequestCorrelation(t *testing.T) {
	for _, kind := range []string{"bind_overlay", "embed_overlay"} {
		for _, typ := range []string{"event", "window_bound"} {
			raw := `{"version":1,"type":"` + typ + `","event":{"kind":"` + kind + `","window":"23","gameWindow":"42","requestId":"18446744073709551615"}}`
			if err := readWindowEvent(raw); err != nil {
				t.Fatalf("valid %s/%s correlation frame rejected: %v", typ, kind, err)
			}
			want := Event{Kind: kind, Window: "23", GameWindow: "42", RequestID: "18446744073709551615"}
			var wire bytes.Buffer
			if err := writeFrame(&wire, envelope{Version: 1, Type: typ, Event: &want}); err != nil {
				t.Fatal(err)
			}
			got, err := readFrame(&wire)
			if err != nil || got.Type != typ || got.Model != nil || got.Event == nil || *got.Event != want {
				t.Fatalf("correlation changed in round trip: %+v, %v", got, err)
			}
		}
	}
}

func TestWindowRequestIDMustBeCanonicalPositiveUint64(t *testing.T) {
	for _, kind := range []string{"bind_overlay", "embed_overlay"} {
		for _, typ := range []string{"event", "window_bound"} {
			for _, field := range []string{
				``, `,"requestId":null`, `,"requestId":""`, `,"requestId":"0"`,
				`,"requestId":"01"`, `,"requestId":"+1"`, `,"requestId":"-1"`,
				`,"requestId":" 1"`, `,"requestId":"1 "`, `,"requestId":"1e2"`,
				`,"requestId":1`, `,"requestId":1.0`, `,"requestId":true`,
				`,"requestId":"18446744073709551616"`, `,"requestId":"１"`,
			} {
				raw := `{"version":1,"type":"` + typ + `","event":{"kind":"` + kind + `","window":"23","gameWindow":"42"` + field + `}}`
				if err := readWindowEvent(raw); !errors.Is(err, ErrProtocol) {
					t.Fatalf("invalid request id accepted on %s/%s: %s", typ, kind, field)
				}
			}
		}
	}
}

func TestOnlyWindowRequestsMayCarryRequestIDField(t *testing.T) {
	for _, fields := range []string{
		`"kind":"ready"`, `"kind":"acknowledge"`, `"kind":"official"`,
		`"kind":"cancel"`, `"kind":"help"`, `"kind":"back"`, `"kind":"other_account"`,
		`"kind":"send_sms"`, `"kind":"login_phone"`,
		`"kind":"select_account","accountId":"synthetic"`,
		`"kind":"login_saved","accountId":"synthetic"`,
		`"kind":"delete_account","accountId":"synthetic"`,
		`"kind":"rename_account","accountId":"synthetic"`,
	} {
		for _, id := range []string{`"1"`, `""`, `null`} {
			raw := `{"version":1,"type":"event","event":{` + fields + `,"requestId":` + id + `}}`
			if err := readWindowEvent(raw); !errors.Is(err, ErrProtocol) {
				t.Fatalf("requestId field accepted on business event: %s", fields)
			}
		}
	}
}

func TestRequestIDCannotBypassValidationThroughJSONAliasesOrNull(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"type":"event","event":{"kind":"cancel","RequestId":null}}`,
		`{"version":1,"type":"event","event":{"kind":"ready","REQUESTID":""}}`,
		`{"version":1,"type":"event","event":{"kind":"bind_overlay","window":"23","gameWindow":"42","requestId":"1","requestId":null}}`,
	} {
		if err := readWindowEvent(raw); !errors.Is(err, ErrProtocol) {
			t.Fatalf("requestId presence/type restriction bypassed: %s", raw)
		}
	}
}

func TestWindowBoundEnvelopeRejectsModelsAndBusinessActions(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"type":"window_bound"}`,
		`{"version":1,"type":"window_bound","event":null}`,
		`{"version":1,"type":"window_bound","model":{"screen":"phone"}}`,
		`{"version":1,"type":"window_bound","event":{"kind":"ready"}}`,
		`{"version":1,"type":"window_bound","event":{"kind":"send_sms","phone":"13800000000"}}`,
		`{"version":1,"type":"window_bound","model":{"screen":"phone"},"event":{"kind":"bind_overlay","window":"23","gameWindow":"42","requestId":"1"}}`,
	} {
		if err := readWindowEvent(raw); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid window completion accepted: %s", raw)
		}
	}
}
