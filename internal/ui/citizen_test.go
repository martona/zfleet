package ui

import (
	"testing"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// Snapshot citizenship: datasets with snapshots earn real chevrons, the
// unknown state wears a dim · until the pool sweep lands, children render
// before snapshots, and t folds snapshots out of an open view.

func citizenFixture() (*Model, *hostState) {
	m, h := destroyFixture()
	h.pools = []*zfs.Pool{{Name: "p"}}
	m.expanded[treePoolID(h, "p")] = true
	m.expanded[treeDsID(h, "p")] = true
	m.expanded[treeDsID(h, "p/a")] = true
	return m, h
}

func findRow(t *testing.T, m *Model, id string) treeRow {
	t.Helper()
	m.rowsOK = false
	for _, r := range m.treeRows() {
		if r.id == id {
			return r
		}
	}
	t.Fatalf("row %q not in tree", id)
	return treeRow{}
}

func rowIdx(t *testing.T, m *Model, id string) int {
	t.Helper()
	m.rowsOK = false
	for i, r := range m.treeRows() {
		if r.id == id {
			return i
		}
	}
	t.Fatalf("row %q not in tree", id)
	return -1
}

func TestCitizenChevrons(t *testing.T) {
	m, h := citizenFixture()

	// pre-sweep: b has a loaded list (bs1) → real chevron, and violet: its
	// expansion holds only snapshots. a has children → plain chevron.
	b := findRow(t, m, treeDsID(h, "p/a/b"))
	if !b.expandable || b.chevUnknown || !b.snapOnly {
		t.Fatalf("b (known snaps) = expandable %v unknown %v snapOnly %v",
			b.expandable, b.chevUnknown, b.snapOnly)
	}
	if a := findRow(t, m, treeDsID(h, "p/a")); a.snapOnly {
		t.Fatal("a has child datasets — a violet chevron there is the jebait inverted")
	}
	c := findRow(t, m, treeDsID(h, "p/a/c"))
	if !c.expandable || !c.chevUnknown {
		t.Fatalf("c (pre-sweep) = expandable %v unknown %v, want ·", c.expandable, c.chevUnknown)
	}

	// an empty sweep is authoritative: c is known-none (chevron retracts)
	// and even b's stale cached list resets — snaps died outside the cache
	m.ApplySweep("h", "p", "")
	c = findRow(t, m, treeDsID(h, "p/a/c"))
	if c.expandable || c.chevUnknown {
		t.Fatalf("c (post-sweep) = expandable %v unknown %v, want plain leaf", c.expandable, c.chevUnknown)
	}
	b = findRow(t, m, treeDsID(h, "p/a/b"))
	if b.expandable {
		t.Fatal("b kept its chevron after the sweep said its snaps are gone")
	}
	if !h.snapSwept["p"] {
		t.Fatal("sweep did not mark the pool's universe known")
	}

	// t on a known-none dataset must refuse — an empty ▾ is a jebait
	m.rowsOK = false
	m.treeSel = treeDsID(h, "p/a/c")
	m.toggleSnapsFold()
	if m.expanded[treeDsID(h, "p/a/c")] {
		t.Fatal("t opened an empty view on a dataset known to have no snaps")
	}
}

func TestCitizenOrderAndFold(t *testing.T) {
	m, h := citizenFixture()

	// children before snapshots (Marton's ordering)
	if d, s := rowIdx(t, m, treeDsID(h, "p/a/d")), rowIdx(t, m, treeDsID(h, "p/a@s1")); d > s {
		t.Fatalf("child at %d renders after snapshot at %d", d, s)
	}

	// t on the dataset folds its snaps; children stay
	m.rowsOK = false
	m.treeSel = treeDsID(h, "p/a")
	if _, ok := m.toggleSnapsFold(); !ok {
		t.Fatal("t refused a dataset row")
	}
	m.rowsOK = false
	for _, r := range m.treeRows() {
		if r.kind == rSnap && r.ds.Name == "p/a" {
			t.Fatal("snap row survived the fold")
		}
	}
	findRow(t, m, treeDsID(h, "p/a/b")) // children still present

	// t again brings them back
	m.rowsOK = false
	m.toggleSnapsFold()
	findRow(t, m, treeDsID(h, "p/a@s1"))

	// t from a snap row folds and parks the cursor on the dataset
	m.rowsOK = false
	m.treeSel = treeDsID(h, "p/a@s1")
	m.toggleSnapsFold()
	if m.treeSel != treeDsID(h, "p/a") {
		t.Fatalf("cursor = %q, want the owning dataset", m.treeSel)
	}

	// → re-expands with everything: the fold is a view op, not a mode
	m.rowsOK = false
	m.expanded[treeDsID(h, "p/a")] = true // still expanded (has children)
	delete(m.snapsFolded, treeDsID(h, "p/a"))
	findRow(t, m, treeDsID(h, "p/a@s1"))
}

func TestCitizenFoldChildless(t *testing.T) {
	m, h := citizenFixture()
	// b is childless with one snap: folding its open view collapses it
	// whole — an open dataset with nothing under it is a broken chevron
	m.expanded[treeDsID(h, "p/a/b")] = true
	m.rowsOK = false
	m.treeSel = treeDsID(h, "p/a/b")
	m.toggleSnapsFold()
	if m.expanded[treeDsID(h, "p/a/b")] || m.snapsFolded[treeDsID(h, "p/a/b")] {
		t.Fatal("childless fold should collapse cleanly, leaving no state")
	}
}
