// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nerv-base/Oddward/internal/gameres"
)

func (b *GameDownloadBackend) externalDownloadCache(root, selected, key string) (string, string, error) {
	root, err := downloadDirectory(root, true)
	if err != nil {
		return "", "", err
	}
	selected, err = downloadDirectory(selected, false)
	if err != nil {
		return "", "", err
	}
	for _, protected := range []string{root, b.StateRoot} {
		if protected != "" && (downloadPathWithin(protected, selected) || downloadPathWithin(selected, protected)) {
			return "", "", gameres.ErrPathUnsafe
		}
	}
	volumeKey := b.downloadVolume
	if volumeKey == nil {
		volumeKey = downloadPerfVolumeKey
	}
	destination := root
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		destination = filepath.Dir(root)
	} else if err != nil {
		return "", "", err
	}
	installVolume, err := volumeKey(destination)
	if err != nil {
		return "", "", err
	}
	cacheVolume, err := volumeKey(selected)
	if err != nil {
		return "", "", err
	}
	if installVolume == cacheVolume {
		return "", "", errors.New("请选择与安装目录不同的本地卷")
	}
	work, err := downloadDirectory(filepath.Join(selected, "Oddward-cache-"+key), true)
	return selected, work, err
}

func downloadPathWithin(parent, child string) bool {
	relative, err := filepath.Rel(strings.ToUpper(parent), strings.ToUpper(child))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, `..\`) && !filepath.IsAbs(relative)
}

// The destination needs an independent file copy for every placement, even
// when multiple placements share one verified cache object.
func (p *downloadPlan) externalInstallBytes() int64 {
	var total int64
	for _, target := range p.plan.Targets {
		total += target.Size
	}
	return total + p.margin
}

func (b *GameDownloadBackend) budgetExternalDownload(p *downloadPlan) error {
	p.cacheBudget = false
	installFree, err := b.space(p.root)
	if err != nil {
		return err
	}
	cacheFree, err := b.space(p.externalCache)
	if err != nil {
		return err
	}
	p.need, p.free = p.externalInstallBytes(), installFree
	if need := p.plan.DownloadBytes + p.margin; need > cacheFree && p.need <= p.free {
		p.need, p.free, p.cacheBudget = need, cacheFree, true
	}
	return nil
}

func (b *GameDownloadBackend) prepareExternalDownload(p *downloadPlan) error {
	_, work, err := b.externalDownloadCache(p.root, p.externalCache, p.key)
	if err != nil {
		return err
	}
	if !strings.EqualFold(work, p.workRoot) {
		return gameres.ErrPathUnsafe
	}
	parent, err := os.OpenRoot(p.externalCache)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := parent.Mkdir(filepath.Base(work), 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	_, err = downloadDirectory(work, false)
	return err
}

func (b *GameDownloadBackend) checkExternalDownloadSpace(p *downloadPlan, want int64) error {
	installFree, err := b.space(p.root)
	if err != nil {
		return err
	}
	if need := p.externalInstallBytes(); installFree < need {
		return fmt.Errorf("安装卷空间不足（需要 %d 字节，可用 %d 字节）: %w", need, installFree, gameres.ErrInsufficientSpace)
	}
	cacheFree, err := b.space(p.externalCache)
	if err != nil {
		return err
	}
	if cacheFree < want {
		return fmt.Errorf("下载暂存卷空间不足（需要 %d 字节，可用 %d 字节）: %w", want, cacheFree, gameres.ErrInsufficientSpace)
	}
	return nil
}
