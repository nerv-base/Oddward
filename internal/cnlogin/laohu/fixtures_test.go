// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"testing"
	"time"
)

// Synthetic values only (spec §8.1); none of them is a real key or account.
const (
	testChannelAppKey = "0123456789abcdefghijklmnopqrstuv"
	testAppKey        = "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	testDeviceID      = "00112233445566778899aabbccddeeff"
	testVersion       = "1.2.3.4"
	testPhone         = "13800138000"
	testCaptcha       = "123456"
	testOSVersion     = "Microsoft Windows NT 10.0.22631.0"
)

var testTime = time.UnixMilli(1700000000000)

func testConfig() Config {
	return Config{
		AppVersion:    "9.8.7",
		PackageName:   "com.example.nte",
		ChannelID:     9,
		Platform:      4,
		ChannelAppID:  "10001",
		ChannelAppKey: testChannelAppKey,
	}
}

func testKey(t *testing.T) AppKey {
	t.Helper()
	key, err := NewAppKey([]byte(testAppKey))
	if err != nil {
		t.Fatalf("NewAppKey: %v", err)
	}
	return key
}

// fakeClock is a manually advanced clock shared by Client and Admission tests.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }
