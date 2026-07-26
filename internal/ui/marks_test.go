package ui

import (
	"sort"
	"testing"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The inheritance/split state machine, exercised without a terminal:
// marking a dataset covers its subtree; deselecting inside a covered
// subtree decomposes the ancestor into the zfs-true remainder (the
// ancestor survives — its snaps and untouched siblings become manual
// marks); marking over descendants absorbs them.

func marksFixture() (*Model, *hostState) {
	// pool p: p ─ a ─ {b (snap bs1), c, d} ; a has snaps s1, s2
	mkDs := func(name string, kids ...*zfs.Dataset) *zfs.Dataset {
		return &zfs.Dataset{Name: name, Children: kids}
	}
	b := mkDs("p/a/b")
	c := mkDs("p/a/c")
	d := mkDs("p/a/d")
	a := mkDs("p/a", b, c, d)
	root := mkDs("p", a)
	tree := &zfs.DatasetTree{
		ByName: map[string]*zfs.Dataset{"p": root, "p/a": a, "p/a/b": b, "p/a/c": c, "p/a/d": d},
		Roots:  []*zfs.Dataset{root},
	}
	h := newHostState("h", "", nil)
	h.dsTrees["p"] = tree
	h.dsSnaps["p/a"] = []*zfs.Snapshot{
		{Name: "p/a@s1", Snap: "s1", Creation: 1},
		{Name: "p/a@s2", Snap: "s2", Creation: 2},
	}
	h.dsSnaps["p/a/b"] = []*zfs.Snapshot{{Name: "p/a/b@bs1", Snap: "bs1", Creation: 3}}
	m := &Model{marks: map[string]bool{}, acks: map[string]string{}}
	h.acks = m.acks
	m.hosts = []*hostState{h}
	return m, h
}

func dsRow(h *hostState, tree *zfs.DatasetTree, name string, depth int) treeRow {
	return treeRow{kind: rDataset, host: h, ds: tree.ByName[name],
		id: treeDsID(h, name), depth: depth}
}

func markSet(m *Model) []string {
	var out []string
	for id := range m.marks {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func wantMarks(t *testing.T, m *Model, h *hostState, want ...string) {
	t.Helper()
	for i := range want {
		want[i] = treeDsID(h, want[i])
	}
	sort.Strings(want)
	got := markSet(m)
	if len(got) != len(want) {
		t.Fatalf("marks = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("marks = %q, want %q", got, want)
		}
	}
}

func TestMarkSplitChild(t *testing.T) {
	m, h := marksFixture()
	tree := h.dsTrees["p"]

	// mark a, then deselect covered child c: a survives — its snaps and
	// the untouched siblings become manual marks
	m.toggleMark(dsRow(h, tree, "p/a", 2))
	wantMarks(t, m, h, "p/a")
	m.toggleMark(dsRow(h, tree, "p/a/c", 3))
	wantMarks(t, m, h, "p/a@s1", "p/a@s2", "p/a/b", "p/a/d")
}

func TestMarkSplitOwnSnap(t *testing.T) {
	m, h := marksFixture()
	tree := h.dsTrees["p"]

	// deselecting one of a's own snaps keeps the other snap and ALL
	// children selected
	m.toggleMark(dsRow(h, tree, "p/a", 2))
	m.toggleMark(treeRow{kind: rSnap, host: h, ds: tree.ByName["p/a"],
		snap: h.dsSnaps["p/a"][0], id: treeDsID(h, "p/a@s1")})
	wantMarks(t, m, h, "p/a@s2", "p/a/b", "p/a/c", "p/a/d")
}

func TestMarkSplitPseudo(t *testing.T) {
	m, h := marksFixture()
	tree := h.dsTrees["p"]

	// a's snapshot list not loaded: the split mints the all-snaps pseudo,
	// which resolves into manual marks when a snap is later deselected
	delete(h.dsSnaps, "p/a")
	m.toggleMark(dsRow(h, tree, "p/a", 2))
	m.toggleMark(dsRow(h, tree, "p/a/c", 3))
	wantMarks(t, m, h, "p/a@", "p/a/b", "p/a/d")

	h.dsSnaps["p/a"] = []*zfs.Snapshot{
		{Name: "p/a@s1", Snap: "s1", Creation: 1},
		{Name: "p/a@s2", Snap: "s2", Creation: 2},
	}
	m.toggleMark(treeRow{kind: rSnap, host: h, ds: tree.ByName["p/a"],
		snap: h.dsSnaps["p/a"][0], id: treeDsID(h, "p/a@s1")})
	wantMarks(t, m, h, "p/a@s2", "p/a/b", "p/a/d")
}

func TestMarkAbsorb(t *testing.T) {
	m, h := marksFixture()
	tree := h.dsTrees["p"]

	// existing marks beneath vanish into the ancestor mark
	m.toggleMark(dsRow(h, tree, "p/a/b", 3))
	m.toggleMark(treeRow{kind: rSnap, host: h, ds: tree.ByName["p/a"],
		snap: h.dsSnaps["p/a"][1], id: treeDsID(h, "p/a@s2")})
	wantMarks(t, m, h, "p/a/b", "p/a@s2")
	m.toggleMark(dsRow(h, tree, "p/a", 2))
	wantMarks(t, m, h, "p/a")
}

func TestMarkGroupsAndTargets(t *testing.T) {
	m, h := marksFixture()
	tree := h.dsTrees["p"]

	m.toggleMark(dsRow(h, tree, "p/a/d", 3))
	m.MarkSnaps("h", "p/a", []string{"s1", "s2"})
	m.MarkSnaps("h", "p/a/b", []string{"bs1"})

	groups := m.markGroups()
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	targets := m.MarkTargets()
	want := map[string]bool{"p/a@s1,s2": true, "p/a/b@bs1": true}
	if len(targets) != 2 || !want[targets[0][1]] || !want[targets[1][1]] {
		t.Fatalf("targets = %v", targets)
	}
	// the dataset group answers with its recursive used
	for _, g := range groups {
		if g.dsMark && g.ds != "p/a/d" {
			t.Fatalf("wrong ds group: %+v", g)
		}
	}
	// root datasets refuse marks
	if m.toggleMark(dsRow(h, tree, "p", 1)) {
		t.Fatal("pool root accepted a mark")
	}
}
