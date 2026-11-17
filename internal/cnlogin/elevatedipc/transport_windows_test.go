// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestSyntheticDuplexSurvivesSetupCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := start(ctx, syntheticLaunch(t), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancel()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "pong" {
		t.Fatalf("reply %q", buf)
	}
}

func TestCanceledSetupDoesNotLaunchOrConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, run := range []func() error{
		func() error {
			c, err := Start(ctx)
			if c != nil {
				c.Close()
				t.Error("Start returned a connection")
			}
			return err
		},
		func() error {
			c, err := Accept(ctx, nil)
			if c != nil {
				c.Close()
				t.Error("Accept returned a connection")
			}
			return err
		},
	} {
		if err := run(); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want canceled", err)
		}
	}
}
