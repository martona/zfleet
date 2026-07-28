package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The cheat line: the NC F-bar reborn as a context line. A key appears only
// when it does something right now, but keys always render in one canonical
// order — per screen, the tail is constant and only the head pair changes,
// in place, as the cursor crosses row types. No jumping around.

type keyHint struct{ key, label string } // key == "" renders label-only

func (m *Model) cheatHints() []keyHint {
	hints := m.cheatBase()
	// the scroll hint earns its slot only when there is something to scroll
	if m.rightOverflow {
		hints = append(hints, keyHint{"j/k", "scrl right"})
	}
	return hints
}

func (m *Model) cheatBase() []keyHint {
	if m.ackPop {
		return []keyHint{{"enter", "ack"}, {"↑↓", "move"}, {"esc", "close"}}
	}
	if m.filterIn {
		return []keyHint{{"", "type to filter…"}, {"↓", "results"},
			{"enter", "keep"}, {"esc", "cancel"}}
	}
	row := m.treeSelected()
	if m.inMarkContext(row) {
		// the standard tail stays: / and s keep working with a selection open
		return []keyHint{{"space", "toggle"}, {"esc", "clear all"},
			{"/", "filter"}, {"s", "sort"}, {"q", "quit"}}
	}
	if m.filter != "" {
		head := []keyHint{{"esc", "clear filter"}}
		if m.filterMarkable(row) {
			head = append(head, keyHint{"space", "select"})
		}
		return append(head, keyHint{"/", "filter"}, keyHint{"s", "sort"},
			keyHint{"q", "quit"})
	}
	vLabel := "all checks"
	if m.verboseDrives {
		vLabel = "warns only"
	}
	var head []keyHint
	switch row.kind {
	case rOverview:
		head = []keyHint{{"↓", "browse"}}
	case rHost:
		head = []keyHint{{"v", vLabel}}
	case rPool:
		if row.expanded {
			head = append(head, keyHint{"←", "collapse"})
		} else {
			head = append(head, keyHint{"→", "expand"})
		}
		head = append(head, keyHint{"v", vLabel})
	case rDataset:
		switch {
		case row.expanded:
			head = append(head, keyHint{"←", "collapse"})
		case row.expandable:
			head = append(head, keyHint{"→", "expand"})
		default:
			head = append(head, keyHint{"←", "parent"})
		}
		if s, ok := row.host.dsSnaps[row.ds.Name]; !ok || len(s) > 0 {
			label := "snaps"
			if m.snapsShown[row.id] {
				label = "hide snaps"
			}
			head = append(head, keyHint{"t", label})
		}
		if dsMarkable(row) {
			head = append(head, keyHint{"space", "select"})
		}
	case rFam:
		if row.expanded {
			head = append(head, keyHint{"←", "fold"})
		} else {
			head = append(head, keyHint{"→", "unfold"})
		}
		head = append(head, keyHint{"space", "select"}, keyHint{"t", "hide snaps"})
	case rSnap:
		head = append(head, keyHint{"space", "select"}, keyHint{"t", "hide snaps"})
	}
	if m.ackPending() > 0 {
		head = append(head, keyHint{"a", "acks"})
	}
	return append(head, keyHint{"/", "filter"}, keyHint{"s", "sort"},
		keyHint{"q", "quit"})
}

func cheatLine(m *Model, w int) string {
	hints := m.cheatHints()
	render := func(hs []keyHint) string {
		parts := make([]string, len(hs))
		for i, h := range hs {
			if h.key == "" {
				parts[i] = styDim.Render(h.label)
				continue
			}
			parts[i] = styInv.Render(" "+h.key+" ") + " " + styDim.Render(h.label)
		}
		return " " + strings.Join(parts, "  ")
	}
	line := render(hints)
	for len(hints) > 1 && lipgloss.Width(line) > w {
		hints = hints[:len(hints)-1] // quit is last hired, first fired
		line = render(hints)
	}
	return line
}

// cheatBorder embeds the hints in the bottom border itself — hotkeys
// straddling the frame, the way the top border carries titles.
func cheatBorder(m *Model, inner int) string {
	line := cheatLine(m, inner-4)
	fill := inner - lipgloss.Width(line) - 2
	if fill < 0 {
		fill = 0
	}
	return "└" + styDim.Render("─") + line + " " + styDim.Render(rep("─", fill)) + "┘"
}
