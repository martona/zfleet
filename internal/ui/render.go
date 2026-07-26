package ui

import (
	"fmt"
	"strconv"
	"strings"

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

	// full-width presentations: the performance dashboard, or the tree
	// with the cursor on the overview row
	if m.mode == modePerf || (m.mode == modePools && m.treeSelected().kind == rOverview) {
		inner := m.w - 2
		var lines []string
		heading := "overview"
		if ft := m.filterTitle(); ft != "" && m.mode == modePools {
			heading = ft
		}
		if m.mode == modePerf {
			heading = "performance · " + m.perf.pool
			if m.multiHost && m.perf.host != nil {
				heading = "performance · " + m.perf.host.name + ":" + m.perf.pool
			}
			lines = perfPane(m, inner)
			key := "p|" + m.perf.pool
			if m.perf.host != nil {
				key = "p|" + m.perf.host.name + "\x00" + m.perf.pool
			}
			if key != m.panelKey {
				m.panelKey, m.panelScroll = key, 0
			}
			lines, m.panelScroll, m.rightOverflow = scrollWindow(lines, m.panelScroll, contentH)
		} else {
			lines = overviewPane(m, inner, contentH)
			m.rightOverflow = false
		}
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
	var rightTitle string
	var right []string
	left := treeNarrowPane(m, leftW, contentH)
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
		// the wait row answers with its parent's inspector — the data you
		// already have, while the rest arrives
		if row.pool != nil {
			rightTitle = row.pool.Name
			right = inspector(m, row.host, row.pool, rightW)
		} else if row.ds != nil {
			rightTitle = row.ds.Base()
			right = dsInspector(m, row.host, row.ds, rightW)
		}
	}
	// while the cursor stays in the marks' home dataset, the panel answers
	// for the selection as a whole
	if len(m.marks) > 0 && (row.id == m.markOwner || row.parentID == m.markOwner) {
		rightTitle = fmt.Sprintf("selection (%d)", len(m.markedSnaps()))
		right = selInspector(m, rightW)
	}
	panelKey := fmt.Sprintf("t|%s|%d", m.treeSel, m.markGen)
	if panelKey != m.panelKey {
		m.panelKey, m.panelScroll = panelKey, 0
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
	if len(noteLines) > 4 {
		hidden := len(noteLines) - 4
		noteLines = append(noteLines[:4], fmt.Sprintf("… (+%d lines — zpool status has the rest)", hidden))
	}
	for _, n := range noteLines {
		head = append(head, " "+styWarn.Render(n))
	}

	head = append(head, "")
	lines := head
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

	lines = append(lines, "")

	// live io, promoted above the topology: host-view grammar plus the
	// ops/s readings a pool has earned
	if r, ok := h.io[p.Name]; ok {
		hist := h.ioHist[p.Name]
		rh := make([]int64, len(hist))
		wh := make([]int64, len(hist))
		for j, s := range hist {
			rh[j] = s.RBw
			wh[j] = s.WBw
		}
		sw := w - 33
		if sw > dsIOHistLen/2 {
			sw = dsIOHistLen / 2
		}
		if sw < 8 {
			sw = 8
		}
		// one shared elastic width for both ops cells — separate keys let a
		// read burst widen only its own line and the right edges drift
		ops := func(bw, n int64) string {
			cell := elastic(h.ioW, "ops", opsCell(bw, n))
			if bw == 0 && n == 0 {
				return styDim.Render(cell)
			}
			return dimUnit(cell)
		}
		lines = append(lines,
			" "+styBold.Render("io")+"   r "+ioRate(r.RBw, 7)+"  "+sparklineFam(sparkSteel, rh, sw)+
				"  "+ops(r.RBw, r.ROps),
			"      w "+ioRate(r.WBw, 7)+"  "+sparklineFam(sparkGold, wh, sw)+
				"  "+ops(r.WBw, r.WOps))
	} else {
		lines = append(lines, " "+styBold.Render("io")+"   "+styDim.Render("sampling…"))
	}
	lines = append(lines, "")

	// topology, fully expanded. The three error-counter columns are gone:
	// zeros were noise, so the verdict column carries the story instead —
	// ONLINE for the healthy, counters REPLACING the word on a serving
	// device with errors, state · counters when both have news. The column
	// sizes itself to its longest occupant.
	type topoEnt struct {
		depth   int
		display string
		sum     string
		verdict string
		vsty    lipgloss.Style
		temp    string
		read    string
		written string
		note    string
	}
	var ents []topoEnt
	var walk func(v *zfs.Vdev, classPrefix string, depth int)
	walk = func(v *zfs.Vdev, classPrefix string, depth int) {
		sum := zfs.NiceBytes(v.Size)
		if len(v.Children) > 0 {
			leaves := v.Leaves()
			sum = fmt.Sprintf("%d× %s", len(leaves), zfs.NiceBytes(leaves[0].Size))
		}
		// the verdict column is the UNIFIED health of this row — zpool
		// state, zpool counters, and the drive's own smart testimony,
		// worst tier wins, reasons spelled out. A WARN in the tree must
		// find its cause here, not a dead end.
		sev := stateSev(v.State)
		var parts []string
		if v.State != "ONLINE" {
			parts = append(parts, v.State)
		}
		if badge := errBadge(zfs.ErrCount(v.ReadErr), zfs.ErrCount(v.WriteErr), zfs.ErrCount(v.CksumErr), 24); badge != "" {
			parts = append(parts, badge)
			if sev < sevWarn {
				sev = sevWarn
			}
		}
		temp, read, written := "", "", ""
		if len(v.Children) == 0 {
			if d := h.diskFor(v.Name); d != nil {
				temp = "-" // resolved but unsensed
				if d.TempC >= 0 {
					temp = fmt.Sprintf("%d°C", d.TempC)
				}
				if s, ok := h.smart[d.Node]; ok {
					if s.ReadBytes >= 0 {
						read = zfs.NiceBytes(s.ReadBytes)
					}
					if s.WriteBytes >= 0 {
						written = zfs.NiceBytes(s.WriteBytes)
					}
					// just the tier — the factfinding is phase 3's drill
					switch smartSev(s) {
					case sevErr:
						parts = append(parts, "FAIL")
						sev = sevErr
					case sevWarn:
						parts = append(parts, "WARN")
						if sev < sevWarn {
							sev = sevWarn
						}
					}
				}
			}
		}
		verdict := strings.Join(parts, " · ")
		if verdict == "" {
			verdict = v.State
		}
		ents = append(ents, topoEnt{depth, classPrefix + v.Name, sum, verdict, sevStyle(sev), temp, read, written, v.Note})
		for _, c := range v.Children {
			walk(c, "", depth+1)
		}
	}
	for _, c := range p.Classes {
		prefix := ""
		if c.Name != "data" {
			prefix = c.Name + " "
			if c.Name == "logs" {
				prefix = "log "
			}
		}
		for _, v := range c.Vdevs {
			walk(v, prefix, 0)
		}
	}
	vw := lipgloss.Width("STATE")
	haveLife := false
	for _, e := range ents {
		if n := lipgloss.Width(e.verdict); n > vw {
			vw = n
		}
		if e.read != "" || e.written != "" {
			haveLife = true
		}
	}
	lifeW := 0
	if haveLife {
		lifeW = 14 // two lifetime odometers where the counter columns once sat
	}
	nameW := w - 13 - vw - lifeW
	if nameW > 42 {
		nameW = 42
	}
	if nameW < 12 {
		nameW = 12
	}
	ashiftCell := ""
	if a, ok := h.ashift[p.Name]; ok {
		ashiftCell = styDim.Render("ashift " + strconv.Itoa(a))
	}
	topoHead := padR("STATE", vw) + padL("TEMP", 6)
	if haveLife {
		topoHead += padL("READ", 7) + padL("WRIT", 7)
	}
	lines = append(lines, " "+padR(ashiftCell, nameW+9)+" "+styDim.Render(topoHead))
	life := func(v string) string {
		if v == "" {
			return rep(" ", 7)
		}
		return dimUnit(padL(v, 7))
	}
	for _, e := range ents {
		row := " " + rep("  ", e.depth) + padR(truncate(e.display, nameW-e.depth*2), nameW-e.depth*2) +
			padL(e.sum, 9) + " " + e.vsty.Render(padR(e.verdict, vw))
		switch {
		case e.temp == "" || e.temp == "-":
			row += styDim.Render(padL(e.temp, 6))
		default:
			row += dimUnit(padL(e.temp, 6))
		}
		if haveLife {
			row += life(e.read) + life(e.written)
		}
		if e.note != "" {
			if room := w - lipgloss.Width(row) - 2; room >= 4 {
				row += " " + styWarn.Render(truncate(e.note, room))
			}
		}
		lines = append(lines, row)
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
	switch m.mode {
	case modePerf:
		if m.perf.host != nil {
			return m.perf.host
		}
	default:
		if row := m.treeSelected(); row.host != nil {
			return row.host
		}
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
