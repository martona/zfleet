package ui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The verdict tiers — the banner's voice. Scream inverts a full-width band;
// attention is yellow text; flow is a normal line; idle is a dim line.
const (
	vScream = iota
	vAttn
	vFlow
	vIdle
)

// poolPerfLines renders the pool panel's live-engine blocks: the verdict
// banner, io charts, dirty chart, arc, txg and zil. Blocks that ride the
// main ticks (io, arc) render always; the perf-fed ones wait for the 2s
// collectors armed by the cursor sitting on this pool.
func poolPerfLines(m *Model, h *hostState, p *zfs.Pool, w int) []string {
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	armed := m.perf.host == h && m.perf.pool == p.Name && m.perf.have

	// ── diagnostics gathered first so the verdict can weigh all of it ──
	arc := h.arcMap
	hasSlog := p.Class("logs") != nil
	scanActive := p.Scan.State == zfs.ScanInProgress
	rollPct := -1.0
	if dh, dm := h.arc.Hits-h.arcPrev.Hits, h.arc.Misses-h.arcPrev.Misses; dh+dm > 0 {
		rollPct = 100 * float64(dh) / float64(dh+dm)
	} else if h.arc.Hits+h.arc.Misses > 0 {
		rollPct = 100 * float64(h.arc.Hits) / float64(h.arc.Hits+h.arc.Misses)
	}
	rollHit := "-"
	if rollPct >= 0 {
		rollHit = fmt.Sprintf("%.1f%%", rollPct)
	}
	ghostRate := h.arcRate("mru_ghost_hits") + h.arcRate("mfu_ghost_hits")
	missRate := h.arcRate("misses")
	memThrottle := h.arcRate("memory_throttle_count")
	noGrow := arc["arc_no_grow"] > 0
	io := h.io[p.Name]

	if armed {
		commitRate := m.perfRate(m.perf.zil, m.perf.zilPrev, "zil_commit_count")
		slogLatW := int64(-1)
		if hasSlog {
			for _, v := range p.Class("logs").Vdevs {
				if lat, _, n := m.latWindow(v.Name); n > 0 {
					slogLatW = lat.TotalW
				}
			}
		}
		sum := zfs.SummarizeTxgs(m.perf.txgs, 20)
		dirtyMax := m.perf.params["zfs_dirty_data_max"]
		delayPct := m.perf.params["zfs_delay_min_dirty_percent"]
		delayBytes := dirtyMax * delayPct / 100
		delayRate := m.perfRate(m.perf.dmu, m.perf.dmuPrev, "dmu_tx_dirty_delay")

		// ── the verdict banner ────────────────────────────────────────
		busyDirty := dirtyMax > 0 && sum.DirtyPeak > delayBytes
		arcStarved := rollPct >= 0 && rollPct < 90 && missRate > 5 && ghostRate > missRate/4
		idleFloor := dirtyMax / 100
		if idleFloor == 0 {
			idleFloor = 16 << 20
		}
		tier, verdict := vFlow, ""
		switch {
		case delayRate > 0.5:
			tier = vScream
			verdict = fmt.Sprintf("THROTTLED  %.1f delays/s · dirty peak %s of %s · see vdev latency",
				delayRate, zfs.NiceBytes(sum.DirtyPeak), zfs.NiceBytes(dirtyMax))
		case commitRate > 20 && slogLatW > 1e6:
			tier = vScream
			verdict = fmt.Sprintf("SYNC-BOUND  slog latency elevated · w-total %s · %.0f commits/s",
				zfs.NiceNS(slogLatW), commitRate)
		case arcStarved:
			tier = vAttn
			verdict = fmt.Sprintf("arc-starved · hit %s · misses re-fetch evicted data · more ARC would help", rollHit)
		case commitRate > 20 && !hasSlog:
			tier = vAttn
			verdict = fmt.Sprintf("sync-heavy · %.0f commits/s landing on data vdevs · a slog would help", commitRate)
		case busyDirty:
			tier = vAttn
			verdict = fmt.Sprintf("near the throttle · dirty peaks %s of the %s delay line",
				zfs.NiceBytes(sum.DirtyPeak), zfs.NiceBytes(delayBytes))
		case io.RBw+io.WBw < 1<<20 && sum.DirtyAvg < idleFloor:
			tier = vIdle
			if gap, found := zfs.LastDirtyGap(m.perf.txgs, 1<<20); found {
				verdict = "idle · heartbeat txgs · last real write " + niceAge(time.Duration(gap)) + " ago"
			} else if gap > 0 {
				verdict = "idle · heartbeat txgs · quiet for the whole ring (" + niceAge(time.Duration(gap)) + "+)"
			} else {
				verdict = "idle · heartbeat txgs, nothing to drain"
			}
		default:
			verdict = fmt.Sprintf("flowing · r %s/s · w %s/s · absorbing without braking",
				zfs.NiceBytes(io.RBw), zfs.NiceBytes(io.WBw))
		}
		scrub := ""
		if scanActive {
			scrub = " · scrub competing"
		}
		switch tier {
		case vScream:
			add(" " + styWarnInv.Render(padR(" "+verdict+scrub+" ", w-2)))
		case vAttn:
			add(" " + styWarn.Render(verdict) + styDim.Render(scrub))
		case vIdle:
			add(" " + styDim.Render(verdict+scrub))
		default:
			add(" " + dimLabels(verdict) + styDim.Render(scrub))
		}
		add("")

		ioChartLines(m, h, p, w, &lines)

		// ── dirty vs dirty_data_max: the write throttle, drawn ────────
		dirtyChartLines(m, w, dirtyMax, delayBytes, delayRate, &lines)
		add("")
		arcLines(h, rollHit, memThrottle, noGrow, &lines)
		add("")
		duty := ""
		if sum.PerMinute > 0 && sum.SAvg > 0 {
			duty = styDim.Render(" · duty ") + fmt.Sprintf("%.0f%%", float64(sum.SAvg)/1e9*sum.PerMinute/60*100)
		}
		add(" " + styBold.Render("txg") + fmt.Sprintf("   %.0f/min", sum.PerMinute) + duty +
			"   " + styDim.Render("sync ") + sparklineFam(sparkGold, sum.SyncTimes, 10))
		add("       " + styDim.Render("open ") + zfs.NiceNS(sum.OAvg) +
			styDim.Render(" · quiesce ") + zfs.NiceNS(sum.QAvg) +
			styDim.Render(" · wait ") + zfs.NiceNS(sum.WAvg) +
			styDim.Render(" · sync ") + zfs.NiceNS(sum.SAvg))
		slogCnt := m.perf.zil["zil_itx_metaslab_slog_count"]
		normalCnt := m.perf.zil["zil_itx_metaslab_normal_count"]
		slogBps := m.perfRate(m.perf.zil, m.perf.zilPrev, "zil_itx_metaslab_slog_bytes")
		normBps := m.perfRate(m.perf.zil, m.perf.zilPrev, "zil_itx_metaslab_normal_bytes")
		slogTxt := fmt.Sprintf("slog %s/s (%s itx) · normal %s/s (%s itx)",
			zfs.NiceBytes(int64(slogBps)), zfs.NiceCount(slogCnt),
			zfs.NiceBytes(int64(normBps)), zfs.NiceCount(normalCnt))
		if hasSlog && slogCnt == 0 {
			slogTxt += styWarn.Render(" (slog unused)")
		}
		add(" " + styBold.Render("zil") + fmt.Sprintf("   %.1f commits/s · ", commitRate) + slogTxt)
		if m.perf.err != "" {
			add(" " + styWarn.Render("collector: "+truncate(m.perf.err, w-14)))
		}
	} else {
		// the main-tick blocks stand alone until the 2s collectors report
		add(" " + styDim.Render("engine: collecting…"))
		add("")
		ioChartLines(m, h, p, w, &lines)
		arcLines(h, rollHit, memThrottle, noGrow, &lines)
	}
	return lines
}

// ioChartLines renders the r/w bandwidth history two rows tall, scaled to
// the remembered high-water mark (life of the process, per pool) — a
// self-scaled ring deflates as its biggest samples age out and would read
// a fading pool as slammed.
func ioChartLines(m *Model, h *hostState, p *zfs.Pool, w int, lines *[]string) {
	ring := h.ioHist[p.Name]
	rv := make([]int64, len(ring))
	wv := make([]int64, len(ring))
	for i, s := range ring {
		rv[i], wv[i] = s.RBw, s.WBw
	}
	pkey := h.name + "\x00" + p.Name
	pk := m.perfPeak[pkey]
	for i := range ring {
		if rv[i] > pk[0] {
			pk[0] = rv[i]
		}
		if wv[i] > pk[1] {
			pk[1] = wv[i]
		}
	}
	m.perfPeak[pkey] = pk
	half := (w - 41) / 2
	if half > 32 {
		half = 32
	}
	if half < 6 {
		half = 6
	}
	fam := [2]sparkFam{sparkSteel, sparkGold}
	if h.conn == connDown {
		fam = [2]sparkFam{sparkDead, sparkDead}
	}
	io := h.io[p.Name]
	rT := sparklineTall(fam[0], rv, half, 2, pk[0])
	wT := sparklineTall(fam[1], wv, half, 2, pk[1])
	*lines = append(*lines,
		" "+styBold.Render("io")+"    read  "+rT[0]+" "+ioRate(io.RBw, 8)+
			"   write "+wT[0]+" "+ioRate(io.WBw, 8),
		rep(" ", 13)+rT[1]+rep(" ", 18)+wT[1],
		"       "+styDim.Render("scaled to high-water · r "+zfs.NiceBytes(pk[0])+"/s · w "+zfs.NiceBytes(pk[1])+"/s"+
			fmt.Sprintf(" · ops r %s w %s", zfs.NiceCount(io.ROps), zfs.NiceCount(io.WOps))),
		"")
}

// arcLines renders the host-global ARC block — host context the pool
// verdicts lean on.
func arcLines(h *hostState, rollHit string, memThrottle float64, noGrow bool, lines *[]string) {
	arc := h.arcMap
	hitPct := func(hits, ms int64) string {
		if hits+ms <= 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(hits)/float64(hits+ms))
	}
	*lines = append(*lines, " "+styBold.Render("arc")+"   "+zfs.NiceBytes(h.arc.Size)+" / "+
		zfs.NiceBytes(h.arc.CMax)+" · hit "+rollHit+" "+sparklineFam(sparkSteel, h.hitHist, 8)+
		"   mru "+zfs.NiceBytes(arc["mru_size"])+" · mfu "+zfs.NiceBytes(arc["mfu_size"]))
	l2 := ""
	if arc["l2_size"] > 0 {
		l2 = " · l2 " + zfs.NiceBytes(arc["l2_size"]) + " hit " + hitPct(arc["l2_hits"], arc["l2_misses"])
	}
	*lines = append(*lines, "       "+styDim.Render("demand-data "+hitPct(arc["demand_data_hits"], arc["demand_data_misses"])+
		" · demand-meta "+hitPct(arc["demand_metadata_hits"], arc["demand_metadata_misses"])+
		" · prefetch "+hitPct(arc["prefetch_data_hits"], arc["prefetch_data_misses"])+l2))
	ghostLine := fmt.Sprintf("ghost hits mru %.1f/s · mfu %.1f/s", h.arcRate("mru_ghost_hits"), h.arcRate("mfu_ghost_hits"))
	if memThrottle > 0 {
		ghostLine += styWarn.Render(fmt.Sprintf(" · memory throttled %.1f/s", memThrottle))
	}
	if noGrow {
		ghostLine += styWarn.Render(" · arc growth paused")
	}
	*lines = append(*lines, "       "+styDim.Render(ghostLine))
}

// dirtyChartLines renders the dirty-vs-dirty_data_max chart: fixed
// geometry claimed from the first frame, the delay line ruled across,
// columns crossing it wearing warn above the line.
func dirtyChartLines(m *Model, w int, dirtyMax, delayBytes int64, delayRate float64, lines *[]string) {
	const chartH, prefixW, scaleLbl = 4, 9, 17
	txgRows := m.perf.txgHist
	chartW := w - prefixW - scaleLbl
	if chartW < 20 {
		chartW = 20
	}
	if len(txgRows) > 2*chartW {
		txgRows = txgRows[len(txgRows)-2*chartW:]
	}
	dsum := zfs.SummarizeTxgs(txgRows, len(txgRows))
	scale := dirtyMax
	if scale <= 0 {
		for _, r := range txgRows {
			if r.NDirty > scale {
				scale = r.NDirty
			}
		}
	}
	throt := fmt.Sprintf("%.1f delays/s", delayRate)
	throtSty := styDim.Render
	if delayRate > 0.5 {
		throtSty = styWarn.Render
	}
	*lines = append(*lines, " "+styBold.Render("dirty")+"  "+
		dimLabels(fmt.Sprintf("avg %s · peak %s · ", zfs.NiceBytes(dsum.DirtyAvg), zfs.NiceBytes(dsum.DirtyPeak)))+
		throtSty(throt)+
		styDim.Render(fmt.Sprintf(" (%s since boot) · last %d txgs",
			zfs.NiceCount(m.perf.dmu["dmu_tx_dirty_delay"]), len(txgRows))))
	lineRow := -1
	if dirtyMax > 0 && delayBytes > 0 && scale > 0 {
		f := float64(delayBytes) / float64(scale)
		lineRow = chartH - 1 - int(f*float64(chartH))
		if lineRow < 0 {
			lineRow = 0
		}
		if lineRow >= chartH {
			lineRow = chartH - 1
		}
	}
	labels := map[int]string{}
	if dirtyMax > 0 {
		labels[0] = "◂ max " + zfs.NiceBytes(scale)
	} else if scale > 0 {
		labels[0] = "◂ peak " + zfs.NiceBytes(scale)
	}
	if lineRow > 0 {
		labels[lineRow] = "◂ delay " + zfs.NiceBytes(delayBytes)
	} else if lineRow == 0 {
		labels[0] = "◂ delay " + zfs.NiceBytes(delayBytes)
	}
	total := chartH * 4
	lvl := func(nd int64) int {
		if nd <= 0 || scale <= 0 {
			return 1
		}
		l := 2 + int(nd*int64(total-2)/scale)
		if l > total {
			l = total
		}
		return l
	}
	clamp4 := func(x int) rune {
		if x < 0 {
			x = 0
		}
		if x > 4 {
			x = 4
		}
		return rune(x)
	}
	for cr := 0; cr < chartH; cr++ {
		dotFloor := 4 * (chartH - 1 - cr)
		var sb, runBuf strings.Builder
		runSty := 0 // 0 dim, 1 ink, 2 warn
		flush := func() {
			if runBuf.Len() == 0 {
				return
			}
			switch runSty {
			case 2:
				sb.WriteString(styWarn.Render(runBuf.String()))
			case 1:
				sb.WriteString(sparkGold[1].Render(runBuf.String()))
			default:
				sb.WriteString(styDim.Render(runBuf.String()))
			}
			runBuf.Reset()
		}
		put := func(r rune, sty int) {
			if sty != runSty {
				flush()
				runSty = sty
			}
			runBuf.WriteRune(r)
		}
		rule := func() {
			if cr == lineRow {
				put('╌', 0)
			} else {
				put(' ', 0)
			}
		}
		for i := 0; i < chartW-(len(txgRows)+1)/2; i++ {
			rule()
		}
		// a cell's above-line rows wear warn when either of its txgs
		// crossed the delay line; below-line mass stays gold
		cell := func(lL, lR int, aNd, bNd int64) {
			bits := brailleL[clamp4(lL-dotFloor)] | brailleR[clamp4(lR-dotFloor)]
			if bits == 0 {
				rule()
				return
			}
			hot := lL
			if lR > hot {
				hot = lR
			}
			sty := 1
			switch {
			case delayBytes > 0 && lineRow >= 0 && cr <= lineRow &&
				(aNd >= delayBytes || bNd >= delayBytes):
				sty = 2
			case hot <= 1:
				sty = 0 // baseline-only pair: muted
			}
			put(0x2800|bits, sty)
		}
		i := 0
		if len(txgRows)%2 == 1 {
			cell(0, lvl(txgRows[0].NDirty), 0, txgRows[0].NDirty)
			i = 1
		}
		for ; i < len(txgRows); i += 2 {
			cell(lvl(txgRows[i].NDirty), lvl(txgRows[i+1].NDirty),
				txgRows[i].NDirty, txgRows[i+1].NDirty)
		}
		flush()
		row := rep(" ", prefixW) + sb.String()
		if lbl, ok := labels[cr]; ok {
			row += " " + styDim.Render(lbl)
		}
		*lines = append(*lines, row)
	}
}

// poolTable is the unified vdev table: topology verdicts, temperature,
// windowed latency and lifetime odometers on one row per vdev, drills
// beneath the alarmed. Columns pop into fixed slots by priority as the
// panel widens — narrow panels lose columns, never evidence: an alarmed
// or straggling leaf's drill always carries the full latency
// decomposition.
func poolTable(m *Model, h *hostState, p *zfs.Pool, w int) []string {
	type ent struct {
		depth     int
		display   string
		sizeTag   string // vdev rows: "8× 18.2T", rides in the name cell
		key       string
		leaf      bool
		verdict   string
		vsty      lipgloss.Style
		temp      string
		tempHot   int
		read      string
		written   string
		note      string
		alarmed   bool
		straggler bool
		drill     []string
	}
	var ents []ent
	var walk func(v *zfs.Vdev, classPrefix string, depth int)
	walk = func(v *zfs.Vdev, classPrefix string, depth int) {
		e := ent{depth: depth, display: classPrefix + v.Name, key: v.Name,
			leaf: len(v.Children) == 0, note: v.Note}
		if !e.leaf {
			leaves := v.Leaves()
			e.sizeTag = fmt.Sprintf("%d× %s", len(leaves), zfs.NiceBytes(leaves[0].Size))
		}
		// the verdict is the UNIFIED health of the row — zpool state,
		// zpool counters and the drive's own smart testimony, worst tier
		// wins, reasons spelled out
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
		if e.leaf {
			if d := h.diskFor(v.Name); d != nil {
				e.temp = "-" // resolved but unsensed
				if d.TempC >= 0 {
					e.temp = fmt.Sprintf("%d°C", d.TempC)
					ts := h.smart[d.Node]
					switch {
					case ts.TempCrit > 0 && d.TempC >= ts.TempCrit:
						e.tempHot = 2
					case ts.TempHigh > 0 && d.TempC >= ts.TempHigh:
						e.tempHot = 1
					}
				}
				if s, ok := h.smart[d.Node]; ok {
					if s.ReadBytes >= 0 {
						e.read = zfs.NiceBytes(s.ReadBytes)
					}
					if s.WriteBytes >= 0 {
						e.written = zfs.NiceBytes(s.WriteBytes)
					}
					switch h.diskSmartSev(d.Node) {
					case sevErr:
						parts = append(parts, "FAIL")
						sev = sevErr
					case sevWarn:
						parts = append(parts, "WARN")
						if sev < sevWarn {
							sev = sevWarn
						}
					default:
						if smartSev(s) > sevOK {
							parts = append(parts, "ack")
						}
					}
					// any UNANSWERED alarm lays the check ledger beneath;
					// an all-ok ledger under a counter badge is itself a
					// finding (the errors are upstream of the platters)
					if m.verboseDrives || sev > sevOK {
						e.drill = drillLines(h, d, s, " "+rep("  ", depth+1)+"  ", "")
					}
				}
			}
		}
		e.verdict = strings.Join(parts, " · ")
		if e.verdict == "" {
			e.verdict = v.State
		}
		e.vsty = sevStyle(sev)
		e.alarmed = sev > sevOK
		ents = append(ents, e)
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

	// straggler: a leaf ≥3× the median of the pool's leaves (R or W total,
	// ≥50 window ops behind it) lights up — self-relative, no absolute ms
	var medRs, medWs []int64
	for _, e := range ents {
		if !e.leaf {
			continue
		}
		avg, _, n := m.latWindow(e.key)
		if n == 0 {
			continue
		}
		if avg.TotalR >= 0 && avg.ROps >= 50 {
			medRs = append(medRs, avg.TotalR)
		}
		if avg.TotalW >= 0 && avg.WOps >= 50 {
			medWs = append(medWs, avg.TotalW)
		}
	}
	medR, medW := median(medRs), median(medWs)
	for i := range ents {
		e := &ents[i]
		if !e.leaf {
			continue
		}
		avg, peakW, n := m.latWindow(e.key)
		if n == 0 {
			continue
		}
		if len(medRs) >= 3 && medR > 0 && avg.ROps >= 50 && avg.TotalR >= 3*medR {
			e.straggler = true
		}
		if len(medWs) >= 3 && medW > 0 && avg.WOps >= 50 && avg.TotalW >= 3*medW {
			e.straggler = true
		}
		// the drill carries the full latency decomposition whenever the
		// leaf is drilled at all — columns may be cut, evidence never is
		if e.straggler || e.alarmed || m.verboseDrives {
			line := " " + rep("  ", e.depth+1) + "  " + styDim.Render("latency  ") +
				"w-total " + zfs.NiceNS(avg.TotalW) +
				styDim.Render(" · disk ") + zfs.NiceNS(avg.DiskW) +
				styDim.Render(" · queue ") + zfs.NiceNS(avg.QueueW()) +
				styDim.Render(" · peak ") + zfs.NiceNS(peakW)
			if e.straggler {
				line = " " + rep("  ", e.depth+1) + "  " + styWarn.Render("latency  "+
					"w-total "+zfs.NiceNS(avg.TotalW)+" · disk "+zfs.NiceNS(avg.DiskW)+
					" · queue "+zfs.NiceNS(avg.QueueW())+" · peak "+zfs.NiceNS(peakW))
			}
			e.drill = append(e.drill, line)
		}
	}

	// ── responsive columns: fixed spatial slots, presence by priority ──
	vw := lipgloss.Width("STATE")
	for _, e := range ents {
		if n := lipgloss.Width(e.verdict); n > vw {
			vw = n
		}
	}
	if vw > 14 {
		vw = 14
	}
	type colDef struct {
		id   string
		head string
		w    int
		prio int // 0 = always; the rest pop in ascending order
	}
	defs := []colDef{
		{"verdict", "STATE", vw + 1, 0},
		{"temp", "TEMP", 6, 1},
		{"rtot", "r-total", 9, 2},
		{"wtot", "w-total", 9, 0},
		{"rdisk", "r-disk", 9, 7},
		{"wdisk", "w-disk", 9, 5},
		{"rq", "r-queue", 9, 6},
		{"wq", "w-queue", 9, 4},
		{"wpk", "w-peak", 9, 3},
		{"read", "READ", 8, 8},
		{"writ", "WRIT", 8, 9},
	}
	const nameMin = 24
	budget := w - 2 - nameMin
	used := 0
	chosen := map[string]bool{}
	for _, d := range defs {
		if d.prio == 0 {
			chosen[d.id] = true
			used += d.w
		}
	}
	for prio := 1; prio <= 9; prio++ {
		for _, d := range defs {
			if d.prio == prio && used+d.w <= budget {
				chosen[d.id] = true
				used += d.w
			}
		}
	}
	nameW := w - 2 - used
	if nameW > 48 {
		nameW = 48
	}

	// header: ashift + the latency window note live in the name column
	headCell := ""
	if a, ok := h.ashift[p.Name]; ok {
		headCell = "ashift " + strconv.Itoa(a)
	}
	if secs := len(m.perf.latHist); secs > 0 {
		win := 0
		for _, ring := range m.perf.latHist {
			if len(ring) > win {
				win = len(ring)
			}
		}
		headCell += fmt.Sprintf(" · %ds avg", win*int(perfInterval.Seconds()))
	}
	head := " " + styDim.Render(padR(headCell, nameW))
	for _, d := range defs {
		if !chosen[d.id] {
			continue
		}
		if d.id == "verdict" {
			head += styDim.Render(" " + padR(d.head, d.w-1))
		} else {
			head += styDim.Render(padL(d.head, d.w))
		}
	}
	lines := []string{head}

	for _, e := range ents {
		avg, peakW, _ := m.latWindow(e.key)
		lat := func(ns int64) string {
			cell := padL(zfs.NiceNS(ns), 9)
			switch {
			case e.straggler:
				return styWarn.Render(cell)
			case ns < 0:
				return styDim.Render(cell)
			}
			return cell
		}
		ind := rep("  ", e.depth)
		if e.straggler {
			if len(ind) >= 2 {
				ind = ind[:len(ind)-2] + "! "
			} else {
				ind = "! "
			}
		}
		avail := nameW - len(ind)
		base := truncate(e.display, avail)
		tag := ""
		if e.sizeTag != "" && avail-lipgloss.Width(base)-1 >= lipgloss.Width(e.sizeTag) {
			tag = e.sizeTag
		}
		pad := avail - lipgloss.Width(base)
		cellName := base
		if tag != "" {
			cellName += " " + styDim.Render(tag)
			pad -= 1 + lipgloss.Width(tag)
		}
		row := " " + ind + cellName + rep(" ", pad)
		for _, d := range defs {
			if !chosen[d.id] {
				continue
			}
			switch d.id {
			case "verdict":
				row += " " + e.vsty.Render(padR(truncate(e.verdict, d.w-1), d.w-1))
			case "temp":
				cell := padL(e.temp, 6)
				switch {
				case e.temp == "" || e.temp == "-":
					row += styDim.Render(cell)
				case e.tempHot == 2:
					row += styBad.Render(cell)
				case e.tempHot == 1:
					row += styWarn.Render(cell)
				default:
					row += dimUnit(cell)
				}
			case "rtot":
				row += lat(avg.TotalR)
			case "wtot":
				row += lat(avg.TotalW)
			case "rdisk":
				row += lat(avg.DiskR)
			case "wdisk":
				row += lat(avg.DiskW)
			case "rq":
				row += lat(avg.QueueR())
			case "wq":
				row += lat(avg.QueueW())
			case "wpk":
				row += lat(peakW)
			case "read":
				if e.read == "" {
					row += rep(" ", 8)
				} else {
					row += dimUnit(padL(e.read, 8))
				}
			case "writ":
				if e.written == "" {
					row += rep(" ", 8)
				} else {
					row += dimUnit(padL(e.written, 8))
				}
			}
		}
		if e.note != "" {
			if room := w - lipgloss.Width(row) - 2; room >= 4 {
				row += " " + styWarn.Render(truncate(e.note, room))
			}
		}
		lines = append(lines, row)
		lines = append(lines, e.drill...)
	}
	return lines
}

func median(vals []int64) int64 {
	if len(vals) == 0 {
		return 0
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}
