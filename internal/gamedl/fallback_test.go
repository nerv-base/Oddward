// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestSequentialFallbackChecksumFailureRetriesWholeObjectOnce(t *testing.T) {
	good, bad := []byte("good"), []byte("evil")
	var full atomic.Int32
	e, task := fixture(t, good, func(w http.ResponseWriter, r *http.Request) {
		payload := good
		if r.Header.Get("Range") == "" && full.Add(1) == 1 {
			payload = bad
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
		w.WriteHeader(200)
		w.Write(payload)
	})
	if err := e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	if full.Load() != 2 {
		t.Fatalf("sequential checksum rounds=%d", full.Load())
	}
	assertBlob(t, e, identity(good), good)
}
