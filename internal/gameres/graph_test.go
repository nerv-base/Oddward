// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"fmt"
	"testing"
)

func graphObject(seed int, size int64) Object {
	return Object{MD5: fmt.Sprintf("%032x", seed), Size: size}
}

func TestPatchGraphRoutesValidateHandBuiltEdges(t *testing.T) {
	source, target := graphObject(40, 100), graphObject(41, 100)
	bad := graphObject(42, 1<<62)
	good := graphObject(43, 10)
	unverified := Object{MD5: "invalid", Size: 100}
	graph := &PatchGraph{Edges: []PatchEntry{
		{Old: source, New: target, Blob: bad},
		{Old: source, New: unverified, Blob: good},
		{Old: unverified, New: target, Blob: good},
		{Old: source, New: target, Blob: good},
	}}
	if got := graph.Routes(source, target, nil, 4); len(got) != 1 || got[0].PatchBytes != 10 || len(got[0].Steps) != 1 {
		t.Fatalf("routes = %+v, want only the structurally valid direct route", got)
	}
}

func TestPatchGraphRouteBlocksAreIndependent(t *testing.T) {
	source, target := graphObject(40, 100), graphObject(41, 100)
	blob := graphObject(42, 10)
	graph := &PatchGraph{Edges: []PatchEntry{{
		Old: source, New: target, Blob: blob, Blocks: []Block{{Size: 10, MD5: blob.MD5}},
	}}}
	got := graph.Routes(source, target, nil, 4)
	if len(got) != 1 || len(got[0].Steps[0].Blocks) != 1 {
		t.Fatalf("routes = %+v, want one route preserving the blob block", got)
	}
	got[0].Steps[0].Blocks[0].Size = 1
	if again := graph.Routes(source, target, nil, 4); again[0].Steps[0].Blocks[0].Size != 10 {
		t.Fatal("editing a returned block changed the graph")
	}
}

func TestPatchGraphRoutesGuardExhaustiveSearch(t *testing.T) {
	source, target, deadEnd, blob := graphObject(900, 100), graphObject(901, 100), graphObject(902, 100), graphObject(903, 20)
	graph := &PatchGraph{Edges: []PatchEntry{{Old: source, New: target, Blob: blob}}}
	for i := range 32 {
		first := graphObject(100+i, 100)
		second := graphObject(200+i, 100)
		third := graphObject(300+i, 100)
		graph.Edges = append(graph.Edges, PatchEntry{Old: source, New: first, Blob: blob}, PatchEntry{Old: third, New: deadEnd, Blob: blob})
		for j := range 32 {
			graph.Edges = append(graph.Edges,
				PatchEntry{Old: first, New: graphObject(200+j, 100), Blob: blob},
				PatchEntry{Old: second, New: graphObject(300+j, 100), Blob: blob})
		}
	}
	t.Run("edge visits", func(t *testing.T) {
		visits := 0
		got, exhausted := graph.routes(source, target, nil, 4, func(PatchEntry) bool {
			visits++
			return true
		}, nil)
		if len(got) != 0 || !exhausted || visits > 65536 {
			t.Fatalf("got %d routes after %d visits, exhausted=%t, want no partial routes and at most 65536 visits", len(got), visits, exhausted)
		}
	})
	t.Run("candidate count", func(t *testing.T) {
		if got := graph.Routes(source, deadEnd, nil, 4); len(got) != 0 {
			t.Fatalf("returned %d routes, want none after exceeding the candidate limit", len(got))
		}
	})
	t.Run("depth", func(t *testing.T) {
		graph := &PatchGraph{}
		for i := 1; i <= 33; i++ {
			graph.Edges = append(graph.Edges, PatchEntry{Old: graphObject(i, 10), New: graphObject(i+1, 10), Blob: blob})
		}
		if got := graph.Routes(graphObject(1, 10), graphObject(34, 10), nil, 1<<20); len(got) != 0 {
			t.Fatalf("returned %d routes, want none beyond the defensive depth limit", len(got))
		}
	})
}

func TestPatchGraphRoutesDefaultToFourSteps(t *testing.T) {
	graph := &PatchGraph{}
	for i := 1; i <= 5; i++ {
		graph.Edges = append(graph.Edges, PatchEntry{Old: graphObject(i, 10), New: graphObject(i+1, 10), Blob: graphObject(100+i, 1)})
	}
	source := graphObject(1, 10)
	if got := graph.Routes(source, graphObject(5, 10), nil, 0); len(got) != 1 || len(got[0].Steps) != 4 {
		t.Fatalf("default length returned %v, want one four-step route", got)
	}
	if got := graph.Routes(source, graphObject(6, 10), nil, 0); len(got) != 0 {
		t.Fatalf("default length returned %v, want no five-step route", got)
	}
	if got := graph.Routes(source, graphObject(6, 10), nil, 5); len(got) != 0 {
		t.Fatalf("explicit length of 5 returned %v, want no path beyond four steps", got)
	}
}

func TestPatchGraphRoutesFilterBeforeExpansion(t *testing.T) {
	source, via, target := graphObject(30, 100), graphObject(31, 100), graphObject(32, 100)
	blob := graphObject(33, 10)
	graph := &PatchGraph{Edges: []PatchEntry{
		{Old: source, New: via, Blob: blob, Section: "tag-a"},
		{Old: via, New: target, Blob: blob},
		{Old: source, New: target, Blob: blob},
	}}
	got, exhausted := graph.routes(source, target, nil, 4, func(edge PatchEntry) bool {
		if edge.Old == via {
			t.Error("search reached a vertex behind an excluded edge")
		}
		return edge.Section == ""
	}, nil)
	if exhausted || len(got) != 1 || len(got[0].Steps) != 1 || got[0].Steps[0].Section != "" {
		t.Fatalf("routes = %+v, want one direct baseTag route", got)
	}
}

func TestPatchGraphRoutesOrdersByNetworkCostThenLength(t *testing.T) {
	source, via, target := graphObject(20, 100), graphObject(21, 100), graphObject(22, 100)
	expensive := graphObject(23, 9)
	shared := graphObject(24, 4)
	direct := graphObject(25, 4)
	cacheHit := graphObject(26, 8)
	graph := &PatchGraph{Edges: []PatchEntry{
		{Old: source, New: target, Blob: expensive},
		{Old: source, New: via, Blob: shared},
		{Old: via, New: target, Blob: shared},
		{Old: source, New: target, Blob: direct},
		{Old: source, New: target, Blob: cacheHit},
	}}
	got := graph.Routes(source, target, func(o Object) bool { return o == cacheHit }, 4)
	want := []struct {
		firstBlob Object
		bytes     int64
		steps     int
	}{
		{cacheHit, 0, 1},
		{direct, 4, 1},
		{shared, 4, 2},
		{expensive, 9, 1},
	}
	if len(got) != len(want) {
		t.Fatalf("routes = %v, want %d routes", got, len(want))
	}
	for i, w := range want {
		route := got[i]
		if route.PatchBytes != w.bytes || len(route.Steps) != w.steps || route.Steps[0].Blob != w.firstBlob {
			t.Errorf("route %d = %+v, want %+v", i, route, w)
		}
	}
}

func TestPatchGraphRoutesRequireContiguousAcyclicEdges(t *testing.T) {
	source, first, second, merge, target := graphObject(1, 100), graphObject(2, 100), graphObject(3, 100), graphObject(4, 100), graphObject(5, 100)
	mismatch := graphObject(4, 101)
	blob := graphObject(10, 20)
	graph := &PatchGraph{Edges: []PatchEntry{
		{Old: source, New: first, Blob: blob},
		{Old: source, New: second, Blob: blob},
		{Old: first, New: merge, Blob: blob},
		{Old: second, New: merge, Blob: blob},
		{Old: merge, New: target, Blob: blob},
		{Old: first, New: source, Blob: blob},    // cycle
		{Old: source, New: source, Blob: blob},   // self-loop
		{Old: mismatch, New: target, Blob: blob}, // same MD5 as merge, different size
	}}
	if got := graph.Routes(source, target, nil, 2); len(got) != 0 {
		t.Fatalf("found routes within 2 steps: %+v", got)
	}
	routes := graph.Routes(source, target, nil, 8)
	if len(routes) != 2 {
		t.Fatalf("route count = %d, want 2", len(routes))
	}
	for i, intermediate := range []Object{first, second} {
		got := routes[i]
		if got.Source != source || got.Target != target || len(got.Steps) != 3 ||
			got.Steps[0].New != intermediate || got.Steps[1].New != merge || got.Steps[2].New != target {
			t.Errorf("route %d = %+v, want source -> %v -> %v -> target", i, got, intermediate, merge)
		}
	}
	if got := graph.Routes(source, source, nil, 8); len(got) != 0 {
		t.Errorf("identity already matches, got patch routes %+v", got)
	}
}
