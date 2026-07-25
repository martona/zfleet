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

// The tree's two presentations render ONE table. The overview is that
// table plus the io columns; inspection replaces the io columns with the
// inspector pane. Both share the row renderer, the header, and the name
// geometry, so switching modes never shifts anything that stays visible.
//
// Shared columns right of the name:
//
//	state cell (9)  — pool: cap bar, or the state text when unhealthy;
//	                  host: uptime
//	cap cell   (5)  — pool: used%; host: temperature
//	used cell  (7)  — pool: total size; dataset: used; host: cpu%
const (
	stateCellW = 9
	capCellW   = 5
	usedCellW  = 7
	// name column → end of used cell, excluding the 2-char cursor margin
	treeColsW = 1 + stateCellW + capCellW + 1 + usedCellW
)

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

// treeNameW measures the widest visible name, uncapped; panes cap it to
// their own space.
func (m *Model) treeNameW(rows []treeRow) int {
	ind := m.treeIndent()
	nameW := 12
	for _, r := range rows {
		n := 0
		switch r.kind {
		case rHost:
			n = lipgloss.Width(r.host.name)
		case rPool:
			n = 2*ind + 2 + lipgloss.Width(r.pool.Name)
		case rDataset:
			n = 2*ind + 2*r.depth + 2 + lipgloss.Width(r.ds.Base())
		}
		if n > nameW {
			nameW = n
		}
	}
	return nameW
}

// treeLeftWidth is the two-pane divider position: cursor margin + name +
// shared cells + a breathing column. Structural — it moves only when
// expansion changes what is visible, never under the cursor.
func (m *Model) treeLeftWidth() int {
	return 2 + m.treeNameW(m.treeRows()) + treeColsW + 1
}

// treeHeader renders the shared header over the left columns.
func treeHeader(nameW int) string {
	return rep(" ", 2+nameW+1+stateCellW) +
		styDim.Render(padL("CAP", capCellW)+" "+padL("USED", usedCellW))
}

// treeRowLeft renders one row's shared left columns (everything except the
// 2-char cursor margin). onCur applies the inverse band — text colors
// yield to it mc-style, capacity bars do not.
func treeRowLeft(m *Model, r treeRow, nameW int, onCur bool) string {
	ind := m.treeIndent()
	switch r.kind {
	case rOverview:
		body := padR("≡ overview", nameW+treeColsW)
		if onCur {
			return styInv.Render(body)
		}
		return body

	case rHost:
		h := r.host
		name := padR(truncate(h.name, nameW), nameW)
		if h.conn == connDown {
			// a dark host is an alarm, not a listing: the name goes warn
			// too, and the outage text degrades to a form that always fits
			txt := hostOutage(h)
			if lipgloss.Width(txt) > treeColsW-1 {
				txt = hostOutageCompact(h)
			}
			rest := padR(txt, treeColsW-1)
			if onCur {
				return styInv.Render(name + " " + rest)
			}
			return styWarn.Bold(true).Render(name) + " " + styWarn.Render(rest)
		}
		temp := ""
		if h.haveTemp {
			temp = fmt.Sprintf("%d°C", h.tempC)
		}
		cpu := ""
		if h.cpuPct >= 0 {
			cpu = fmt.Sprintf("cpu %d%%", h.cpuPct)
		}
		cells := padR(uptimeCoarse(h.uptimeSec), stateCellW) +
			padL(temp, capCellW) + " " + padL(cpu, usedCellW)
		if onCur {
			return styInv.Render(name + " " + cells)
		}
		return styBold.Render(name) + " " + styDim.Render(cells)

	case rPool:
		prefix := rep("  ", ind) + expandMark(r) + " "
		name := truncate(r.pool.Name, nameW-lipgloss.Width(prefix))
		pad := rep(" ", nameW-lipgloss.Width(prefix+name))
		sick := zfs.StateRank(r.pool.State) != 0
		capS := padL(pctStr(r.pool.CapPct), capCellW)
		size := padL(zfs.NiceBytes(r.pool.Size), usedCellW)
		if onCur {
			if sick {
				return styInv.Render(prefix + name + pad + " " +
					padR(r.pool.State, stateCellW) + capS + " " + size)
			}
			return styInv.Render(prefix+name+pad+" ") + bar(r.pool.CapPct, stateCellW-1) +
				styInv.Render(" "+capS+" "+size)
		}
		nameSty := healthStyle(r.pool.State)
		if !rowLive(r.host) {
			nameSty = styDim
		}
		state := bar(r.pool.CapPct, stateCellW-1) + " "
		if sick {
			state = healthStyle(r.pool.State).Render(padR(r.pool.State, stateCellW))
		}
		return prefix + nameSty.Render(name) + pad + " " + state + dimUnit(capS) + " " + dimUnit(size)

	case rDataset:
		prefix := rep("  ", ind+r.depth) + expandMark(r) + " "
		name := truncate(r.ds.Base(), nameW-lipgloss.Width(prefix))
		used := padL(zfs.NiceBytes(r.ds.Used), usedCellW)
		lead := padR(prefix+name, nameW) + " " + rep(" ", stateCellW+capCellW) + " "
		if onCur {
			return styInv.Render(lead + used)
		}
		return lead + dimUnit(used)
	}
	return ""
}

// treeNarrowPane renders the table for the two-pane presentation: the same
// rows the overview shows, io columns swapped for the inspector.
func treeNarrowPane(m *Model, w, h int) []string {
	rows := m.treeRows()
	cur := m.treeIdx(rows)
	nameW := w - 2 - treeColsW - 1
	if nameW < 8 {
		nameW = 8
	}
	var lines []string
	for i, r := range rows {
		if i == cur {
			lines = append(lines, styInv.Render("▸ ")+treeRowLeft(m, r, nameW, true))
		} else {
			lines = append(lines, "  "+treeRowLeft(m, r, nameW, false))
		}
	}
	return append([]string{treeHeader(nameW)}, scrollToCursor(lines, cur, h-1)...)
}

// overviewPane renders the same rows full-width with io columns appended.
// Sparklines absorb the slack and vanish first when the tree gets wide.
func overviewPane(m *Model, w, h int) []string {
	rows := m.treeRows()
	cur := m.treeIdx(rows)

	nameW := m.treeNameW(rows)
	const rateW = 10
	if max := w - 2 - treeColsW - 2*rateW - 4; nameW > max {
		nameW = max
	}
	sparkW := (w - 2 - nameW - treeColsW - 2*rateW - 6) / 2
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

	head := treeHeader(nameW) + styDim.Render("  "+padL("READ", rateW)) +
		rep(" ", cellW-rateW) + "  " + styDim.Render(padL("WRITE", rateW))

	var lines []string
	for i, r := range rows {
		row := "  " + treeRowLeft(m, r, nameW, false)
		switch r.kind {
		case rHost:
			if r.host.conn != connDown {
				rh, wh := hostIORings(r.host)
				rbw, wbw := hostLiveIO(r.host)
				live := rowLive(r.host) && len(r.host.hostIOHist) > 0
				row += "  " + ioCell(rbw, rh, live) + "  " + ioCell(wbw, wh, live)
			}
		case rPool:
			io, ok := r.host.io[r.pool.Name]
			ok = ok && rowLive(r.host)
			hist := r.host.ioHist[r.pool.Name]
			rh := make([]int64, len(hist))
			wh := make([]int64, len(hist))
			for j, s := range hist {
				rh[j] = s.RBw
				wh[j] = s.WBw
			}
			row += "  " + ioCell(io.RBw, rh, ok) + "  " + ioCell(io.WBw, wh, ok)
		case rDataset:
			sub, rh, wh, loaded := r.host.subtreeIO(r.ds)
			live := loaded > 0 && rowLive(r.host)
			row += "  " + ioCell(sub.RBw, rh, live) + "  " + ioCell(sub.WBw, wh, live)
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
