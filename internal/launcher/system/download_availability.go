// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

const downloadProbeLimit = 64
const downloadProbeTTL = 10 * time.Minute

type cachedDownloadAvailability struct {
	branch   string
	objects  []gameres.Object
	snapshot gameres.AvailabilitySnapshot
}

func (b *GameDownloadBackend) downloadAvailability(ctx context.Context, branch string, objects []gameres.Object, now time.Time) gameres.AvailabilitySnapshot {
	selected := make([]gameres.Object, 0, min(downloadProbeLimit, len(objects)))
	unique := make(map[gameres.Object]bool)
	for _, object := range objects {
		if !unique[object] {
			unique[object] = true
			selected = append(selected, object)
			if len(selected) == downloadProbeLimit {
				break
			}
		}
	}
	if len(selected) == 0 {
		return gameres.AvailabilitySnapshot{}
	}
	b.mu.Lock()
	previous := b.availability
	if previous.branch == branch && slices.Equal(previous.objects, selected) && !now.Before(previous.snapshot.At) && now.Sub(previous.snapshot.At) < downloadProbeTTL {
		b.mu.Unlock()
		previous.snapshot.Objects = maps.Clone(previous.snapshot.Objects)
		return previous.snapshot
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m := b.metadata()
	client := *m.client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	statuses := make([]gameres.ObjectStatus, len(selected))
	jobs := make(chan int, len(selected))
	for i := range selected {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(8, len(selected)) {
		workers.Go(func() {
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				statuses[index] = probeDownloadObject(ctx, &client, m.roots, branch, selected[index])
			}
		})
	}
	workers.Wait()

	snapshot := gameres.AvailabilitySnapshot{At: now, TTL: downloadProbeTTL, Objects: make(map[gameres.Object]gameres.ObjectStatus, len(selected))}
	for i, o := range selected {
		snapshot.Objects[o] = statuses[i]
	}
	if ctx.Err() == nil {
		b.mu.Lock()
		b.availability = cachedDownloadAvailability{branch: branch, objects: selected, snapshot: snapshot}
		b.mu.Unlock()
		snapshot.Objects = maps.Clone(snapshot.Objects)
	}
	return snapshot
}

func probeDownloadObject(ctx context.Context, client *http.Client, roots []string, branch string, object gameres.Object) gameres.ObjectStatus {
	allGone := len(roots) > 0
	for _, root := range roots {
		address, err := gameres.CASURL(root, branch, object)
		if err != nil {
			return gameres.StatusUnknown
		}
		status := probeDownloadURL(ctx, client, address, object.Size)
		if status == gameres.StatusReachable {
			return status
		}
		allGone = allGone && status == gameres.StatusGone
	}
	if allGone {
		return gameres.StatusGone
	}
	return gameres.StatusUnknown
}

func probeDownloadURL(ctx context.Context, client *http.Client, address string, size int64) gameres.ObjectStatus {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, address, nil)
	if err != nil {
		return gameres.StatusUnknown
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := client.Do(req)
	if err != nil {
		return gameres.StatusUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return gameres.StatusGone
	}
	encoding := resp.Header.Get("Content-Encoding")
	if resp.StatusCode == http.StatusOK && resp.ContentLength == size && !resp.Uncompressed && (encoding == "" || encoding == "identity") {
		return gameres.StatusReachable
	}
	return gameres.StatusUnknown
}

func downloadProbeCandidates(manifest gameres.ResList, graph *gameres.PatchGraph, local downloadLocal, patch bool) []gameres.Object {
	var objects []gameres.Object
	needed := func(path string, object gameres.Object) bool {
		return !local.Trusted(path, object) && !local.Cached(object)
	}
	// Probe Pak containers first, then entry CAS counterparts and standalone files.
	for _, pack := range manifest.Packages {
		for _, entry := range pack.Entries {
			if needed(entry.Path, gameres.Object{MD5: entry.MD5, Size: entry.Size}) {
				objects = append(objects, pack.Pak)
				break
			}
		}
	}
	if patch && graph != nil {
		for _, entry := range relevantPatchEdges(manifest, graph, local, needed) {
			if !local.Cached(entry.Blob) {
				objects = append(objects, entry.Blob)
			}
		}
	}
	for _, pack := range manifest.Packages {
		for _, entry := range pack.Entries {
			object := gameres.Object{MD5: entry.MD5, Size: entry.Size}
			if needed(entry.Path, object) {
				objects = append(objects, object)
			}
		}
	}
	for _, file := range manifest.Files {
		object := gameres.Object{MD5: file.MD5, Size: file.Size}
		if needed(file.Path, object) {
			objects = append(objects, object)
		}
	}
	return objects
}

// relevantPatchEdges returns only baseTag patch edges that lie on at least one
// directed path from an installed object for a needed target to a needed
// manifest target. Historical edges from unrelated versions are skipped so
// bounded HEAD probing checks only the active update's candidate blobs.
func relevantPatchEdges(manifest gameres.ResList, graph *gameres.PatchGraph, local downloadLocal, needed func(string, gameres.Object) bool) []gameres.PatchEntry {
	if graph == nil || len(graph.Edges) == 0 {
		return nil
	}
	starts := make(map[gameres.Object]bool)
	wants := make(map[gameres.Object]bool)
	collect := func(path string, target gameres.Object) {
		if !needed(path, target) {
			return
		}
		wants[target] = true
		if old, ok := local.Identity(path); ok {
			starts[old] = true
		}
	}
	for _, pack := range manifest.Packages {
		for _, entry := range pack.Entries {
			collect(entry.Path, gameres.Object{MD5: entry.MD5, Size: entry.Size})
		}
	}
	for _, file := range manifest.Files {
		collect(file.Path, gameres.Object{MD5: file.MD5, Size: file.Size})
	}
	if len(starts) == 0 || len(wants) == 0 {
		return nil
	}
	byOld := make(map[gameres.Object][]int)
	byNew := make(map[gameres.Object][]int)
	for i, edge := range graph.Edges {
		if edge.Section != "" {
			continue
		}
		byOld[edge.Old] = append(byOld[edge.Old], i)
		byNew[edge.New] = append(byNew[edge.New], i)
	}
	forward := make(map[gameres.Object]bool, len(starts))
	queue := make([]gameres.Object, 0, len(starts))
	for obj := range starts {
		forward[obj] = true
		queue = append(queue, obj)
	}
	for head := 0; head < len(queue); head++ {
		for _, idx := range byOld[queue[head]] {
			next := graph.Edges[idx].New
			if !forward[next] {
				forward[next] = true
				queue = append(queue, next)
			}
		}
	}
	backward := make(map[gameres.Object]bool, len(wants))
	queue = queue[:0]
	for obj := range wants {
		if forward[obj] {
			backward[obj] = true
			queue = append(queue, obj)
		}
	}
	for head := 0; head < len(queue); head++ {
		for _, idx := range byNew[queue[head]] {
			prev := graph.Edges[idx].Old
			if forward[prev] && !backward[prev] {
				backward[prev] = true
				queue = append(queue, prev)
			}
		}
	}
	var out []gameres.PatchEntry
	for _, edge := range graph.Edges {
		if edge.Section == "" && forward[edge.Old] && backward[edge.New] {
			out = append(out, edge)
		}
	}
	return out
}
