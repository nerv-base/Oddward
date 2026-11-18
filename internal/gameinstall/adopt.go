// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gamelocate"
	"github.com/nerv-base/Oddward/internal/gameres"
)

const adoptOfficialDir = "NTELauncher/UserData/Patcher/PatcherSDK/"

// AdoptQuick reads only structural headers, metadata and file sizes. It never
// hashes game contents, creates a work directory, or contacts a server.
func (s *Store) AdoptQuick(ctx context.Context, selected string, remote *AdoptMetadata) (AdoptResult, error) {
	installation, err := gamelocate.Inspect(ctx, selected)
	if err != nil {
		return AdoptResult{}, err
	}
	r, err := os.OpenRoot(installation.Root)
	if err != nil {
		return AdoptResult{}, err
	}
	defer r.Close()
	result := AdoptResult{Installation: installation, State: Detected}
	configData, configErr := stateReadBounded(r, adoptOfficialDir+"config.xml")
	manifestData, manifestErr := stateReadBounded(r, adoptOfficialDir+"ResList.xml")
	officialDigest := ""
	if configErr == nil && manifestErr == nil {
		officialDigest = adoptDigest(append(append(append([]byte{}, configData...), 0), manifestData...))
	}
	var cfg gameres.Config
	var manifest gameres.ResList
	var plain []byte
	usable := false
	if configErr == nil && manifestErr == nil {
		cfg, configErr = gameres.ParseConfig(configData)
		plain, manifest, manifestErr = adoptParseManifest(manifestData)
		if manifestErr != nil && plain != nil {
			return result, manifestErr
		}
		usable = configErr == nil && manifestErr == nil
	}
	remoteHashMismatch := false
	if usable && remote != nil {
		remoteConfig, configErr := gameres.ParseConfig(remote.ConfigXML)
		remotePlain, remoteManifest, manifestErr := adoptParseManifest(remote.Manifest)
		if configErr != nil || manifestErr != nil || gameres.VerifyManifestHash(remotePlain, remoteConfig.ListHash) != nil {
			result.Warnings = append(result.Warnings, "supplied remote metadata is untrusted; local baseline retained")
		} else if remote.Branch != "" && remote.Branch != installation.Branch || remoteConfig.ResVersion != cfg.ResVersion || remoteManifest.Version != manifest.Version || remoteConfig.ListHash != cfg.ListHash {
			result.Warnings = append(result.Warnings, "remote metadata differs from the installed generation; local baseline retained")
		}
	}
	if !usable && remote != nil {
		if remote.Branch != "" && remote.Branch != installation.Branch {
			return result, fmt.Errorf("%w: remote branch differs", ErrStaleState)
		}
		cfg, err = gameres.ParseConfig(remote.ConfigXML)
		if err != nil {
			return result, err
		}
		plain, manifest, err = adoptParseManifest(remote.Manifest)
		if err != nil {
			return result, err
		}
		remoteHashMismatch = gameres.VerifyManifestHash(plain, cfg.ListHash) != nil
		usable = true
	}
	key, err := InstallKey(installation.Root)
	if err != nil {
		return result, err
	}
	id, err := installRootFileID(installation.Root)
	if err != nil {
		return result, err
	}
	record := InstallRecord{Schema: 2, InstallKey: key, Root: installation.Root, Mode: "adopted", State: Detected, Branch: installation.Branch, VerifyCoverage: "none", CreatedAt: time.Now().UTC(), RootFileID: id, OfficialDigest: officialDigest}
	if usable {
		record.Version = cfg.ResVersion
		record.ListHash = cfg.ListHash
		record.ManifestDigest = adoptDigest(plain)
		record.VerifyCoverage = "metadata"
		targets, err := adoptTargets(manifest.Files, manifest.Packages)
		if err != nil {
			return result, err
		}
		record.Targets = targets
		result.Files = len(targets)
		result.Mismatches, err = adoptCheckSizes(ctx, r, targets)
		if err != nil {
			return result, err
		}
		for _, tag := range manifest.Tags {
			targets, err := adoptTargets(tag.Files, tag.Packages)
			if err != nil {
				return result, err
			}
			drift, err := adoptCheckSizes(ctx, r, targets)
			if err != nil {
				return result, err
			}
			result.TagDrift = append(result.TagDrift, drift...)
		}
		if len(result.Mismatches) == 0 {
			record.State = Adopted
			result.State = Adopted
		}
		record.BaselineMismatch = remoteHashMismatch || cfg.ResVersion != manifest.Version || cfg.LocalBranch != "" && cfg.LocalBranch != installation.Branch
		if record.BaselineMismatch || cfg.ListHash != record.ManifestDigest {
			result.Warnings = append(result.Warnings, "official metadata generations differ; full verification required")
		}
	} else {
		result.Warnings = append(result.Warnings, "official resource metadata unavailable; installation only detected")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := s.writeInstall(record); err != nil {
		return result, err
	}
	result.Record = record
	return result, nil
}
func adoptDigest(data []byte) string { return fmt.Sprintf("%x", md5.Sum(data)) }
func adoptParseManifest(data []byte) ([]byte, gameres.ResList, error) {
	plain := data
	var err error
	switch {
	case bytes.HasPrefix(data, []byte("PatcherXML0")):
		plain, err = gameres.OpenContainer(data)
	case bytes.HasPrefix(data, []byte("PK")):
		var opened gameres.ManifestXML
		opened, err = gameres.OpenManifestZIP(data)
		plain = opened.ResList
	}
	if err != nil {
		return nil, gameres.ResList{}, err
	}
	parsed, err := gameres.ParseResList(plain)
	return plain, parsed, err
}
func adoptTargets(files []gameres.File, packages []gameres.Package) ([]InstallTarget, error) {
	var targets []InstallTarget
	seen := map[string]bool{}
	add := func(path, hash string, size int64) error {
		normalized, err := gameres.NormalizePath(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(strings.ToLower(normalized), "client/") {
			normalized = "Client/WindowsNoEditor/" + normalized
		}
		if !strings.HasPrefix(strings.ToLower(normalized), "client/windowsnoeditor/") {
			return gameres.ErrPathUnsafe
		}
		key := strings.ToUpper(normalized)
		if seen[key] {
			return gameres.ErrMalformedManifest
		}
		seen[key] = true
		targets = append(targets, InstallTarget{Path: normalized, MD5: strings.ToLower(hash), Size: size})
		return nil
	}
	for _, f := range files {
		if err := add(f.Path, f.MD5, f.Size); err != nil {
			return nil, err
		}
	}
	for _, p := range packages {
		for _, e := range p.Entries {
			if err := add(e.Path, e.MD5, e.Size); err != nil {
				return nil, err
			}
		}
	}
	return targets, nil
}
func adoptCheckSizes(ctx context.Context, r *os.Root, targets []InstallTarget) ([]Mismatch, error) {
	var mismatches []Mismatch
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		st, err := r.Stat(target.Path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			mismatches = append(mismatches, Mismatch{target.Path, "missing"})
		case err != nil:
			return nil, err
		case !st.Mode().IsRegular():
			mismatches = append(mismatches, Mismatch{target.Path, "not_regular"})
		case st.Size() != target.Size:
			mismatches = append(mismatches, Mismatch{target.Path, "size"})
		}
	}
	return mismatches, nil
}
