//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const downloadMutationWorkerEnv = "ODDWARD_DOWNLOAD_MUTATION_WORKER"

func TestDownloadMutationSerializesIndependentProcesses(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	release := filepath.Join(root, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestDownloadMutationWorker$", "-test.count=1")
	cmd.Env = append(os.Environ(), downloadMutationWorkerEnv+"="+root)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile := func(path string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(path); err == nil {
				return
			}
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				t.Fatalf("worker did not create %s", path)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForFile(ready)
	if _, err := acquireDownloadMutation(root); !errors.Is(err, errDownloadMutation) {
		t.Fatalf("second process acquired mutation lock: %v", err)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("mutation worker failed: %v", err)
	}
	lock, err := acquireDownloadMutation(root)
	if err != nil {
		t.Fatalf("lock was not released after worker exit: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadMutationWorker(t *testing.T) {
	root := os.Getenv(downloadMutationWorkerEnv)
	if root == "" {
		t.Skip("subprocess-only mutation worker")
	}
	lock, err := acquireDownloadMutation(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := os.WriteFile(filepath.Join(root, "ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "release")); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("parent did not release worker")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
