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

	// ── diagnostics gathered first so the reading can weigh all of it ──
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

	// ── arc (global) ──────────────────────────────────────────────
	hitPct := func(h, ms int64) string {
		if h+ms <= 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(h)/float64(h+ms))
	}
	rollHit := "-"
	if rollPct >= 0 {
		rollHit = fmt.Sprintf("%.1f%%", rollPct)
	}
	add(" " + styBold.Render("arc") + "   " + zfs.NiceBytes(h.arc.Size) + " / " +
		zfs.NiceBytes(h.arc.CMax) + " · hit " + rollHit + " " + sparkline(h.hitHist, 8) +
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

	// ── txg engine ────────────────────────────────────────────────
	sum := zfs.SummarizeTxgs(m.perf.txgs, 20)
	dirtyMax := m.perf.params["zfs_dirty_data_max"]
	delayPct := m.perf.params["zfs_delay_min_dirty_percent"]
	maxTxt := ""
	if dirtyMax > 0 {
		maxTxt = "   of " + zfs.NiceBytes(dirtyMax) + " max" +
			styDim.Render(fmt.Sprintf(" (delay @ %s)", zfs.NiceBytes(dirtyMax*delayPct/100)))
	}
	add(" " + styBold.Render("txg") + fmt.Sprintf("   %.0f txg/min · dirty avg %s · peak %s",
		sum.PerMinute, zfs.NiceBytes(sum.DirtyAvg), zfs.NiceBytes(sum.DirtyPeak)) + maxTxt)
	add("       " + styDim.Render("open ") + zfs.NiceNS(sum.OAvg) +
		styDim.Render(" · quiesce ") + zfs.NiceNS(sum.QAvg) +
		styDim.Render(" · wait ") + zfs.NiceNS(sum.WAvg) +
		styDim.Render(" · sync ") + zfs.NiceNS(sum.SAvg) +
		"   " + styDim.Render("sync ") + sparkline(sum.SyncTimes, 10))

	delayRate := m.perfRate(m.perf.dmu, m.perf.dmuPrev, "dmu_tx_dirty_delay")
	throttle := fmt.Sprintf(" %.1f delays/s", delayRate)
	if delayRate == 0 {
		throttle = " 0 delays/s"
	}
	add("       throttle" + throttle +
		styDim.Render(fmt.Sprintf(" (%s since boot)", zfs.NiceCount(m.perf.dmu["dmu_tx_dirty_delay"]))))

	// the reading: a labeled heuristic, not a verdict from on high
	io := h.io[m.perf.pool]
	var reading string
	busyDirty := dirtyMax > 0 && sum.DirtyPeak > dirtyMax*delayPct/100
	arcStarved := rollPct >= 0 && rollPct < 90 && missRate > 5 && ghostRate > missRate/4
	switch {
	case delayRate > 0.5:
		reading = "write-throttled: dirty at the delay ceiling · see vdev latency"
	case arcStarved:
		reading = "arc-starved: misses re-fetch evicted data · more ARC would help"
	case commitRate > 20 && !hasSlog:
		reading = "sync-heavy: commits landing on data vdevs · slog would help"
	case commitRate > 20 && slogLatW > 1e6:
		reading = "sync-heavy: slog latency elevated · see vdev latency"
	case busyDirty:
		reading = "near the throttle: dirty peaks above the delay line"
	case scanActive:
		reading = "scrub competing: scan io shares the vdevs"
	case io.RBw+io.WBw < 1<<20 && sum.DirtyAvg < dirtyMax/100:
		reading = "idle: heartbeat txgs, nothing to drain"
	default:
		reading = "flowing: absorbing without braking; any bottleneck is upstream of ZFS"
	}
	for i, ln := range wrap(reading, w-18) {
		prefix := "reading: "
		if i > 0 {
			prefix = "         "
		}
		add("       " + styDim.Render(prefix+ln))
	}
	add("")

	// ── vdev latency (windowed) ───────────────────────────────────
	// cells are window averages so a persistent straggler separates from
	// rotating GC noise; w-peak keeps the worst single sample honest.
	// Leaf disks expand when the terminal has room (or `t` forces it), with
	// the boilerplate prefix siblings share stripped so rows show the part
	// of the serial that differs.
	// always fully expanded — j/k scrolling replaced the fit-or-force logic
	expandLeaves := true
	type latEnt struct {
		indent string
		full   string // complete name — used whenever it fits
		lcp    int    // where the distinguishing part starts (0 = no anchor)
		key    string
		dim    bool
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
				ents = append(ents, latEnt{"   ", prefix + v.Name, 0, v.Name, false})
				if !expandLeaves {
					continue
				}
				leaves := v.Leaves()
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
					ents = append(ents, latEnt{"     ", leaf.Name, lcp, leaf.Name, true})
				}
			}
		}
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
		row := padR(e.indent+disp(e), nameW+3) +
			cell(avg.TotalR) + cell(avg.TotalW) + cell(avg.DiskR) + cell(avg.DiskW) +
			cell(avg.QueueR()) + cell(avg.QueueW()) + cell(peakW)
		if e.dim {
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
	notes := 0
	if m.perf.err != "" {
		notes += 2
	}
	if strings.HasPrefix(h.src.Name(), "replay") {
		notes += 2
	}
	avail := 8
	if len(talkers) == 0 {
		add("   " + styDim.Render("no dataset io this interval"))
	}
	for i, t := range talkers {
		if i >= avail {
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
