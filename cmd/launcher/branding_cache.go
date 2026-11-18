// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"time"

	"github.com/nerv-base/Oddward/internal/brandingcache"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/news"
)

type brandingCacheAdapter struct {
	cache *brandingcache.Cache
}

func (a brandingCacheAdapter) Load(ctx context.Context) (launcher.BrandingRecord, error) {
	record, err := a.cache.Load(ctx)
	if err != nil {
		return launcher.BrandingRecord{}, err
	}
	return launcher.BrandingRecord{
		Image:       record.Image,
		Slogan:      sloganFromMetadata(record.Metadata),
		VersionName: versionFromMetadata(record.Metadata),
	}, nil
}

func (a brandingCacheAdapter) Save(ctx context.Context, record launcher.BrandingRecord) error {
	return a.cache.Save(ctx, brandingcache.Record{
		Image: record.Image,
		Metadata: brandingcache.Metadata{
			CoverCode:     record.Slogan.CoverCode,
			URL:           record.Slogan.URL,
			ETag:          record.Slogan.ETag,
			MD5:           record.Slogan.MD5,
			Bytes:         record.Slogan.Bytes,
			Width:         record.Slogan.Width,
			Height:        record.Slogan.Height,
			LastModified:  record.Slogan.LastModified,
			VersionNumber: record.VersionName.Number,
			VersionName:   record.VersionName.Name,
			ArticleURL:    record.VersionName.ArticleURL,
			FetchedAt:     time.Now().UTC(),
		},
	})
}

func sloganFromMetadata(metadata brandingcache.Metadata) news.Slogan {
	return news.Slogan{
		CoverCode:    metadata.CoverCode,
		URL:          metadata.URL,
		ETag:         metadata.ETag,
		MD5:          metadata.MD5,
		Bytes:        metadata.Bytes,
		Width:        metadata.Width,
		Height:       metadata.Height,
		LastModified: metadata.LastModified,
	}
}

func versionFromMetadata(metadata brandingcache.Metadata) news.VersionName {
	return news.VersionName{
		Number:     metadata.VersionNumber,
		Name:       metadata.VersionName,
		ArticleURL: metadata.ArticleURL,
	}
}
