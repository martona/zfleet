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
	switch m.mode {
	case modePerf:
		return []keyHint{{"tab/←→", "pool"}, {"t", "disks"}, {"esc", "back"}, {"q", "quit"}}

	case modeBrowser:
		if m.br.filterIn {
			return []keyHint{{"", "type to filter…"}, {"enter", "keep"}, {"esc", "cancel"}}
		}
		if len(m.br.selSnaps) > 0 {
			return []keyHint{{"space", "toggle"}, {"esc", "clear"}, {"bksp", "up"}, {"q", "quit"}}
		}
		var head []keyHint
		switch sel := m.brSelected(); sel.kind {
		case eSelf:
			head = []keyHint{{"enter", "up"}}
		case eChild:
			head = []keyHint{{"enter", "open"}}
		case eFam:
			label := "unfold"
			if c := m.brContainer(); c != nil && m.br.expFams[c.Name+"\x00"+sel.fam.Label()] {
				label = "fold"
			}
			head = []keyHint{{"enter", label}, {"space", "select"}}
		case eSnap:
			head = []keyHint{{"space", "select"}}
		}
		return append(head, keyHint{"bksp", "up"}, keyHint{"/", "filter"},
			keyHint{"s", "sort"}, keyHint{"p", "perf"}, keyHint{"q", "quit"})

	default: // tree screen
		row := m.treeSelected()
		var head []keyHint
		switch row.kind {
		case rOverview:
			head = []keyHint{{"↓", "browse"}}
		case rPool:
			if row.expanded {
				head = append(head, keyHint{"←", "collapse"})
			} else {
				head = append(head, keyHint{"→", "expand"})
			}
			head = append(head, keyHint{"enter", "drill"}, keyHint{"t", "disks"})
		case rDataset:
			switch {
			case row.expanded:
				head = append(head, keyHint{"←", "collapse"})
			case row.expandable:
				head = append(head, keyHint{"→", "expand"})
			default:
				head = append(head, keyHint{"←", "parent"})
			}
			head = append(head, keyHint{"enter", "drill"})
		}
		return append(head, keyHint{"s", "sort"}, keyHint{"p", "perf"}, keyHint{"q", "quit"})
	}
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
