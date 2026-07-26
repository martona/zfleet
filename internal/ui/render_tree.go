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
		case rFam:
			n = 2*ind + 2*r.depth + 2 + lipgloss.Width(famLabel(r.fam))
		case rSnap:
			n = 2*ind + 2*r.depth + 2 + lipgloss.Width("@"+r.snap.Snap)
			if r.member {
				n++
			}
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
			// an unreachable host is an ERROR, not a listing: red name,
			// red outage text, degraded to a form that always fits
			txt := hostOutage(h)
			if lipgloss.Width(txt) > treeColsW-1 {
				txt = hostOutageCompact(h)
			}
			rest := padR(txt, treeColsW-1)
			if onCur {
				return styInv.Render(name + " " + rest)
			}
			return styBad.Render(name) + " " + styBad.Render(rest)
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
		// the worst pool's tier colors the host — trouble is visible from
		// the very top of the chain
		nameSty := styBold
		switch h.sev() {
		case sevWarn:
			nameSty = styWarn.Bold(true)
		case sevErr:
			nameSty = styBad
		}
		return nameSty.Render(name) + " " + styDim.Render(cells)

	case rPool:
		prefix := rep("  ", ind) + expandMark(r) + " "
		name := truncate(r.pool.Name, nameW-lipgloss.Width(prefix))
		pad := rep(" ", nameW-lipgloss.Width(prefix+name))
		sick := zfs.StateRank(r.pool.State) != 0
		// zfse's own bubbling: zfs never sums child counters upward and
		// knows nothing of SMART, so an ONLINE pool over a counter-riddled
		// or health-warning disk warns here regardless. The row says only
		// WARN — a summary verdict; the inspector holds the root cause.
		badge := ""
		if !sick && r.host.poolSevFull(r.pool) > sevOK {
			badge = "WARN"
		}
		capS := padL(pctStr(r.pool.CapPct), capCellW)
		size := padL(zfs.NiceBytes(r.pool.Size), usedCellW)
		if onCur {
			switch {
			case sick:
				return styInv.Render(prefix + name + pad + " " +
					padR(r.pool.State, stateCellW) + capS + " " + size)
			case badge != "":
				return styInv.Render(prefix + name + pad + " " +
					padR(badge, stateCellW) + capS + " " + size)
			}
			return styInv.Render(prefix+name+pad+" ") + bar(r.pool.CapPct, stateCellW-1) +
				styInv.Render(" "+capS+" "+size)
		}
		nameSty := sevStyle(r.host.poolSevFull(r.pool))
		if !rowLive(r.host) {
			nameSty = styDim
		}
		state := bar(r.pool.CapPct, stateCellW-1) + " "
		switch {
		case sick:
			state = healthStyle(r.pool.State).Render(padR(r.pool.State, stateCellW))
		case badge != "":
			state = styWarn.Render(padR(badge, stateCellW))
		}
		return prefix + nameSty.Render(name) + pad + " " + state + dimUnit(capS) + " " + dimUnit(size)

	case rDataset:
		markCh := " "
		if r.sel {
			markCh = "*"
		}
		prefix := rep("  ", ind+r.depth) + expandMark(r) + markCh
		name := truncate(r.ds.Base(), nameW-lipgloss.Width(prefix))
		used := padL(zfs.NiceBytes(r.ds.Used), usedCellW)
		lead := padR(prefix+name, nameW) + " " + rep(" ", stateCellW+capCellW) + " "
		if onCur {
			return styInv.Render(lead + used)
		}
		if r.sel {
			return styWarn.Render(lead + used)
		}
		if m.filter != "" && !r.hit {
			// structural ancestor of a match — the path, not the prize
			return styDim.Render(lead + used)
		}
		return lead + dimUnit(used)

	case rFam:
		markCh := " "
		if r.sel {
			markCh = "*"
		}
		prefix := rep("  ", ind+r.depth) + expandMark(r) + markCh
		name := truncate(famLabel(r.fam), nameW-lipgloss.Width(prefix))
		used := padL(zfs.NiceBytes(r.fam.Used()), usedCellW)
		lead := padR(prefix+name, nameW) + " " + rep(" ", stateCellW+capCellW) + " "
		switch {
		case onCur:
			return styInv.Render(lead + used)
		case markCh == "*":
			return styWarn.Render(lead + used)
		}
		return lead + dimUnit(used)

	case rSnap:
		markCh := " "
		if r.sel {
			markCh = "*"
		}
		prefix := rep("  ", ind+r.depth) + markCh + " "
		if r.member {
			prefix += " " // scattered out of an unfolded family
		}
		name := truncate("@"+r.snap.Snap, nameW-lipgloss.Width(prefix))
		used := padL(zfs.NiceBytes(r.snap.Used), usedCellW)
		lead := padR(prefix+name, nameW) + " " + rep(" ", stateCellW+capCellW) + " "
		switch {
		case onCur:
			return styInv.Render(lead + used)
		case markCh == "*":
			return styWarn.Render(lead + used)
		case r.hit:
			// a filter match is the prize — full brightness among dim paths
			return lead + dimUnit(used)
		}
		return styDim.Render(lead + used)

	case rPending:
		label := "collecting datasets…"
		if r.ds != nil {
			label = "collecting snapshots…"
		}
		body := padR(rep("  ", ind+r.depth)+"  "+label, nameW+treeColsW)
		if onCur {
			return styInv.Render(body)
		}
		return styDim.Render(body)
	}
	return ""
}

// famLabel is a family row's display name: the shared prefix and the count.
func famLabel(f *zfs.SnapFamily) string {
	return fmt.Sprintf("@%s (%d)", f.Label(), len(f.Snaps))
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
	if sparkW > dsIOHistLen/2 { // braille: two samples per cell
		sparkW = dsIOHistLen / 2
	}
	if sparkW < 3 {
		sparkW = 0
	}
	cellW := rateW
	if sparkW > 0 {
		cellW += 1 + sparkW
	}

	ioCell := func(rate int64, hist []int64, ok bool, fam sparkFam) string {
		if !ok {
			// stale rates are lies and blank to "-", but a frozen sparkline
			// is history — it stays, dimmed, right where it stopped
			cell := styDim.Render(padL("-", rateW))
			if sparkW > 0 && len(hist) > 0 {
				return cell + " " + sparklineFam(sparkDead, hist, sparkW)
			}
			return cell + rep(" ", cellW-rateW)
		}
		// an idle rate recedes wholesale; a moving one keeps bright digits
		// over a dim unit
		cell := padL(zfs.NiceBytes(rate)+"/s", rateW)
		if rate == 0 {
			cell = styDim.Render(cell)
		} else {
			cell = dimUnit(cell)
		}
		if sparkW > 0 {
			cell += " " + sparklineFam(fam, hist, sparkW)
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
				row += "  " + ioCell(rbw, rh, live, sparkSteel) + "  " + ioCell(wbw, wh, live, sparkGold)
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
			row += "  " + ioCell(io.RBw, rh, ok, sparkSteel) + "  " + ioCell(io.WBw, wh, ok, sparkGold)
		case rDataset:
			sub, rh, wh, loaded := r.host.subtreeIO(r.ds)
			live := loaded > 0 && rowLive(r.host)
			row += "  " + ioCell(sub.RBw, rh, live, sparkSteel) + "  " + ioCell(sub.WBw, wh, live, sparkGold)
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
