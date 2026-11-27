// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamelocate"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func TestSyncOfficialMetadataNoOpWithoutOfficialDir(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	payload := []byte("game-file")
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", payload)
	digest := fmt.Sprintf("%x", md5.Sum(payload))
	cfg := gameres.Config{ResVersion: "1.5.13", ListHash: strings.Repeat("a", 32), CheckHash: "abc123"}
	manifest := gameres.ResList{
		Version: "1.5.13",
		Tag:     "baseTag",
		Files:   []gameres.File{{Path: "HT/Content/Paks/a.pak", MD5: digest, Size: int64(len(payload))}},
	}
	if err := SyncOfficialMetadata(ctx, root, cfg, manifest, "PC_150"); err != nil {
		t.Fatalf("SyncOfficialMetadata without official dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "NTELauncher")); !os.IsNotExist(err) {
		t.Fatalf("SyncOfficialMetadata created NTELauncher on fresh install: %v", err)
	}
}

func TestSyncOfficialMetadataUpdatesConfigAndResListPreservingTags(t *testing.T) {
	ctx := context.Background()
	root, _ := adoptFixture(t)
	// Seed realistic official config.xml (with BaseVerson, speed, stale RemoteContent)
	// and encrypted PatcherXML0 ResList.xml (with BaseVersion tag group and Removed).
	oldCfg := `<?xml version="1.0" encoding="utf-8" ?>
<config>
    <LocalBranch>PC_150</LocalBranch>
    <ResVersion>1.5.6</ResVersion>
    <AppVersion>0.0</AppVersion>
    <UpdateResVersion>0.0</UpdateResVersion>
    <Tag>baseTag</Tag>
    <ResSize>27</ResSize>
    <ResCount>2</ResCount>
    <BaseVerson appVersion="0.0">
        <Res section="0.101" version="0.101.12" Tag="pakchunk101" ResSize="1989634531" ResCount="8" />
    </BaseVerson>
    <Extra>
        <diffHash>11111111111111111111111111111111</diffHash>
        <listHash>22222222222222222222222222222222</listHash>
        <BaseTag>
            <item name="baseTag" />
        </BaseTag>
        <speed>75</speed>
    </Extra>
    <Hash>oldhash</Hash>
    <RemoteContent>stale-base64</RemoteContent>
</config>
`
	oldResPlain := `<?xml version="1.0" encoding="utf-8" ?>
<ResList version="1.5.6" tag="baseTag">
    <BaseVersion version="0.101.12" tag="pakchunk101">
        <ResList>
            <Res filename="Client/WindowsNoEditor/HT/Content/TagPatchPaks/pakchunk101-Windows.sig" md5="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" timestamp="1776740239" filesize="55752" />
        </ResList>
    </BaseVersion>
    <Removed>
        <Item name="Client/WindowsNoEditor/HT/Old.pak" version="1.5.5" />
    </Removed>
    <Res filename="Client/WindowsNoEditor/HT/Content/Paks/a.pak" md5="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" timestamp="1776732768" filesize="13" />
</ResList>
`
	oldResSealed, err := gameres.SealContainer([]byte(oldResPlain))
	if err != nil {
		t.Fatal(err)
	}
	adoptPut(t, root, adoptOfficialDir+"config.xml", []byte(oldCfg))
	adoptPut(t, root, adoptOfficialDir+"ResList.xml", oldResSealed)

	// Place updated 1.5.13 files on disk (one top-level file with blocks, one package entry).
	newA := []byte("updated-first-payload-1513")
	newB := []byte("updated-entry-payload")
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", newA)
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Binaries/Win64/helper.dll", newB)
	tsA := time.Unix(1795777328, 0).UTC()
	tsB := time.Unix(1795777400, 0).UTC()
	if err := os.Chtimes(filepath.Join(root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak"), tsA, tsA); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "Client/WindowsNoEditor/HT/Binaries/Win64/helper.dll"), tsB, tsB); err != nil {
		t.Fatal(err)
	}

	md5A := fmt.Sprintf("%x", md5.Sum(newA))
	md5B := fmt.Sprintf("%x", md5.Sum(newB))
	blockMD5 := fmt.Sprintf("%x", md5.Sum(newA))
	manifest := gameres.ResList{
		Version: "1.5.13",
		Tag:     "baseTag",
		Files: []gameres.File{
			{
				Path:   "HT/Content/Paks/a.pak",
				MD5:    md5A,
				Size:   int64(len(newA)),
				Blocks: []gameres.Block{{Index: 0, Start: 0, Size: int64(len(newA)), MD5: blockMD5}},
			},
		},
		Packages: []gameres.Package{
			{
				Pak: gameres.Object{MD5: strings.Repeat("c", 32), Size: int64(len(newB))},
				Entries: []gameres.Entry{
					{Path: "HT/Binaries/Win64/helper.dll", Pak: gameres.Object{MD5: strings.Repeat("c", 32), Size: int64(len(newB))}, Offset: 0, Size: int64(len(newB)), MD5: md5B},
				},
			},
		},
	}
	cfg := gameres.Config{
		ResVersion: "1.5.13",
		Tag:        "baseTag",
		BaseTag:    "baseTag",
		ListHash:   strings.Repeat("d", 32),
		DiffHash:   strings.Repeat("e", 32),
		CheckHash:  "f9a1b2",
	}

	if err := SyncOfficialMetadata(ctx, root, cfg, manifest, "PC_150"); err != nil {
		t.Fatalf("SyncOfficialMetadata failed: %v", err)
	}

	// Verify config.xml contents.
	gotCfgBytes, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(adoptOfficialDir+"config.xml")))
	if err != nil {
		t.Fatal(err)
	}
	gotCfgText := string(gotCfgBytes)
	gotCfg, err := gameres.ParseConfig(gotCfgBytes)
	if err != nil {
		t.Fatalf("synced config.xml failed ParseConfig: %v\n%s", err, gotCfgText)
	}
	wantSize := int64(len(newA) + len(newB))
	if gotCfg.ResVersion != "1.5.13" || gotCfg.LocalBranch != "PC_150" || gotCfg.ResSize != wantSize || gotCfg.ListHash != cfg.ListHash || gotCfg.DiffHash != cfg.DiffHash || gotCfg.CheckHash != "f9a1b2" {
		t.Fatalf("synced config mismatch: %+v", gotCfg)
	}
	if !strings.Contains(gotCfgText, `Tag="pakchunk101"`) || !strings.Contains(gotCfgText, "<speed>75</speed>") || !strings.Contains(gotCfgText, "<ResCount>2</ResCount>") {
		t.Fatalf("synced config dropped preserved fields:\n%s", gotCfgText)
	}
	if strings.Contains(gotCfgText, "RemoteContent") {
		t.Fatalf("synced config kept stale RemoteContent:\n%s", gotCfgText)
	}

	// Verify ResList.xml is PatcherXML0-sealed, preserves tags/removed, and writes real timestamps + blocks.
	gotResSealed, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(adoptOfficialDir+"ResList.xml")))
	if err != nil {
		t.Fatal(err)
	}
	gotResPlain, err := gameres.OpenContainer(gotResSealed)
	if err != nil {
		t.Fatalf("synced ResList.xml failed OpenContainer: %v", err)
	}
	gotResText := string(gotResPlain)
	gotRes, err := gameres.ParseResList(gotResPlain)
	if err != nil {
		t.Fatalf("synced ResList.xml failed ParseResList: %v\n%s", err, gotResText)
	}
	if gotRes.Version != "1.5.13" || len(gotRes.Files) != 2 || len(gotRes.Packages) != 0 || len(gotRes.Tags) != 1 || gotRes.Tags[0].Tag != "pakchunk101" {
		t.Fatalf("synced ResList structure mismatch: %+v", gotRes)
	}
	if !strings.Contains(gotResText, `timestamp="1795777328"`) || !strings.Contains(gotResText, `timestamp="1795777400"`) || !strings.Contains(gotResText, `blockSize="1"`) || !strings.Contains(gotResText, `<Removed>`) {
		t.Fatalf("synced ResList missing timestamps, blocks, or Removed:\n%s", gotResText)
	}

	// Oddward's own ReadOfficialVersion and AdoptQuick must cleanly accept the synced directory.
	ver, err := ReadOfficialVersion(gamelocate.Installation{Root: root, Branch: "PC_150"})
	if err != nil || ver != "1.5.13" {
		t.Fatalf("ReadOfficialVersion = %q, %v; want 1.5.13", ver, err)
	}
	store := adoptStore(t)
	adopted, err := store.AdoptQuick(ctx, root, nil)
	if err != nil || adopted.State != Adopted || adopted.Record.Version != "1.5.13" || len(adopted.Mismatches) != 0 {
		t.Fatalf("AdoptQuick on synced official metadata = %+v %v", adopted, err)
	}
}

func TestSyncOfficialMetadataAbortsWithoutMutationIfDiskFileMissing(t *testing.T) {
	ctx := context.Background()
	root, _ := adoptFixture(t)
	origCfg, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(adoptOfficialDir+"config.xml")))
	if err != nil {
		t.Fatal(err)
	}
	cfg := gameres.Config{ResVersion: "1.5.13", ListHash: strings.Repeat("d", 32)}
	manifest := gameres.ResList{
		Version: "1.5.13",
		Tag:     "baseTag",
		Files:   []gameres.File{{Path: "HT/Content/Paks/does-not-exist.pak", MD5: strings.Repeat("1", 32), Size: 10}},
	}
	if err := SyncOfficialMetadata(ctx, root, cfg, manifest, "PC_150"); err == nil {
		t.Fatal("expected error when manifest target is missing on disk")
	}
	afterCfg, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(adoptOfficialDir+"config.xml")))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterCfg) != string(origCfg) {
		t.Fatal("official config.xml mutated despite missing target file")
	}
}
