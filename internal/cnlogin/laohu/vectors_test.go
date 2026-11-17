// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"bytes"
	"testing"
)

// Spec §8.1 synthetic vectors, computed with .NET from the reference code.

func TestChannelParamsSignatureVector(t *testing.T) {
	v := channelParams(testConfig(), testVersion, testDeviceID, testTime)
	want := map[string]string{
		"appId": "10001", "bid": "com.example.nte", "channelId": "9", "deviceId": testDeviceID,
		"deviceModel": "WindowsPc", "deviceName": "WindowsPc", "deviceSys": "WindowsPc", "deviceType": "WindowsPc",
		"mac": "", "sdkVersion": "5.159.0", "t": "1700000000000", "version": testVersion,
		"sign": "a3dde382c3ddba17a746728a5f4ba826",
	}
	if len(v) != len(want) {
		t.Fatalf("keys = %v, want %d keys", v.keys(), len(want))
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	wantInput := "10001com.example.nte900112233445566778899aabbccddeeffWindowsPcWindowsPcWindowsPcWindowsPc5.159.017000000000001.2.3.4" + testChannelAppKey
	if got := v.signInput(testChannelAppKey); got != wantInput {
		t.Errorf("sign input = %q", got)
	}
}

func TestSignAddsRequestFields(t *testing.T) {
	v := channelParams(testConfig(), testVersion, testDeviceID, testTime)
	v["cellphone"] = testPhone
	v["type"] = "16"
	v.sign(testChannelAppKey)
	if v["sign"] != "f728f46fe8bd94538c084c15933574fd" {
		t.Fatalf("sign = %s", v["sign"])
	}
	// Re-signing replaces, never includes, the previous signature.
	v.sign(testChannelAppKey)
	if v["sign"] != "f728f46fe8bd94538c084c15933574fd" {
		t.Fatalf("re-sign = %s", v["sign"])
	}
}

func TestEncryptFieldVectors(t *testing.T) {
	for in, want := range map[string]string{
		testPhone:   "iQF6i+uWVp5hMHbiKjbi+w==",
		testCaptcha: "OHWmvBk3Be7jbSCY0BFy1Q==",
	} {
		got, err := encryptField(in, testChannelAppKey)
		if err != nil || got != want {
			t.Errorf("encryptField(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestAppKeyDerivations(t *testing.T) {
	key := testKey(t)
	if string(key.tail[:]) != "QRSTUVWXYZ012345" {
		t.Errorf("tail = %q", key.tail[:])
	}
	if string(key.preLogin[:]) != "BCHIJKLPQRZ01234" {
		t.Errorf("preLogin = %q", key.preLogin[:])
	}
	for _, bad := range []string{"", "short", testAppKey + "6", "ABCDEFGHIJKLMNOPQRSTUVWXYZ01234\x01", "ABCDEFGHIJKLMNOPQRSTUVWXYZ01234\x7f"} {
		if _, err := NewAppKey([]byte(bad)); err == nil {
			t.Errorf("NewAppKey(%q) accepted", bad)
		}
	}
}

func TestPreLoginSignatureVector(t *testing.T) {
	v := preLoginParams(testVersion, testDeviceID, "123456789", testTime, testKey(t).preLogin)
	wantCanonical := "appId=1289&appVersion=1.2.3.4&channelId=9&deviceName=WindowsPc&ndid=00112233445566778899aabbccddeeff&platformId=4&sdkVersion=5.159.0&timestamp=1700000000000&udid=00112233445566778899aabbccddeeff&userId=123456789&version=2"
	sign := v["sign"]
	delete(v, "sign")
	if got := v.canonical(); got != wantCanonical {
		t.Errorf("canonical = %q", got)
	}
	if sign != "F7QjclJKV+tEe8g/CyU35iY3gCsGlOkDRUu4QmsPCiM=" {
		t.Errorf("sign = %s", sign)
	}
}

const envelopePlain = `{"appId":"1289","channelId":"9","channelToken":"tok","channelUid":"42","device":"{}","extraJson":"{\"channelId\":\"9\",\"subchannelId\":\"9\"}","platformId":"4","version":"2"}`
const envelopeWire = "kbsbMKXTX82GoktpGvYLRQuqGQ04X5+OpRZFJNJzoR0n1x3b04hQB57KHFwlP4lxBlDln9zwtH8jmZuUaKlg19bXLAd8RmrqOrYADqNCLDZ3/PV0kCR2XT49XPc5hahIox/pR265rHztbOr4g3UsixyETr19GdtEoelMS+JRuME4N+KB6rt3fn+RZ29coafQk7WtB/w7aVN+4S/IjwthxaDEhbBIoyxmOicHe5NmzyU="

func TestOneLoginEnvelopeVector(t *testing.T) {
	key := testKey(t)
	fields := oneLoginFields("42", "tok", "{}")
	plain := marshalRelaxed(fields)
	if string(plain) != envelopePlain {
		t.Fatalf("fields JSON = %s", plain)
	}
	wire, err := encodeEnvelope(plain, key.tail)
	if err != nil || string(wire) != envelopeWire {
		t.Fatalf("envelope = %s, %v", wire, err)
	}
	back, err := decodeEnvelope(wire, key.tail)
	if err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("decode = %s, %v", back, err)
	}
}

func TestDecodeEnvelopeRejectsMalformed(t *testing.T) {
	key := testKey(t)
	for name, in := range map[string]string{
		"empty":       "",
		"non-ascii":   "kbsb\x80",
		"not-base64":  "!!!!",
		"not-block":   "AAAA",
		"bad-padding": "AAAAAAAAAAAAAAAAAAAAAA==",
	} {
		if _, err := decodeEnvelope([]byte(in), key.tail); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
