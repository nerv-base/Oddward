// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"cmp"
	"slices"
)

const (
	defaultChainSteps = 4
	maxPatchEdges     = 4096
	maxEdgeVisits     = 65_536
	maxRouteResults   = 4096
)

// PatchGraph holds patch edges in document order. Treat Edges as read-only.
type PatchGraph struct {
	Edges []PatchEntry
	byOld map[Object][]int
}

func indexPatchEdges(edges []PatchEntry) map[Object][]int {
	byOld := make(map[Object][]int)
	for index, edge := range edges {
		if validObject(edge.Old, maxFileBytes) && validObject(edge.New, maxFileBytes) &&
			validObject(edge.Blob, maxFileBytes) && len(edge.Blocks) <= maxBlocks {
			byOld[edge.Old] = append(byOld[edge.Old], index)
		}
	}
	return byOld
}

// Route is a chain of patches to apply from a trusted source to a target.
type Route struct {
	Source     Object
	Steps      []PatchEntry
	Target     Object
	PatchBytes int64
}

// Routes returns acyclic chains in order of missing blob bytes, step count,
// and document order. A nonpositive maxSteps defaults to four, the supported
// maximum. If the graph needs more work than allowed, Routes returns no paths.
func (g *PatchGraph) Routes(source, target Object, cached func(Object) bool, maxSteps int) []Route {
	routes, _ := g.routes(source, target, cached, maxSteps, nil, nil)
	for i := range routes {
		routes[i].cloneBlocks()
	}
	return routes
}

func (r *Route) cloneBlocks() {
	for i := range r.Steps {
		r.Steps[i].Blocks = slices.Clone(r.Steps[i].Blocks)
	}
}

// routes reports exhaustion and leaves each edge's block table shared until
// the caller publishes a route. remaining bounds visits across searches.
func (g *PatchGraph) routes(source, target Object, cached func(Object) bool, maxSteps int, eligible func(PatchEntry) bool, remaining *int) ([]Route, bool) {
	if g == nil || source == target {
		return nil, false
	}
	if len(g.Edges) > maxPatchEdges {
		return nil, true
	}
	if maxSteps <= 0 || maxSteps > defaultChainSteps {
		maxSteps = defaultChainSteps
	}
	byOld := g.byOld
	if byOld == nil {
		byOld = indexPatchEdges(g.Edges)
	}
	var routes []Route
	var steps []PatchEntry
	seen := map[Object]bool{source: true}
	visits := 0
	var walk func(Object) bool
	walk = func(current Object) bool {
		if len(steps) == maxSteps {
			return true
		}
		for _, index := range byOld[current] {
			if visits == maxEdgeVisits || remaining != nil && *remaining == 0 {
				return false
			}
			visits++
			if remaining != nil {
				*remaining -= 1
			}
			edge := g.Edges[index]
			if seen[edge.New] || (eligible != nil && !eligible(edge)) {
				continue
			}
			steps = append(steps, edge)
			if edge.New == target {
				if len(routes) == maxRouteResults {
					return false
				}
				routes = append(routes, Route{Source: source, Target: target, Steps: slices.Clone(steps)})
			} else {
				seen[edge.New] = true
				if !walk(edge.New) {
					return false
				}
				delete(seen, edge.New)
			}
			steps = steps[:len(steps)-1]
		}
		return true
	}
	if !walk(source) {
		return nil, true
	}
	cacheHits := make(map[Object]bool)
	seenBlobs := make(map[Object]bool)
	for i := range routes {
		clear(seenBlobs)
		for _, edge := range routes[i].Steps {
			blob := edge.Blob
			if seenBlobs[blob] {
				continue
			}
			seenBlobs[blob] = true
			if cached != nil {
				hit, checked := cacheHits[blob]
				if !checked {
					hit = cached(blob)
					cacheHits[blob] = hit
				}
				if hit {
					continue
				}
			}
			routes[i].PatchBytes += blob.Size
		}
	}
	// Stable sorting preserves document order at each divergence on a tie.
	slices.SortStableFunc(routes, func(a, b Route) int {
		return cmp.Or(cmp.Compare(a.PatchBytes, b.PatchBytes), cmp.Compare(len(a.Steps), len(b.Steps)))
	})
	return routes, false
}
