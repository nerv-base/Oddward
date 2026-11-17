// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretFormattingRedacts(t *testing.T) {
	sec := sampleSecret(1)
	var logged strings.Builder
	slog.New(slog.NewTextHandler(&logged, nil)).Info("unlock", "secret", sec)
	outputs := []string{
		sec.String(),
		fmt.Sprint(sec),
		fmt.Sprintf("%v %+v %#v %s", sec, sec, sec, sec),
		fmt.Sprintf("%v", &sec),
		fmt.Sprintf("%+v", struct{ S Secret }{sec}),
		logged.String(),
	}
	for _, out := range outputs {
		for _, plain := range []string{sec.DeviceID, sec.ChannelUID, sec.ChannelToken, "SECRETVALUE"} {
			if strings.Contains(out, plain) {
				t.Fatalf("formatted secret leaks %q: %s", plain, out)
			}
		}
		if !strings.Contains(out, "redacted") {
			t.Fatalf("formatted secret does not say it is redacted: %s", out)
		}
	}
}

func TestSecretWipe(t *testing.T) {
	sec := sampleSecret(1)
	sec.Wipe()
	if sec != (Secret{}) {
		t.Fatalf("Wipe left %d/%d/%d bytes", len(sec.DeviceID), len(sec.ChannelUID), len(sec.ChannelToken))
	}
	buf := []byte("data key material")
	wipeBytes(buf)
	for _, b := range buf {
		if b != 0 {
			t.Fatal("wipeBytes left non-zero bytes")
		}
	}
}
