// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/nerv-base/Oddward/internal/gamedl"
)

func TestStrictRangeResponseNeverPublishesInvalidEnvelope(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"wrong offset", "Content-Range", "bytes 1-4/4"},
		{"wrong total", "Content-Range", "bytes 0-3/5"},
		{"short declared length", "Content-Length", "3"},
		{"encoded response", "Content-Encoding", "gzip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("data")
			e, task := fixture(t, data, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Range", "bytes 0-3/4")
				w.Header().Set("Content-Length", "4")
				w.Header().Set(tc.key, tc.value)
				w.WriteHeader(206)
				fmt.Fprint(w, "data")
			})
			if err := e.Run(context.Background(), task, nil); err == nil {
				t.Fatal("invalid range response accepted")
			}
			if f, err := e.OpenBlob(context.Background(), identity(data)); err == nil {
				f.Close()
				t.Fatal("invalid response published")
			}
		})
	}
}

var _ gamedl.Task
