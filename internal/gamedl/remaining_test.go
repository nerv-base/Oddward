// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"context"
	"net/http"
	"testing"
)

func TestRemainingCountsMissingAndVerifiedCacheWithoutHTTP(t *testing.T) {
	data := []byte("remaining")
	e, task := fixture(t, data, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-8/9")
		w.WriteHeader(206)
		w.Write(data)
	})
	remaining, err := e.Remaining(context.Background(), task.Transfers)
	if err != nil || remaining != int64(len(data)) {
		t.Fatalf("missing remaining=%d err=%v", remaining, err)
	}
	if err = e.Run(context.Background(), task, nil); err != nil {
		t.Fatal(err)
	}
	remaining, err = e.Remaining(context.Background(), task.Transfers)
	if err != nil || remaining != 0 {
		t.Fatalf("cached remaining=%d err=%v", remaining, err)
	}
}
