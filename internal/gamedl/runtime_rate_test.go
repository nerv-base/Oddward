// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestRuntimeRateLimitBlocksThenReleasesPendingRead(t *testing.T) {
	data := bytes.Repeat([]byte("r"), 1<<20)
	started := make(chan struct{})
	var once sync.Once
	e, task := fixture(t, data, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		w.Header().Set("Content-Length", "1048576")
		w.Header().Set("Content-Range", "bytes 0-1048575/1048576")
		w.WriteHeader(206)
		w.Write(data)
	})
	if err := e.SetRateLimitKiB(1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, task, nil) }()
	<-started
	select {
	case err := <-done:
		t.Fatalf("runtime limit bypassed: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	before := time.Now()
	if err := e.SetRateLimitKiB(0); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("rate change did not wake pending read")
	}
	if time.Since(before) > time.Second {
		t.Fatal("runtime setter blocked on Engine.Run")
	}
	assertBlob(t, e, task.Transfers[0].Object, data)
}
