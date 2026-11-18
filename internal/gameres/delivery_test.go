// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestFixturesParseAsDocumented(t *testing.T) {
	cfg, data := deliveryFixtures(t)
	m, g, err := ParseDelivery(cfg, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 3 || len(m.Packages) != 1 || len(m.Tags) != 1 || len(g.Edges) != 2 || m.CheckHash != cfg.CheckHash {
		t.Fatalf("unexpected metadata: manifest=%+v graph=%+v", m, g)
	}
	old := Object{strings.Repeat("7", 32), 4000}
	keep := Object{oldHash, 2000}
	local := planLocal{installed: map[string]Object{"game/unchanged.bin": keep, "game/update.bin": old},
		trusted: map[Object]bool{keep: true, old: true}, cached: map[Object]bool{{midHash, 100}: true}}
	for _, test := range []struct {
		allow  bool
		update ActionKind
		bytes  int64
	}{{false, ActionFull, 4075}, {true, ActionPatch, 125}} {
		p, err := BuildPlan(&m, g, local, Capabilities{PatchApply: test.allow}, AvailabilitySnapshot{}, roomyBudget())
		if err != nil {
			t.Fatal(err)
		}
		got := make([]ActionKind, 0, len(p.Targets))
		for _, target := range p.Targets {
			got = append(got, target.Action)
		}
		if want := []ActionKind{ActionReuseInstalled, test.update, ActionInstallCached, ActionFull}; !slices.Equal(got, want) || p.DownloadBytes != test.bytes {
			t.Fatalf("patch enabled=%v: plan=%+v", test.allow, p)
		}
		if test.allow && (p.Targets[1].Fallback == nil || p.Targets[1].Fallback.Full.Object != (Object{newHash, 4000})) {
			t.Fatalf("wrong fallback: %+v", p.Targets[1])
		}
	}
}

func TestParseDeliveryRejectsUnboundMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Config, *ManifestXML)
		want   error
	}{
		{"list checksum", func(c *Config, _ *ManifestXML) { c.ListHash = oldHash }, ErrManifestHash},
		{"diff checksum", func(c *Config, _ *ManifestXML) { c.DiffHash = oldHash }, ErrManifestHash},
		{"diff missing", func(_ *Config, m *ManifestXML) { m.LastDiff = nil }, ErrManifestHash},
		{"diff unauthenticated", func(c *Config, _ *ManifestXML) { c.DiffHash = "" }, ErrManifestHash},
		{"version mismatch", func(c *Config, _ *ManifestXML) { c.ResVersion = "9.0.1" }, ErrMalformedManifest},
		{"tag mismatch", func(c *Config, _ *ManifestXML) { c.Tag = "other" }, ErrMalformedManifest},
		{"size mismatch", func(c *Config, _ *ManifestXML) { c.ResSize++ }, ErrMalformedManifest},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, data := deliveryFixtures(t)
			test.change(&c, &data)
			if _, _, err := ParseDelivery(c, data); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
	c, data := deliveryFixtures(t)
	c.DiffHash, data.LastDiff = "", nil
	if m, g, err := ParseDelivery(c, data); err != nil || len(m.Files) != 3 || g != nil {
		t.Fatalf("full-only delivery: manifest=%+v graph=%+v err=%v", m, g, err)
	}
}

func deliveryFixtures(t *testing.T) (Config, ManifestXML) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	data := ManifestXML{ResList: read("delivery.xml"), LastDiff: read("lastdiff.xml")}
	config := fmt.Sprintf(`<config><ResVersion>9.0.2</ResVersion><ResSize>6175</ResSize><Tag>baseTag</Tag><Hash>abcdef</Hash><Extra><listHash>%x</listHash><diffHash>%x</diffHash></Extra></config>`, md5.Sum(data.ResList), md5.Sum(data.LastDiff))
	c, err := ParseConfig([]byte(config))
	if err != nil {
		t.Fatal(err)
	}
	return c, data
}
