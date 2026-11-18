// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"net/http"
	"testing"
	"time"
)

func TestNetworkActiveDurationExcludesVerification(t *testing.T) {
	data := []byte("duration")
	e, task := fixture(t, data, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		http.ServeContent(w, r, "fixture", zeroTime, bytes.NewReader(data))
	})
	var verifying, staged time.Duration
	if err := e.Run(context.Background(), task, func(p gamedl.Progress) {
		if p.Stage == "verifying" {
			verifying = p.NetworkActiveDuration
			time.Sleep(50 * time.Millisecond)
		}
		if p.Stage == "staged" {
			staged = p.NetworkActiveDuration
		}
	}); err != nil {
		t.Fatal(err)
	}
	if verifying < 20*time.Millisecond {
		t.Fatalf("HTTP active duration missing: %v", verifying)
	}
	if staged != verifying {
		t.Fatalf("verification counted as network time: verifying=%v staged=%v", verifying, staged)
	}
}
