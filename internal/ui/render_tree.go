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

// treeIndent is the extra indent applied under host rows: pools step in one
// level when the host level exists at all.
func (m *Model) treeIndent() int {
	if m.multiHost {
		return 1
	}
	return 0
}

// rowLive reports whether a row's numbers can be trusted as current — old
// topology is information, old rates are lies.
func rowLive(h *hostState) bool { return h != nil && h.conn == connLive }

// treeNarrowPane renders the tree for the two-pane presentation.
func treeNarrowPane(m *Model, w, h int) []string {
	rows := m.treeRows()
	cur := m.treeIdx(rows)
	ind := m.treeIndent()
	var lines []string
	for i, r := range rows {
		onCur := i == cur
		marker := "  "
		if onCur {
			marker = "▸ "
		}
		var row string
		switch r.kind {
		case rOverview:
			row = marker + "≡ overview"
			if onCur {
				row = styInv.Render(padR(row, w))
			}
		case rHost:
			vit := hostVitalsRow(r.host, false)
			vitSty := styDim
			if r.host.conn == connDown {
				vit, vitSty = hostOutage(r.host), styWarn
			}
			nameW := w - lipgloss.Width(vit) - 4
			if nameW < 6 {
				nameW = 6
			}
			left := marker + padR(truncate(r.host.name, nameW), nameW)
			if onCur {
				row = styInv.Render(left + " " + padR(vit, w-lipgloss.Width(left)-1))
			} else {
				row = marker + styBold.Render(padR(truncate(r.host.name, nameW), nameW)) +
					" " + vitSty.Render(vit)
			}
		case rPool:
			pre := rep("  ", ind)
			nameW := w - 27 - len(pre)
			if nameW < 6 {
				nameW = 6
			}
			left := marker + pre + expandMark(r) + " " + padR(truncate(r.pool.Name, nameW), nameW) + " "
			right := " " + padL(pctStr(r.pool.CapPct), 4) + " " + padL(zfs.NiceBytes(r.pool.Size), 6)
			if onCur {
				row = styInv.Render(left) + bar(r.pool.CapPct, 8) +
					styInv.Render(padR(right, w-lipgloss.Width(left)-8))
			} else {
				name := healthStyle(r.pool.State).Render(padR(truncate(r.pool.Name, nameW), nameW))
				if !rowLive(r.host) {
					name = styDim.Render(padR(truncate(r.pool.Name, nameW), nameW))
				}
				row = marker + pre + expandMark(r) + " " + name + " " + bar(r.pool.CapPct, 8) + right
			}
		case rDataset:
			indent := rep("  ", r.depth+ind)
			nameW := w - 13 - len(indent)
			if nameW < 6 {
				nameW = 6
			}
			row = marker + indent + expandMark(r) + " " +
				padR(truncate(r.ds.Base(), nameW), nameW) +
				" " + padL(zfs.NiceBytes(r.ds.Used), 7)
			if onCur {
				row = styInv.Render(padR(row, w))
			}
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
	ind := m.treeIndent()

	nameW := 12
	for _, r := range rows {
		n := 0
		switch r.kind {
		case rHost:
			n = 4 + lipgloss.Width(r.host.name) // name + a shoulder before vitals
		case rPool:
			n = 4 + 2*ind + lipgloss.Width(r.pool.Name)
		case rDataset:
			n = 4 + 2*ind + r.depth*2 + lipgloss.Width(r.ds.Base())
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
			// stale rates are lies and blank to "-", but a frozen sparkline
			// is history — it stays, dimmed, right where it stopped
			cell := padL("-", rateW)
			if sparkW > 0 && len(hist) > 0 {
				return cell + " " + sparkline(hist, sparkW)
			}
			return cell + rep(" ", cellW-rateW)
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

	// the vitals cell spans the STATE/CAP/USED region a host row has no
	// use for
	const vitalsW = 9 + 4 + 1 + 7

	var lines []string
	for i, r := range rows {
		var row string
		switch r.kind {
		case rOverview:
			row = "  ≡ overview"
		case rHost:
			name := "  " + truncate(r.host.name, nameW-2)
			if r.host.conn == connDown {
				row = padR(name, nameW) + styWarn.Render(hostOutage(r.host))
				break
			}
			rh, wh := hostIORings(r.host)
			rbw, wbw := hostLiveIO(r.host)
			live := rowLive(r.host) && len(r.host.hostIOHist) > 0
			row = padR(name, nameW) +
				styDim.Render(padR(hostVitalsRow(r.host, true), vitalsW)) + "  " +
				ioCell(rbw, rh, live) + "  " + ioCell(wbw, wh, live)
		case rPool:
			name := "  " + rep("  ", ind) + expandMark(r) + " " + truncate(r.pool.Name, nameW-4-2*ind)
			io, ok := r.host.io[r.pool.Name]
			ok = ok && rowLive(r.host)
			hist := r.host.ioHist[r.pool.Name]
			rh := make([]int64, len(hist))
			wh := make([]int64, len(hist))
			for j, s := range hist {
				rh[j] = s.RBw
				wh[j] = s.WBw
			}
			stateCell := healthStyle(r.pool.State).Render(padR(r.pool.State, 9))
			if !rowLive(r.host) {
				stateCell = styDim.Render(padR(r.pool.State, 9))
			}
			row = padR(name, nameW) + stateCell +
				padL(pctStr(r.pool.CapPct), 4) + " " +
				padL(zfs.NiceBytes(r.pool.Size), 7) + "  " +
				ioCell(io.RBw, rh, ok) + "  " + ioCell(io.WBw, wh, ok)
		case rDataset:
			name := "  " + rep("  ", ind+r.depth) + expandMark(r) + " " + truncate(r.ds.Base(), nameW-4-2*ind-r.depth*2)
			sub, rh, wh, loaded := r.host.subtreeIO(r.ds)
			row = padR(name, nameW) +
				padR("", 9) + padR("", 5) +
				padL(zfs.NiceBytes(r.ds.Used), 7) + "  " +
				ioCell(sub.RBw, rh, loaded > 0 && rowLive(r.host)) + "  " +
				ioCell(sub.WBw, wh, loaded > 0 && rowLive(r.host))
		}
		if i == cur {
			// the wide view's cursor only ever rests on the overview row
			row = styInv.Render(padR(row, w))
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
