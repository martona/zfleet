package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The frame is hand-composed rather than widget-assembled: one box, an
// internal divider, titles embedded in the border, and a full-width vitals
// strip — the NC-style chrome the design settled on.
//
//	┌ pools ─────────┬ rust ───────────────┐
//	│ <pool rows>    │ <inspector>         │
//	├────────────────┴─────────────────────┤
//	│ <vitals strip>                       │
//	└──────────────────────────────────────┘

func rep(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(s, n)
}

// fit pads or truncates a possibly-styled line to exactly w cells.
func fit(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d >= 0 {
		return s + rep(" ", d)
	}
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

func frame(m *Model) string {
	if m.w < 70 || m.h < 16 {
		return fmt.Sprintf("zfse needs at least 70x16 (have %dx%d)", m.w, m.h)
	}
	if !m.multiHost {
		// single host: nothing to show until its pools land. A fleet
		// renders immediately — host rows carry their own state.
		h := m.hosts[0]
		if len(h.pools) == 0 {
			if h.lastErr != nil {
				return "zfse: " + h.lastErr.Error()
			}
			if h.errText != "" {
				return "zfse: " + h.errText
			}
			return "collecting…"
		}
	}

	contentH := m.h - 4 // top border, divider, strip, keyed bottom border

	title := func(t string, w int) string {
		seg := " " + t + " "
		if lipgloss.Width(seg) > w-2 {
			seg = " " + truncate(t, w-4) + " "
		}
		return "─" + seg + rep("─", w-1-lipgloss.Width(seg))
	}

	// full-width presentation: the tree with the cursor on the overview row
	if m.mode == modePools && m.treeSelected().kind == rOverview {
		inner := m.w - 2
		heading := "overview"
		if ft := m.filterTitle(); ft != "" {
			heading = ft
		}
		if len(m.marks) > 0 {
			heading += fmt.Sprintf(" · %d marked", len(m.marks))
		}
		lines := overviewPane(m, inner, contentH)
		m.rightOverflow = false
		var b strings.Builder
		b.WriteString("┌" + title(heading, inner) + "┐\n")
		for i := 0; i < contentH; i++ {
			l := ""
			if i < len(lines) {
				l = lines[i]
			}
			b.WriteString("│" + fit(l, inner) + "│\n")
		}
		b.WriteString("├" + rep("─", inner) + "┤\n")
		b.WriteString("│" + fit(strip(m), inner) + "│\n")
		b.WriteString(cheatBorder(m, inner))
		if m.ackPop {
			return ackOverlay(m, b.String())
		}
		return b.String()
	}

	// the tree's divider sits exactly where the overview's io columns
	// begin: same rows, same geometry, no shift on mode change. It
	// moves only when expansion changes what is visible.
	leftW := m.treeLeftWidth()
	if leftW > m.w/2 {
		leftW = m.w / 2
	}
	rightW := m.w - leftW - 3

	row := m.treeSelected()
	leftTitle := "pools"
	if m.multiHost {
		leftTitle = "hosts"
	}
	if ft := m.filterTitle(); ft != "" {
		leftTitle = ft
	}
	if len(m.marks) > 0 {
		leftTitle += fmt.Sprintf(" · %d marked", len(m.marks))
	}
	// panel identity first: a context change resets the scroll BEFORE the
	// panel builds, so windowed builders see the offset they render at
	panelKey := fmt.Sprintf("t|%s|%d", m.treeSel, m.markGen)
	if panelKey != m.panelKey {
		m.panelKey, m.panelScroll = panelKey, 0
	}
	var rightTitle string
	var right []string
	left := treeNarrowPane(m, leftW, contentH)
	// settle-hold: while the cursor is in flight the panel stays blank —
	// no point paying for an inspector that the very next buffered key
	// replaces. It populates the instant the cursor has been still.
	if time.Since(m.cursorMovedAt) >= settleDelay {
		switch row.kind {
		case rHost:
			rightTitle = row.host.name
			right = hostInspector(m, row.host, rightW)
		case rPool:
			rightTitle = row.pool.Name
			right = inspector(m, row.host, row.pool, rightW)
		case rDataset:
			rightTitle = row.ds.Base()
			right = dsInspector(m, row.host, row.ds, rightW)
		case rFam:
			rightTitle = "@" + row.fam.Label()
			right = famInspector(m, row.host, row.ds.Name, row.fam, rightW)
		case rSnap:
			rightTitle = "@" + row.snap.Snap
			right = snapInspector(m, row.snap)
		case rPending:
			// the wait row answers with its parent's inspector — the data
			// you already have, while the rest arrives
			if row.pool != nil {
				rightTitle = row.pool.Name
				right = inspector(m, row.host, row.pool, rightW)
			} else if row.ds != nil {
				rightTitle = row.ds.Base()
				right = dsInspector(m, row.host, row.ds, rightW)
			}
		}
		// while the cursor stays in the selection's world, the panel
		// answers for the collection as a whole
		if m.inMarkContext(row) {
			rightTitle = fmt.Sprintf("selection (%d)", len(m.marks))
			right = selInspector(m, rightW, m.panelScroll, contentH)
		}
	}
	right, m.panelScroll, m.rightOverflow = scrollWindow(right, m.panelScroll, contentH)

	var b strings.Builder
	b.WriteString("┌" + title(leftTitle, leftW) + "┬" + title(rightTitle, rightW) + "┐\n")
	for i := 0; i < contentH; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		b.WriteString("│" + fit(l, leftW) + "│" + fit(r, rightW) + "│\n")
	}
	b.WriteString("├" + rep("─", leftW) + "┴" + rep("─", rightW) + "┤\n")
	b.WriteString("│" + fit(strip(m), m.w-2) + "│\n")
	b.WriteString(cheatBorder(m, m.w-2))
	if m.ackPop {
		return ackOverlay(m, b.String())
	}
	return b.String()
}

func inspector(m *Model, h *hostState, p *zfs.Pool, w int) []string {
	var head []string

	errTxt := p.ErrorsLine
	if errTxt == "No known data errors" {
		errTxt = "no known data errors"
	}
	headLine := " " + healthStyle(p.State).Render(p.State) + styDim.Render(" · ") + errTxt
	if h.poolSevFull(p) > poolSev(p) {
		// zfs is content but the drives beneath are not — point at the rows
		headLine += styWarn.Render(" · drive warnings below")
	}
	head = append(head, headLine)

	scanSty := styDim
	if p.Scan.Errors > 0 {
		scanSty = styBad
	}
	switch p.Scan.State {
	case zfs.ScanNone:
		head = append(head, " "+styDim.Render("scrub: never"))
	case zfs.ScanInProgress:
		head = append(head, " "+styBold.Render(p.Scan.Kind+": ")+p.Scan.Summary)
	default:
		head = append(head, " "+scanSty.Render(p.Scan.Kind+": "+p.Scan.Summary))
	}

	var noteLines []string
	for _, n := range p.Notes {
		noteLines = append(noteLines, wrap(n, w-3)...)
	}
	// the unified panel is busy — the zpool prose yields after two lines
	if len(noteLines) > 2 {
		hidden := len(noteLines) - 2
		noteLines = append(noteLines[:2], fmt.Sprintf("… (+%d lines — zpool status has the rest)", hidden))
	}
	for _, n := range noteLines {
		head = append(head, " "+styWarn.Render(n))
	}

	head = append(head, "")
	lines := head

	// the live engine: banner, io charts, dirty chart, arc, txg, zil
	lines = append(lines, poolPerfLines(m, h, p, w)...)
	lines = append(lines, "")

	// the unified vdev table: verdicts, temps, windowed latency, odometers
	lines = append(lines, poolTable(m, h, p, w)...)
	lines = append(lines, "")

	// capacity basement: class bars, allocation overhead, cloning footprint
	for _, c := range p.Classes {
		label := c.Name
		if label == "logs" {
			label = "log"
		}
		pct := int64(-1)
		if c.Size > 0 && c.Alloc >= 0 {
			pct = (c.Alloc*100 + c.Size/2) / c.Size
		}
		lines = append(lines, " "+padR(label, 8)+bar(pct, 10)+" "+padL(pctStr(pct), 4)+
			"  "+padL(zfs.NiceBytes(c.Alloc), 6)+styDim.Render(" / ")+zfs.NiceBytes(c.Size))
	}

	// raw-vs-charged allocation overhead against the geometry baseline —
	// meaningful on raidz, where the pool layer counts parity and padding
	// but datasets are charged deflated bytes
	if rs, ok := h.rootStats[p.Name]; ok && rs.Used > 0 && p.Alloc > 0 {
		if vw, par, ash, geo := h.poolGeometry(p.Name); geo {
			actual := float64(p.Alloc) / float64(rs.Used)
			base := zfs.RaidzRawPerCharged(vw, par, ash)
			actualCell := fmt.Sprintf("×%.2f", actual)
			if actual > base*1.05 {
				actualCell = styWarn.Render(actualCell + " (padding excess)")
			}
			lines = append(lines, " raw "+actualCell+" per charged byte"+
				styDim.Render(fmt.Sprintf(" · baseline ×%.2f (raidz%d %dw)", base, par, vw)))
		}
	}

	// block-cloning footprint — only pools that actually hold cloned
	// blocks (reflink-era cp, clone-aware receives) earn the line
	if bc := h.bclone[p.Name]; bc.Used > 0 {
		ratio := float64(bc.Used+bc.Saved) / float64(bc.Used)
		lines = append(lines, " cloned "+zfs.NiceBytes(bc.Used)+
			styDim.Render(" · saving ")+zfs.NiceBytes(bc.Saved)+
			styDim.Render(fmt.Sprintf(" (×%.2f)", ratio)))
	}

	return lines
}

// scrollWindow views h rows of a taller panel at the given offset, marking
// hidden rows above and below. Returns the clamped offset and whether the
// panel overflows at all.
func scrollWindow(lines []string, off, h int) ([]string, int, bool) {
	if len(lines) <= h || h < 2 {
		return lines, 0, false
	}
	max := len(lines) - h
	if off > max {
		off = max
	}
	if off < 0 {
		off = 0
	}
	out := make([]string, h)
	copy(out, lines[off:off+h])
	if off > 0 {
		out[0] = " " + styDim.Render(fmt.Sprintf("… (%d above)", off))
	}
	if off < max {
		out[h-1] = " " + styDim.Render(fmt.Sprintf("… (%d below)", max-off))
	}
	return out, off, true
}

// opsCell renders an ops/s figure, refusing to claim "0 ops/s" when bytes
// moved: zpool iostat floors sub-1.0 ops/s rates to zero (a lone aggregated
// write in a sample window is the classic case), so bandwidth-with-zero-ops
// really means "less than one op per second".
func opsCell(bw, ops int64) string {
	if ops == 0 && bw > 0 {
		return "<1 ops/s"
	}
	return zfs.NiceCount(ops) + " ops/s"
}

// filterTitle renders the active filter's pane title: pattern, input
// cursor, live match count, sweep progress. Empty when no filter is up.
func (m *Model) filterTitle() string {
	if !m.filterIn && m.filter == "" {
		return ""
	}
	t := "/" + m.filter
	if m.filterIn {
		t += "▏"
	}
	if m.filter != "" {
		t += fmt.Sprintf(" · %d", m.filterHits(m.treeRows()))
	}
	if k := m.sweepPending(); k > 0 {
		t += fmt.Sprintf(" · sweeping %d", k)
	}
	return t
}

// stripHost resolves which host the vitals strip should reflect: the one
// owning whatever the cursor is on.
func (m *Model) stripHost() *hostState {
	if row := m.treeSelected(); row.host != nil {
		return row.host
	}
	return m.hosts[0]
}

func strip(m *Model) string {
	// on the overview the strip answers for the fleet; everywhere else it
	// follows the selection's host
	if m.multiHost && m.mode == modePools && m.treeSelected().kind == rOverview {
		return fleetStrip(m)
	}
	h := m.stripHost()
	var segs []string

	arcSeg := "arc -"
	if h.haveArc {
		arcSeg = "arc " + zfs.NiceBytes(h.arc.Size) + "/" + zfs.NiceBytes(h.arc.CMax)
		dh := h.arc.Hits - h.arcPrev.Hits
		dm := h.arc.Misses - h.arcPrev.Misses
		if dh+dm <= 0 {
			dh, dm = h.arc.Hits, h.arc.Misses // no traffic in window: lifetime
		}
		if dh+dm > 0 {
			arcSeg += fmt.Sprintf(" · hit %.1f%%", 100*float64(dh)/float64(dh+dm))
		}
	}
	if m.multiHost {
		segs = append(segs, " "+styBold.Render(h.name)+" · "+arcSeg)
	} else {
		segs = append(segs, " "+arcSeg)
	}

	var rbw, wbw int64
	for _, r := range h.io {
		rbw += r.RBw
		wbw += r.WBw
	}
	segs = append(segs, "Σ r "+elastic(h.stripW, "rbw", zfs.NiceBytes(rbw)+"/s")+
		" w "+elastic(h.stripW, "wbw", zfs.NiceBytes(wbw)+"/s"))

	scans := []string{}
	for _, p := range h.pools {
		if p.Scan.State == zfs.ScanInProgress {
			scans = append(scans, fmt.Sprintf("%s %s %.0f%%", p.Scan.Kind, p.Name, p.Scan.Percent))
		}
	}
	if len(scans) > 0 {
		segs = append(segs, styBold.Render(strings.Join(scans, ", ")))
	} else {
		segs = append(segs, styDim.Render("no scans running"))
	}

	if len(h.pools) > 0 {
		worst := h.pools[0]
		for _, p := range h.pools {
			if zfs.StateRank(p.State) > zfs.StateRank(worst.State) {
				worst = p
			}
		}
		if zfs.StateRank(worst.State) == 0 {
			segs = append(segs, styGood.Render(fmt.Sprintf("%d pools ONLINE", len(h.pools))))
		} else {
			segs = append(segs, healthStyle(worst.State).Render(worst.Name+" "+worst.State))
		}
	}
	for _, p := range h.pools {
		if er, ew, ec := p.ErrSums(); er+ew+ec > 0 {
			segs = append(segs, styWarn.Render("! "+p.Name+" "+errBadge(er, ew, ec, 20)))
			break
		}
	}

	if h.conn == connDown {
		segs = append(segs, styBad.Render("! "+hostOutage(h)))
	}
	if strings.HasPrefix(h.src.Name(), "replay") {
		segs = append(segs, styDim.Render("[replay]"))
	}
	if h.lastErr != nil {
		segs = append(segs, styBad.Render("! "+truncate(h.lastErr.Error(), 30)))
	}

	return strings.Join(segs, styDim.Render(" │ "))
}

// fleetStrip is the overview's strip: every host's io summed, every scan
// anywhere, every outage — the whole estate at a glance.
func fleetStrip(m *Model) string {
	var segs []string

	var rbw, wbw int64
	pools, online := 0, 0
	var worstPool *zfs.Pool
	worstHost := ""
	errSeg := ""
	scans := []string{}
	downs := []string{}
	for _, h := range m.hosts {
		if h.conn == connLive {
			for _, r := range h.io {
				rbw += r.RBw
				wbw += r.WBw
			}
		}
		if h.conn == connDown {
			downs = append(downs, h.name)
		}
		for _, p := range h.pools {
			pools++
			if zfs.StateRank(p.State) == 0 {
				online++
			}
			if worstPool == nil || zfs.StateRank(p.State) > zfs.StateRank(worstPool.State) {
				worstPool, worstHost = p, h.name
			}
			if er, ew, ec := p.ErrSums(); errSeg == "" && er+ew+ec > 0 {
				errSeg = "! " + h.name + ":" + p.Name + " " + errBadge(er, ew, ec, 20)
			}
			if p.Scan.State == zfs.ScanInProgress {
				scans = append(scans, fmt.Sprintf("%s %s:%s %.0f%%", p.Scan.Kind, h.name, p.Name, p.Scan.Percent))
			}
		}
	}

	segs = append(segs, " "+styBold.Render("fleet")+" · Σ r "+
		elastic(m.fleetW, "rbw", zfs.NiceBytes(rbw)+"/s")+
		" w "+elastic(m.fleetW, "wbw", zfs.NiceBytes(wbw)+"/s"))

	if len(scans) > 0 {
		segs = append(segs, styBold.Render(strings.Join(scans, ", ")))
	} else {
		segs = append(segs, styDim.Render("no scans running"))
	}

	if pools > 0 {
		if online == pools {
			segs = append(segs, styGood.Render(fmt.Sprintf("%d pools ONLINE", pools)))
		} else {
			segs = append(segs, healthStyle(worstPool.State).Render(worstHost+":"+worstPool.Name+" "+worstPool.State))
		}
	}
	if errSeg != "" {
		segs = append(segs, styWarn.Render(errSeg))
	}

	for _, name := range downs {
		segs = append(segs, styBad.Render("! "+name+" unreachable"))
	}
	if strings.HasPrefix(m.hosts[0].src.Name(), "replay") {
		segs = append(segs, styDim.Render("[replay]"))
	}
	return strings.Join(segs, styDim.Render(" │ "))
}

func pctStr(p int64) string {
	if p < 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", p)
}

func clampLines(lines []string, h int) []string {
	if len(lines) <= h {
		return lines
	}
	out := lines[:h]
	out[h-1] = " " + styDim.Render("…")
	return out
}
