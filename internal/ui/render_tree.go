package ui

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/martona/zfs-explorer/internal/zfs"
)

func expandMark(r treeRow) string {
	switch {
	case !r.expandable:
		return " "
	case r.expanded:
		return "▾"
	default:
		return "▸"
	}
}

// treeNarrowPane renders the tree for the two-pane presentation.
func treeNarrowPane(m *Model, w, h int) []string {
	rows := m.treeRows()
	cur := m.treeIdx(rows)
	var lines []string
	for i, r := range rows {
		marker := "  "
		if i == cur {
			marker = "▸ "
		}
		var row string
		switch r.kind {
		case rOverview:
			row = marker + "≡ overview"
		case rPool:
			nameW := w - 27
			if nameW < 6 {
				nameW = 6
			}
			row = marker + expandMark(r) + " " +
				healthStyle(r.pool.State).Render(padR(truncate(r.pool.Name, nameW), nameW)) +
				" " + bar(r.pool.CapPct, 8) + " " + padL(pctStr(r.pool.CapPct), 4) +
				" " + padL(zfs.NiceBytes(r.pool.Size), 6)
		case rDataset:
			indent := rep("  ", r.depth)
			nameW := w - 13 - len(indent)
			if nameW < 6 {
				nameW = 6
			}
			row = marker + indent + expandMark(r) + " " +
				padR(truncate(r.ds.Base(), nameW), nameW) +
				" " + padL(zfs.NiceBytes(r.ds.Used), 7)
		}
		if i == cur {
			row = styBold.Render(row)
		}
		lines = append(lines, row)
	}
	return scrollToCursor(lines, cur, h)
}

// overviewPane renders the same rows full-width with io columns. Column
// widths adapt to the deepest visible expansion; sparklines absorb the
// slack and vanish first when the tree gets wide.
func overviewPane(m *Model, w, h int) []string {
	rows := m.treeRows()
	cur := m.treeIdx(rows)

	nameW := 12
	for _, r := range rows {
		n := 0
		switch r.kind {
		case rPool:
			n = 4 + lipgloss.Width(r.pool.Name)
		case rDataset:
			n = 4 + r.depth*2 + lipgloss.Width(r.ds.Base())
		}
		if n > nameW {
			nameW = n
		}
	}
	if nameW > w/2 {
		nameW = w / 2
	}
	// fixed cells: state 9, cap 5, used 9, two rate cells, gaps; the
	// sparklines absorb whatever width remains, up to the history window
	const rateW = 10
	sparkW := (w - nameW - 9 - 5 - 9 - 2*rateW - 6) / 2
	if sparkW > dsIOHistLen {
		sparkW = dsIOHistLen
	}
	if sparkW < 3 {
		sparkW = 0
	}
	cellW := rateW
	if sparkW > 0 {
		cellW += 1 + sparkW
	}

	ioCell := func(rate int64, hist []int64, ok bool) string {
		if !ok {
			return padL("-", rateW) + rep(" ", cellW-rateW)
		}
		cell := padL(zfs.NiceBytes(rate)+"/s", rateW)
		if sparkW > 0 {
			cell += " " + sparkline(hist, sparkW)
		}
		return cell
	}

	head := padR("", nameW) + styDim.Render(padR("STATE", 9)+padL("CAP", 4)+" "+
		padL("USED", 7)+"  "+padL("READ", rateW)) + rep(" ", cellW-rateW) +
		"  " + styDim.Render(padL("WRITE", rateW))

	var lines []string
	for i, r := range rows {
		var row string
		switch r.kind {
		case rOverview:
			row = "  ≡ overview"
		case rPool:
			name := "  " + expandMark(r) + " " + truncate(r.pool.Name, nameW-4)
			io, ok := m.io[r.pool.Name]
			hist := m.ioHist[r.pool.Name]
			rh := make([]int64, len(hist))
			wh := make([]int64, len(hist))
			for j, s := range hist {
				rh[j] = s.RBw
				wh[j] = s.WBw
			}
			row = padR(name, nameW) +
				healthStyle(r.pool.State).Render(padR(r.pool.State, 9)) +
				padL(pctStr(r.pool.CapPct), 4) + " " +
				padL(zfs.NiceBytes(r.pool.Size), 7) + "  " +
				ioCell(io.RBw, rh, ok) + "  " + ioCell(io.WBw, wh, ok)
		case rDataset:
			name := "  " + rep("  ", r.depth) + expandMark(r) + " " + truncate(r.ds.Base(), nameW-4-r.depth*2)
			sub, rh, wh, loaded := m.subtreeIO(r.ds)
			row = padR(name, nameW) +
				padR("", 9) + padR("", 5) +
				padL(zfs.NiceBytes(r.ds.Used), 7) + "  " +
				ioCell(sub.RBw, rh, loaded > 0) + "  " + ioCell(sub.WBw, wh, loaded > 0)
		}
		if i == cur {
			row = styBold.Render(row)
		}
		lines = append(lines, row)
	}
	return append([]string{head}, scrollToCursor(lines, cur, h-1)...)
}

// scrollToCursor keeps the cursor row visible on long lists.
func scrollToCursor(lines []string, cur, h int) []string {
	if len(lines) > h && cur >= h-1 {
		start := cur - h + 2
		out := make([]string, 0, h)
		out = append(out, " "+styDim.Render(fmt.Sprintf("… (%d above)", start)))
		out = append(out, lines[start+1:]...)
		return clampLines(out, h)
	}
	return clampLines(lines, h)
}
