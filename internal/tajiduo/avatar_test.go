// SPDX-License-Identifier: GPL-3.0-or-later

package tajiduo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAvatarFetchesPNG(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "" {
			t.Errorf("avatar request must not carry authorization")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-bytes"))
	}))
	defer srv.Close()

	c, err := NewClient(Config{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c.avatarBase = srv.URL + "/"
	data, err := c.Avatar(context.Background(), "Fashion_1042_2")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "png-bytes" {
		t.Fatalf("avatar bytes = %q", data)
	}
	if gotPath != "/Fashion_1042_2.PNG" {
		t.Fatalf("avatar path = %q", gotPath)
	}
}

func TestAvatarRejectsUnsafeID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unsafe id must not be requested")
	}))
	defer srv.Close()

	c, err := NewClient(Config{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c.avatarBase = srv.URL + "/"
	if _, err := c.Avatar(context.Background(), "../Fashion_1"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestAvatarRejectsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>"))
	}))
	defer srv.Close()

	c, err := NewClient(Config{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c.avatarBase = srv.URL + "/"
	if _, err := c.Avatar(context.Background(), "Fashion_1"); !errors.Is(err, ErrProtocolChanged) {
		t.Fatalf("err = %v, want ErrProtocolChanged", err)
	}
}
