package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The filter bar must be modeless: arrows walk the results directly from
// input mode (keeping the pattern), enter keeps, esc cancels, and
// backspacing an empty bar closes it.

func TestFilterNavigation(t *testing.T) {
	m, h := marksFixture()
	h.pools = []*zfs.Pool{{Name: "p"}}
	m.treeSel = overviewID
	press := func(msg tea.KeyMsg) { m.treeKeys(msg) }

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
	press := func(msg tea.KeyMsg) { m.treeKeys(msg) }
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
