// SPDX-License-Identifier: GPL-3.0-or-later

package selfupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub serves release assets by URL and records requests.
type fakeGitHub struct {
	mu       sync.Mutex
	assets   map[string][]byte
	requests []string
}

func (f *fakeGitHub) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.String())
	body, ok := f.assets[r.URL.String()]
	f.mu.Unlock()
	if !ok {
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found")), Request: r}, nil
	}
	return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
}

func latestURL(name string) string {
	return "https://github.com/" + testRepo + "/releases/latest/download/" + name
}

func packageURL(m Manifest) string {
	return "https://github.com/" + testRepo + "/releases/download/v" + m.Version + "/" + m.Package.Name
}

func newUpdater(t *testing.T, l Layout, gh *fakeGitHub) *Updater {
	pub, _ := testKey()
	return &Updater{HTTP: &http.Client{Transport: gh}, Repository: testRepo, PublicKey: pub, Layout: l,
		FreeSpace: func(string) (uint64, error) { return 1 << 40, nil }}
}

func TestCheckOffersOnlyNewerSignedReleases(t *testing.T) {
	_, l := installed(t, "0.2.0")
	for _, c := range []struct {
		version string
		newer   bool
	}{{"0.3.0", true}, {"0.2.0", false}, {"0.1.9", false}, {"0.3.0-rc.1", true}} {
		m, data, sig, _ := release(t, c.version, appFiles(c.version))
		gh := &fakeGitHub{assets: map[string][]byte{latestURL(ManifestAsset): data, latestURL(SignatureAsset): sig}}
		got, newer, err := newUpdater(t, l, gh).Check(context.Background())
		if err != nil || newer != c.newer || (newer && got.Version != m.Version) {
			t.Fatalf("%s: %+v %v %v", c.version, got.Version, newer, err)
		}
	}
}

func TestCheckRefusesUnsignedOrUnconfigured(t *testing.T) {
	_, l := installed(t, "0.2.0")
	_, data, _, _ := release(t, "0.3.0", appFiles("0.3.0"))
	gh := &fakeGitHub{assets: map[string][]byte{latestURL(ManifestAsset): data, latestURL(SignatureAsset): []byte("AAAA")}}
	if _, _, err := newUpdater(t, l, gh).Check(context.Background()); err == nil {
		t.Fatal("accepted a bad signature")
	}
	gh.requests = nil
	u := newUpdater(t, l, gh)
	u.PublicKey = nil
	if _, _, err := u.Check(context.Background()); !errors.Is(err, errMissing) || len(gh.requests) != 0 {
		t.Fatalf("unconfigured build: %v, requests %v", err, gh.requests)
	}
}

func TestStageVerifiesEveryFileBeforeTheFolderAppears(t *testing.T) {
	root, l := installed(t, "0.1.0")
	m, data, sig, pkg := release(t, "0.2.0", appFiles("0.2.0"))
	gh := &fakeGitHub{assets: map[string][]byte{latestURL(ManifestAsset): data, latestURL(SignatureAsset): sig, packageURL(m): pkg}}
	u := newUpdater(t, l, gh)
	var last [2]int64
	dir, err := u.Stage(context.Background(), m, func(done, total int64) { last = [2]int64{done, total} })
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(root, "app-0.2.0") || last[0] != m.Package.Size || last[1] != m.Package.Size {
		t.Fatalf("dir %s progress %v", dir, last)
	}
	body, _ := os.ReadFile(filepath.Join(dir, AppExe))
	if string(body) != "MZ launcher 0.2.0" {
		t.Fatalf("exe = %q", body)
	}
	apps, _ := Apps(root)
	if pick, _ := Choose(apps); pick.Dir != dir {
		t.Fatalf("stub would start %s", pick.Dir)
	}
	if old, _ := os.ReadFile(filepath.Join(root, "app-0.1.0", AppExe)); string(old) != "MZ launcher 0.1.0" {
		t.Fatal("old version was touched")
	}
	// Staging again reuses the verified folder without downloading.
	gh.requests = nil
	if again, err := u.Stage(context.Background(), m, nil); err != nil || again != dir || len(gh.requests) != 0 {
		t.Fatalf("restage: %v %v", err, gh.requests)
	}
}

func TestStageRefusesBadPackagesAndLeavesNoReadyFolder(t *testing.T) {
	cases := map[string]func(t *testing.T) (Manifest, []byte){
		"package hash": func(t *testing.T) (Manifest, []byte) {
			m, _, _, pkg := release(t, "0.2.0", appFiles("0.2.0"))
			pkg[len(pkg)-30] ^= 1
			return m, pkg
		},
		"file hash": func(t *testing.T) (Manifest, []byte) {
			m, _, _, _ := release(t, "0.2.0", appFiles("0.2.0"))
			pkg := zipOf(t, []fixtureFile{{AppExe, []byte("MZ evil")}, appFiles("0.2.0")[1]})
			m.Package.Size, m.Package.SHA256 = int64(len(pkg)), digest(pkg)
			return m, pkg
		},
		"extra entry": func(t *testing.T) (Manifest, []byte) {
			m, _, _, _ := release(t, "0.2.0", appFiles("0.2.0"))
			pkg := zipOf(t, append(appFiles("0.2.0"), fixtureFile{"payload.dll", []byte("x")}))
			m.Package.Size, m.Package.SHA256 = int64(len(pkg)), digest(pkg)
			return m, pkg
		},
		"missing entry": func(t *testing.T) (Manifest, []byte) {
			m, _, _, _ := release(t, "0.2.0", appFiles("0.2.0"))
			pkg := zipOf(t, appFiles("0.2.0")[:1])
			m.Package.Size, m.Package.SHA256 = int64(len(pkg)), digest(pkg)
			return m, pkg
		},
		"zip slip": func(t *testing.T) (Manifest, []byte) {
			m, _, _, _ := release(t, "0.2.0", appFiles("0.2.0"))
			pkg := zipOf(t, append(appFiles("0.2.0"), fixtureFile{"../escape.txt", []byte("x")}))
			m.Package.Size, m.Package.SHA256 = int64(len(pkg)), digest(pkg)
			return m, pkg
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			root, l := installed(t, "0.1.0")
			m, pkg := build(t)
			gh := &fakeGitHub{assets: map[string][]byte{packageURL(m): pkg}}
			if _, err := newUpdater(t, l, gh).Stage(context.Background(), m, nil); err == nil {
				t.Fatal("staged a bad package")
			}
			apps, _ := Apps(root)
			if len(apps) != 1 || apps[0].Version.String() != "0.1.0" {
				t.Fatalf("apps after failure: %+v", apps)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); err == nil {
				t.Fatal("zip slip escaped")
			}
		})
	}
}

func TestStageRefusesWhenSpaceIsShort(t *testing.T) {
	_, l := installed(t, "0.1.0")
	m, _, _, pkg := release(t, "0.2.0", appFiles("0.2.0"))
	gh := &fakeGitHub{assets: map[string][]byte{packageURL(m): pkg}}
	u := newUpdater(t, l, gh)
	u.FreeSpace = func(string) (uint64, error) { return 1024, nil }
	if _, err := u.Stage(context.Background(), m, nil); !errors.Is(err, ErrNoSpace) || len(gh.requests) != 0 {
		t.Fatalf("err %v requests %v", err, gh.requests)
	}
}

func TestStageReplacesAnUnverifiableLeftover(t *testing.T) {
	root, l := installed(t, "0.1.0")
	m, _, _, pkg := release(t, "0.2.0", appFiles("0.2.0"))
	writeApp(t, root, "0.2.0", []fixtureFile{{AppExe, []byte("corrupt")}})
	gh := &fakeGitHub{assets: map[string][]byte{packageURL(m): pkg}}
	dir, err := newUpdater(t, l, gh).Stage(context.Background(), m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, AppExe)); string(body) != "MZ launcher 0.2.0" {
		t.Fatalf("leftover kept: %q", body)
	}
}

func TestStageHonoursCancellation(t *testing.T) {
	_, l := installed(t, "0.1.0")
	m, _, _, pkg := release(t, "0.2.0", appFiles("0.2.0"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newUpdater(t, l, &fakeGitHub{assets: map[string][]byte{packageURL(m): pkg}}).Stage(ctx, m, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
