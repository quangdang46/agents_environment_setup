package resolver

import (
	"fmt"
	"sort"
	"strings"
)

// TopoSort orders tools so that every dependency appears before the tool that
// needs it.
//
// deps maps a tool to its direct dependencies. Every key in deps is expected in
// the output, including those with no dependencies of their own — a tool with
// an empty dependency list still has a position in the install order.
//
// Ties are broken by name, so the same graph always produces the same order.
// That is a contract (I8): the resolver's output has to be byte-identical
// across runs, and an arbitrary tie-break would make it depend on Go's map
// iteration order.
//
// A cycle is an error. TopoSort never returns a partial order, and never loops
// forever waiting for a node that will not become ready (I11).
func TopoSort(deps map[string][]string) ([]string, error) {
	// remaining[n] counts the dependencies of n not yet emitted. A node becomes
	// ready when this reaches zero.
	remaining := make(map[string]int, len(deps))
	// dependents[n] is who is waiting on n, so emitting a node decrements
	// exactly its direct dependents instead of rescanning the graph.
	dependents := make(map[string][]string, len(deps))
	for name, ds := range deps {
		remaining[name] = len(ds)
		for _, d := range ds {
			dependents[d] = append(dependents[d], name)
		}
	}
	for k := range dependents {
		sort.Strings(dependents[k])
	}

	out := make([]string, 0, len(deps))
	done := make(map[string]bool, len(deps))
	for len(out) < len(deps) {
		ready := readyNode(remaining, done)
		if ready == "" {
			// Everything left is in a cycle, or waits on one.
			return nil, cycleError(deps, done)
		}
		done[ready] = true
		out = append(out, ready)
		for _, dep := range dependents[ready] {
			remaining[dep]--
		}
	}
	return out, nil
}

// readyNode returns the smallest unemitted node whose dependencies have all
// been emitted, or "" when nothing is available.
func readyNode(remaining map[string]int, done map[string]bool) string {
	ready := ""
	for name, r := range remaining {
		if r == 0 && !done[name] && (ready == "" || name < ready) {
			ready = name
		}
	}
	return ready
}

// cycleError builds the error for an unsatisfiable graph, naming the actual
// cycle so its author can find it. An error that only says "cycle" sends the
// reader hunting through the whole catalog.
func cycleError(deps map[string][]string, done map[string]bool) error {
	// Nodes still unemitted are exactly those in a cycle, or downstream of one.
	var stuck []string
	for name := range deps {
		if !done[name] {
			stuck = append(stuck, name)
		}
	}
	sort.Strings(stuck)

	if path := findCycle(deps, done); len(path) > 0 {
		return fmt.Errorf("%w: %s", ErrCycle, strings.Join(path, " -> "))
	}
	return fmt.Errorf("%w involving %s", ErrCycle, strings.Join(stuck, ", "))
}

// findCycle walks the unemitted part of the graph depth-first and returns the
// first cycle it reaches as a closed path ("a -> b -> a"), or nil if there is
// none. Nodes already fully explored are memoized, so a wide diamond costs one
// visit per node rather than one per path through it.
func findCycle(deps map[string][]string, done map[string]bool) []string {
	var start string
	for name := range deps {
		if done[name] {
			continue
		}
		if start == "" || name < start {
			start = name
		}
	}
	if start == "" {
		return nil
	}

	var path []string
	onPath := make(map[string]bool, len(deps))
	explored := make(map[string]bool, len(deps))

	var visit func(string) []string
	visit = func(n string) []string {
		if onPath[n] {
			// Close the loop: from n onward in path is the cycle.
			for i, p := range path {
				if p == n {
					cycle := make([]string, 0, len(path)-i+1)
					cycle = append(cycle, path[i:]...)
					return append(cycle, n)
				}
			}
		}
		if explored[n] {
			return nil
		}
		onPath[n] = true
		path = append(path, n)
		edges := append([]string(nil), deps[n]...)
		sort.Strings(edges)
		for _, d := range edges {
			if done[d] {
				continue
			}
			if cycle := visit(d); cycle != nil {
				return cycle
			}
		}
		path = path[:len(path)-1]
		onPath[n] = false
		explored[n] = true
		return nil
	}
	return visit(start)
}
