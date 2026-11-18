// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestDownloadAvailabilityUsesBoundedHEADAndRecognizesOnlyDefinitiveResults(t *testing.T) {
	objects := make([]gameres.Object, 70)
	for i := range objects {
		objects[i] = gameres.Object{MD5: fmt.Sprintf("%032x", i+1), Size: 10}
	}
	var requests atomic.Int32
	src := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodHead || r.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("unexpected %s %s", r.Method, r.Header.Get("Accept-Encoding"))
		}
		switch {
		case strings.HasSuffix(r.URL.Path, objects[0].String()) && strings.HasPrefix(r.URL.Path, "/two/"):
			w.Header().Set("Content-Length", "10")
		case strings.HasSuffix(r.URL.Path, objects[1].String()):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, objects[2].String()):
			w.Header().Set("Content-Length", "9")
		case strings.HasSuffix(r.URL.Path, objects[0].String()):
			http.NotFound(w, r)
		default:
			w.Header().Set("Content-Length", "10")
		}
	}))
	defer src.Close()
	now := time.Now()
	b := GameDownloadBackend{Client: src.Client(), roots: []string{src.URL + "/one", src.URL + "/two"}}
	snapshot := b.downloadAvailability(context.Background(), bootstrapBranch, objects, now)
	if snapshot.Objects[objects[0]] != gameres.StatusReachable || snapshot.Objects[objects[1]] != gameres.StatusGone || snapshot.Objects[objects[2]] != gameres.StatusUnknown {
		t.Fatalf("snapshot did not identify mirror availability: %+v", snapshot)
	}
	if snapshot.At != now || snapshot.TTL != 10*time.Minute || len(snapshot.Objects) > 64 || snapshot.Objects[objects[64]] != gameres.StatusUnknown {
		t.Fatalf("invalid snapshot scope: time %v TTL %v objects %d", snapshot.At, snapshot.TTL, len(snapshot.Objects))
	}
	before := requests.Load()
	b.downloadAvailability(context.Background(), bootstrapBranch, objects, now.Add(9*time.Minute))
	if requests.Load() != before {
		t.Fatal("unexpired evidence was reprobed")
	}
	b.downloadAvailability(context.Background(), bootstrapBranch, objects, now.Add(11*time.Minute))
	if requests.Load() <= before {
		t.Fatal("expired evidence was retained")
	}
}
