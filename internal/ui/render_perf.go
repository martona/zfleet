package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// tabCells windows a row of tab labels so the current one stays visible
// when the fleet outgrows the line.
func tabCells(cells []string, curIdx, w int) string {
	width := func(a, b int) int {
		total := 0
		for _, c := range cells[a:b] {
			total += lipgloss.Width(c) + 1
		}
		return total
	}
	start, end := 0, len(cells)
	for width(start, end) > w-6 && (start < curIdx || end > curIdx+1) {
		if end-1 > curIdx {
			end--
		} else {
			start++
		}
	}
	bar := strings.Join(cells[start:end], " ")
	if start > 0 {
		bar = styDim.Render("‹ ") + bar
	}
	if end < len(cells) {
		bar += styDim.Render(" ›")
	}
	return bar
}

// perfPoolBar renders the pool tabs of the dashboard's host — current one
// inverted.
func perfPoolBar(m *Model, w int) string {
	var cells []string
	curIdx := 0
	for i, p := range m.perf.host.pools {
		label := " " + p.Name + " "
		if p.Name == m.perf.pool {
			curIdx = i
			cells = append(cells, styInv.Render(label))
		} else {
			cells = append(cells, styDim.Render(label))
		}
	}
	return " " + tabCells(cells, curIdx, w)
}

// perfHostBar renders the host line above the pool line; a `▸` marks which
// of the two ←/→ currently walks. Unreachable hosts show dark.
func perfHostBar(m *Model, w int) (hostLine, poolLine string) {
	var cells []string
	curIdx := 0
	for i, h := range m.hosts {
		label := " " + h.name + " "
		switch {
		case h == m.perf.host:
			curIdx = i
			cells = append(cells, styInv.Render(label))
		case h.conn == connDown:
			cells = append(cells, styDim.Render(" "+h.name+"× "))
		default:
			cells = append(cells, styDim.Render(label))
		}
	}
	hostMark, poolMark := "  ", "  "
	if m.perf.focusHosts {
		hostMark = "▸ "
	} else {
		poolMark = "▸ "
	}
	hostLine = " " + hostMark + styDim.Render("host ") + tabCells(cells, curIdx, w-8)
	poolLine = " " + poolMark + styDim.Render("pool ") + perfPoolBar(m, w-8)
	return hostLine, poolLine
}

// The verdict tiers — the banner's voice. Scream inverts a full-width band;
// attention is yellow text; flow is a normal line; idle is a dim line. The
// banner alone carries the tier: a trickle pool straddles the idle
// threshold every few samples, so whole-body dimming strobed.
const (
	vScream = iota
	vAttn
	vFlow
	vIdle
)

func perfPane(m *Model, w int) []string {
	h := m.perf.host
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	if m.multiHost {
		hostLine, poolLine := perfHostBar(m, w)
		add(hostLine)
		add(poolLine)
	} else {
		add(perfPoolBar(m, w))
	}
	add("")
	if h.conn == connDown {
		add(" " + styWarn.Render(h.name+" "+hostOutage(h)))
		if !h.lastOK.IsZero() {
			add(" " + styDim.Render("last data "+niceAge(time.Since(h.lastOK))+" ago"))
		}
		if h.errText != "" {
			for _, ln := range wrap(h.errText, w-3) {
				add(" " + styDim.Render(ln))
			}
		}
		return lines
	}
	if !m.perf.have {
		add(" " + styDim.Render("collecting…"))
		return lines
	}

	// ── diagnostics gathered first so the verdict can weigh all of it ──
	arc := h.arcMap
	var perfPool *zfs.Pool
	for _, p := range h.pools {
		if p.Name == m.perf.pool {
			perfPool = p
		}
	}
	hasSlog := perfPool != nil && perfPool.Class("logs") != nil
	scanActive := perfPool != nil && perfPool.Scan.State == zfs.ScanInProgress

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
	commitRate := m.perfRate(m.perf.zil, m.perf.zilPrev, "zil_commit_count")
	slogLatW := int64(-1)
	if hasSlog {
		for _, v := range perfPool.Class("logs").Vdevs {
			if lat, _, n := m.latWindow(v.Name); n > 0 {
				slogLatW = lat.TotalW
			}
		}
	}
	io := h.io[m.perf.pool]
	sum := zfs.SummarizeTxgs(m.perf.txgs, 20)
	dirtyMax := m.perf.params["zfs_dirty_data_max"]
	delayPct := m.perf.params["zfs_delay_min_dirty_percent"]
	delayBytes := dirtyMax * delayPct / 100
	delayRate := m.perfRate(m.perf.dmu, m.perf.dmuPrev, "dmu_tx_dirty_delay")

	// ── the verdict: the old `reading:` heuristics, promoted to a banner ──
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

	// ── io: rate history two rows tall, scaled to the remembered high-water
	// mark (life of the process, per pool) — a self-scaled ring deflates as
	// its biggest samples age out and would read a fading pool as slammed ──
	ring := h.ioHist[m.perf.pool]
	rv := make([]int64, len(ring))
	wv := make([]int64, len(ring))
	for i, s := range ring {
		rv[i], wv[i] = s.RBw, s.WBw
	}
	pkey := h.name + "\x00" + m.perf.pool
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
	if half < 8 {
		half = 8
	}
	rT := sparklineTall(sparkSteel, rv, half, 2, pk[0])
	wT := sparklineTall(sparkGold, wv, half, 2, pk[1])
	add(" " + styBold.Render("io") + "    read  " + rT[0] + " " + ioRate(io.RBw, 8) +
		"   write " + wT[0] + " " + ioRate(io.WBw, 8))
	add(rep(" ", 13) + rT[1] + rep(" ", 18) + wT[1])
	add("       " + styDim.Render("scaled to high-water · r "+zfs.NiceBytes(pk[0])+"/s · w "+zfs.NiceBytes(pk[1])+"/s"))
	add("")

	// ── dirty vs dirty_data_max: the write throttle, drawn. Fixed absolute
	// scale, the delay line ruled across; a column crossing the line renders
	// its above-line part warn — the part over the flood line is the part
	// that delays writes ──
	const chartH, prefixW, scaleLbl = 4, 9, 17
	// fixed geometry: the chart claims its full width from the first frame —
	// rule and labels pinned, newest txg at the right edge, history growing
	// leftward into the blank as txgHist banks committed rows across ticks
	// (the kernel ring remembers only ~100; committed only — ndirty is
	// recorded at sync completion, and charting open rows pinned a false
	// floor to the newest edge). A right edge that travels as the bank
	// fills was noise.
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
	add(" " + styBold.Render("dirty") + "  " +
		dimLabels(fmt.Sprintf("avg %s · peak %s · ", zfs.NiceBytes(dsum.DirtyAvg), zfs.NiceBytes(dsum.DirtyPeak))) +
		throtSty(throt) +
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
	// braille cells: two txgs per cell at sixteen dot-levels — the same
	// glyph grammar as every other chart (a measured zero keeps a muted
	// baseline dot, ink starts a dot above it, absent data stays blank);
	// the delay rule fills whatever cells no ink claims
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
		add(row)
	}
	add("")

	// ── arc (global) ──────────────────────────────────────────────
	hitPct := func(h, ms int64) string {
		if h+ms <= 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(h)/float64(h+ms))
	}
	add(" " + styBold.Render("arc") + "   " + zfs.NiceBytes(h.arc.Size) + " / " +
		zfs.NiceBytes(h.arc.CMax) + " · hit " + rollHit + " " + sparklineFam(sparkSteel, h.hitHist, 8) +
		"   mru " + zfs.NiceBytes(arc["mru_size"]) + " · mfu " + zfs.NiceBytes(arc["mfu_size"]))
	l2 := ""
	if arc["l2_size"] > 0 {
		l2 = " · l2 " + zfs.NiceBytes(arc["l2_size"]) + " hit " + hitPct(arc["l2_hits"], arc["l2_misses"])
	}
	add("       " + styDim.Render("demand-data "+hitPct(arc["demand_data_hits"], arc["demand_data_misses"])+
		" · demand-meta "+hitPct(arc["demand_metadata_hits"], arc["demand_metadata_misses"])+
		" · prefetch "+hitPct(arc["prefetch_data_hits"], arc["prefetch_data_misses"])+l2))
	ghostLine := fmt.Sprintf("ghost hits mru %.1f/s · mfu %.1f/s", h.arcRate("mru_ghost_hits"), h.arcRate("mfu_ghost_hits"))
	if memThrottle > 0 {
		ghostLine += styWarn.Render(fmt.Sprintf(" · memory throttled %.1f/s", memThrottle))
	}
	if noGrow {
		ghostLine += styWarn.Render(" · arc growth paused")
	}
	add("       " + styDim.Render(ghostLine))
	add("")

	// ── txg engine (dirty and throttle moved up to their chart) ───
	// duty = fraction of wall time the pool spends in sync — avg sync
	// seconds per txg × txgs per second. A fast pool fed by a slow writer
	// runs single-digit duty and its iostat samples read as pulses.
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
	add("")

	// ── vdev latency (windowed) ───────────────────────────────────
	// cells are window averages so a persistent straggler separates from
	// rotating GC noise; w-peak keeps the worst single sample honest.
	// always fully expanded — j/k scrolling replaced the fit-or-force logic
	type latEnt struct {
		indent string
		full   string // complete name — used whenever it fits
		lcp    int    // where the distinguishing part starts (0 = no anchor)
		key    string
		dim    bool
		leaf   bool // a physical device: straggler math runs over these
	}
	var ents []latEnt
	if perfPool != nil {
		for _, c := range perfPool.Classes {
			prefix := ""
			if c.Name != "data" {
				prefix = c.Name + " "
				if c.Name == "logs" {
					prefix = "log "
				}
			}
			for _, v := range c.Vdevs {
				leaves := v.Leaves()
				solo := len(leaves) == 1 && leaves[0] == v
				ents = append(ents, latEnt{"   ", prefix + v.Name, 0, v.Name, false, solo})
				lcp := 0
				if len(leaves) > 1 {
					var names []string
					for _, leaf := range leaves {
						names = append(names, leaf.Name)
					}
					if lcp = commonPrefixLen(names); lcp < 8 {
						lcp = 0
					}
				}
				for _, leaf := range leaves {
					if leaf == v {
						continue
					}
					ents = append(ents, latEnt{"     ", leaf.Name, lcp, leaf.Name, true, true})
				}
			}
		}
	}
	// straggler: a leaf ≥3× the median of the pool's leaves (R or W total,
	// with ≥50 ops in the window behind it) lights up — self-relative, so it
	// works for rust and nvme alike; absolute thresholds across mixed media
	// would lie
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
	slow := func(e latEnt) bool {
		if !e.leaf {
			return false
		}
		avg, _, n := m.latWindow(e.key)
		if n == 0 {
			return false
		}
		if len(medRs) >= 3 && medR > 0 && avg.ROps >= 50 && avg.TotalR >= 3*medR {
			return true
		}
		return len(medWs) >= 3 && medW > 0 && avg.WOps >= 50 && avg.TotalW >= 3*medW
	}
	capW := w - 3 - 7*9 - 1
	windowSecs := 0
	for _, ring := range m.perf.latHist {
		if len(ring) > windowSecs {
			windowSecs = len(ring)
		}
	}
	windowSecs *= int(perfInterval.Seconds())
	label := fmt.Sprintf("vdev latency · %ds avg", windowSecs)
	// name column grows into whatever the seven data columns leave over,
	// and never shrinks under the header label's feet
	nameW := lipgloss.Width(label) - 2
	for _, e := range ents {
		if n := lipgloss.Width(e.indent+e.full) - 2; n > nameW {
			nameW = n
		}
	}
	if nameW > capW {
		nameW = capW
	}
	// per-row: full name when it fits; otherwise cut only what's necessary,
	// starting immediately left of the distinguishing part — the shared
	// prefix yields one character at a time, the serial tail never does
	disp := func(e latEnt) string {
		avail := nameW - (len(e.indent) - 3)
		if len(e.full) <= avail {
			return e.full
		}
		if e.lcp > 0 {
			tail := e.full[e.lcp:]
			if room := avail - 1 - len(tail); room > 0 {
				return e.full[:room] + "…" + tail
			}
			return truncate("…"+tail, avail)
		}
		return truncate(e.full, avail)
	}
	cell := func(ns int64) string { return padL(zfs.NiceNS(ns), 9) }
	add(" " + styBold.Render(padR(label, nameW+2)) +
		styDim.Render(padL("r-total", 9)+padL("w-total", 9)+padL("r-disk", 9)+padL("w-disk", 9)+
			padL("r-queue", 9)+padL("w-queue", 9)+padL("w-peak", 9)))
	for _, e := range ents {
		avg, peakW, _ := m.latWindow(e.key)
		ind := e.indent
		straggler := slow(e)
		if straggler {
			ind = ind[:len(ind)-2] + "! "
		}
		row := padR(ind+disp(e), nameW+3) +
			cell(avg.TotalR) + cell(avg.TotalW) + cell(avg.DiskR) + cell(avg.DiskW) +
			cell(avg.QueueR()) + cell(avg.QueueW()) + cell(peakW)
		switch {
		case straggler:
			row = styWarn.Render(row)
		case e.dim:
			row = styDim.Render(row)
		}
		add(row)
	}
	add("")

	// ── zil ───────────────────────────────────────────────────────
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
	add("")

	// ── top io: this pool's datasets, filling the rows that remain ─
	type talker struct {
		name string
		r, w int64
	}
	var talkers []talker
	for name, io := range h.dsIO {
		if poolOf(name) != m.perf.pool || io.RBw+io.WBw == 0 {
			continue
		}
		talkers = append(talkers, talker{name, io.RBw, io.WBw})
	}
	sort.Slice(talkers, func(i, j int) bool {
		return talkers[i].r+talkers[i].w > talkers[j].r+talkers[j].w
	})
	add(" " + styBold.Render("top io"))
	if len(talkers) == 0 {
		add("   " + styDim.Render("no dataset io this interval"))
	}
	for i, t := range talkers {
		if i >= 8 {
			break
		}
		add("   " + padR(truncate(t.name, w-32), w-32) +
			" r " + padL(zfs.NiceBytes(t.r)+"/s", 8) + "  w " + padL(zfs.NiceBytes(t.w)+"/s", 8))
	}

	if m.perf.err != "" {
		add("")
		add(" " + styWarn.Render("collector: "+truncate(m.perf.err, w-14)))
	}
	if strings.HasPrefix(h.src.Name(), "replay") {
		add("")
		add(" " + styDim.Render("[replay] rates read as zero — counters are frozen"))
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
