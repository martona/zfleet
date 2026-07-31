package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfleet/internal/zfs"
)

// The filter bar must be modeless: arrows walk the results directly from
// input mode (keeping the pattern), enter keeps, esc cancels, and
// backspacing an empty bar closes it.

func TestFilterNavigation(t *testing.T) {
	m, h := marksFixture()
	h.pools = []*zfs.Pool{{Name: "p"}}
	m.treeSel = overviewID
	press := func(msg tea.KeyMsg) { m.rowsOK = false; m.treeKeys(msg); m.rowsOK = false }

	// arrows straight out of input mode: filter survives, cursor moves
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	press(tea.KeyMsg{Type: tea.KeyDown})
	if m.filterIn || m.filter != "a" {
		t.Fatalf("down from input: filterIn=%v filter=%q", m.filterIn, m.filter)
	}
	if m.treeSel == overviewID {
		t.Fatalf("down from input did not move off the overview")
	}

	// enter keeps the filter; further downs walk results
	m.filter, m.filterIn, m.treeSel = "", false, overviewID
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	press(tea.KeyMsg{Type: tea.KeyEnter})
	press(tea.KeyMsg{Type: tea.KeyDown})
	if m.filterIn || m.filter != "a" || m.treeSel == overviewID {
		t.Fatalf("enter+down: filterIn=%v filter=%q sel=%q", m.filterIn, m.filter, m.treeSel)
	}

	// backspace erases; on an empty bar it closes the input like esc
	m.filter, m.filterIn = "", false
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	press(tea.KeyMsg{Type: tea.KeyBackspace})
	if !m.filterIn || m.filter != "" {
		t.Fatalf("backspace erase: filterIn=%v filter=%q", m.filterIn, m.filter)
	}
	press(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.filterIn {
		t.Fatal("backspace on an empty bar did not close it")
	}
}

// While filtering, space addresses only what the pattern matched: snap
// rows in @-hunts (dataset rows are chrome and skip), hit dataset rows in
// name hunts (ancestors skip).
func TestFilterSpaceSemantics(t *testing.T) {
	m, h := marksFixture()
	h.pools = []*zfs.Pool{{Name: "p"}}
	press := func(msg tea.KeyMsg) { m.rowsOK = false; m.treeKeys(msg); m.rowsOK = false }
	space := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}

	// @-hunt: the dataset row skips (cursor still moves), the snap marks
	m.filter = "@s"
	m.treeSel = treeDsID(h, "p/a")
	press(space)
	if len(m.marks) != 0 {
		t.Fatalf("@-hunt ds row took a mark: %v", m.marks)
	}
	if m.treeSel == treeDsID(h, "p/a") {
		t.Fatal("@-hunt ds row skip did not move the cursor")
	}
	if m.treeSel != treeDsID(h, "p/a@s1") {
		t.Fatalf("cursor = %q, want the first snap", m.treeSel)
	}
	press(space)
	if !m.marks[treeDsID(h, "p/a@s1")] {
		t.Fatalf("snap row did not mark: %v", m.marks)
	}

	// name hunt: the hit dataset row marks whole (-r), ancestors skip
	m.clearMarks()
	m.filter = "b"
	m.treeSel = treeDsID(h, "p/a") // dim ancestor of the hit p/a/b
	press(space)
	if len(m.marks) != 0 {
		t.Fatalf("name-hunt ancestor took a mark: %v", m.marks)
	}
	m.treeSel = treeDsID(h, "p/a/b")
	press(space)
	if !m.marks[treeDsID(h, "p/a/b")] {
		t.Fatalf("name-hunt hit ds did not mark whole: %v", m.marks)
	}
}

// Held space at the end of a list must not flip the last row by buffer
// parity: consecutive spaces on an unmoved cursor count once; any other
// key re-arms the guard.
func TestSpaceFloodGuard(t *testing.T) {
	m, h := marksFixture()
	h.pools = []*zfs.Pool{{Name: "p"}}
	press := func(msg tea.KeyMsg) { m.rowsOK = false; m.treeKeys(msg); m.rowsOK = false }
	space := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}

	m.filter = "@s"
	rows := m.treeRows()
	last := rows[len(rows)-1]
	if last.kind != rSnap {
		t.Fatalf("fixture: last filter row is kind %d, want a snap", last.kind)
	}
	m.treeSel = last.id
	press(space)
	if !m.marks[last.id] {
		t.Fatal("first space did not mark the last row")
	}
	press(space)
	press(space)
	if !m.marks[last.id] {
		t.Fatal("buffered spaces flipped the last row by parity")
	}
	press(tea.KeyMsg{Type: tea.KeyDown}) // re-arms the guard
	press(space)
	if m.marks[last.id] {
		t.Fatal("deliberate re-toggle after another key did not unmark")
	}
}

// `*` is the one-key vacuum: toggle-select everything the filter matched.
// Direct marks only — inherited stars belong to their dataset mark.
func TestBulkToggle(t *testing.T) {
	m, h := marksFixture()
	h.pools = []*zfs.Pool{{Name: "p"}}
	press := func(msg tea.KeyMsg) { m.rowsOK = false; m.treeKeys(msg); m.rowsOK = false }
	star := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("*")}

	m.filter = "@s"
	press(star)
	if len(m.marks) != 3 { // p/a@s1, p/a@s2, p/a/b@bs1
		t.Fatalf("bulk select: marks = %v", m.marks)
	}
	press(star)
	if len(m.marks) != 0 {
		t.Fatalf("bulk deselect: marks = %v", m.marks)
	}

	// inherited stars survive *: mark ds p/a/b (covers bs1); * then only
	// toggles the uncovered s1/s2
	m.rowsOK = false
	m.toggleMark(dsRow(h, h.dsTrees["p"], "p/a/b", 3))
	m.rowsOK = false
	press(star) // selects the uncovered s1, s2
	if !m.marks[treeDsID(h, "p/a@s1")] || !m.marks[treeDsID(h, "p/a@s2")] ||
		!m.marks[treeDsID(h, "p/a/b")] || m.marks[treeDsID(h, "p/a/b@bs1")] {
		t.Fatalf("bulk select with cover: marks = %v", m.marks)
	}
	press(star) // clears s1/s2, leaves the dataset mark (and its cover) intact
	if m.marks[treeDsID(h, "p/a@s1")] || !m.marks[treeDsID(h, "p/a/b")] {
		t.Fatalf("bulk deselect with cover: marks = %v", m.marks)
	}
	// everything covered by one dataset mark: * refuses — not its to break
	m.clearMarks()
	m.rowsOK = false
	m.toggleMark(dsRow(h, h.dsTrees["p"], "p/a", 2)) // covers every match
	m.rowsOK = false
	press(star)
	if !m.marks[treeDsID(h, "p/a")] || len(m.marks) != 1 {
		t.Fatalf("bulk on all-inherited: marks = %v", m.marks)
	}
}
