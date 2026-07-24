package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/martona/zfs-explorer/internal/zfs"
)

func perfPane(m *Model, w, h int) []string {
	if !m.perf.have {
		return []string{" " + styDim.Render("collecting…")}
	}
	var lines []string
	add := func(s string) { lines = append(lines, s) }

	// ── arc (global) ──────────────────────────────────────────────
	arc := m.arcMap
	hitPct := func(h, ms int64) string {
		if h+ms <= 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(h)/float64(h+ms))
	}
	rollHit := "-"
	if dh, dm := m.arc.Hits-m.arcPrev.Hits, m.arc.Misses-m.arcPrev.Misses; dh+dm > 0 {
		rollHit = fmt.Sprintf("%.1f%%", 100*float64(dh)/float64(dh+dm))
	} else if m.arc.Hits+m.arc.Misses > 0 {
		rollHit = hitPct(m.arc.Hits, m.arc.Misses)
	}
	add(" " + styBold.Render("arc") + "   " + zfs.NiceBytes(m.arc.Size) + " / " +
		zfs.NiceBytes(m.arc.CMax) + " · hit " + rollHit + " " + sparkline(m.hitHist, 8) +
		"   mru " + zfs.NiceBytes(arc["mru_size"]) + " · mfu " + zfs.NiceBytes(arc["mfu_size"]))
	l2 := ""
	if arc["l2_size"] > 0 {
		l2 = " · l2 " + zfs.NiceBytes(arc["l2_size"]) + " hit " + hitPct(arc["l2_hits"], arc["l2_misses"])
	}
	add("       " + styDim.Render("demand-data "+hitPct(arc["demand_data_hits"], arc["demand_data_misses"])+
		" · demand-meta "+hitPct(arc["demand_metadata_hits"], arc["demand_metadata_misses"])+
		" · prefetch "+hitPct(arc["prefetch_data_hits"], arc["prefetch_data_misses"])+l2))
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
	io := m.io[m.perf.pool]
	var reading string
	busyDirty := dirtyMax > 0 && sum.DirtyPeak > dirtyMax*delayPct/100
	switch {
	case delayRate > 0.5:
		reading = "write-throttled: dirty at the delay ceiling · see vdev latency"
	case busyDirty:
		reading = "near the throttle: dirty peaks above the delay line"
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

	// ── vdev latency ──────────────────────────────────────────────
	add(" " + styBold.Render(padR("vdev latency", 20)) + styDim.Render(
		padL("r-total", 9)+padL("w-total", 9)+padL("r-disk", 9)+padL("w-disk", 9)+
			padL("r-queue", 9)+padL("w-queue", 9)))
	for _, p := range m.pools {
		if p.Name != m.perf.pool {
			continue
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
				lat, ok := m.perf.lat[v.Name]
				cell := func(ns int64) string { return padL(zfs.NiceNS(ns), 9) }
				if !ok {
					lat = zfs.VdevLat{TotalR: -1, TotalW: -1, DiskR: -1, DiskW: -1,
						SyncQR: -1, SyncQW: -1, AsyncQR: -1, AsyncQW: -1}
				}
				add("   " + padR(truncate(prefix+v.Name, 18), 18) +
					cell(lat.TotalR) + cell(lat.TotalW) + cell(lat.DiskR) + cell(lat.DiskW) +
					cell(lat.QueueR()) + cell(lat.QueueW()))
			}
		}
	}
	add("")

	// ── zil ───────────────────────────────────────────────────────
	commitRate := m.perfRate(m.perf.zil, m.perf.zilPrev, "zil_commit_count")
	slogCnt := m.perf.zil["zil_itx_metaslab_slog_count"]
	normalCnt := m.perf.zil["zil_itx_metaslab_normal_count"]
	slogTxt := fmt.Sprintf("slog itx %s / normal %s", zfs.NiceCount(slogCnt), zfs.NiceCount(normalCnt))
	hasSlog := false
	for _, p := range m.pools {
		if p.Name == m.perf.pool && p.Class("logs") != nil {
			hasSlog = true
		}
	}
	if hasSlog && slogCnt == 0 {
		slogTxt += styWarn.Render(" (slog unused)")
	}
	add(" " + styBold.Render("zil") + fmt.Sprintf("   %.1f commits/s · %s total · ",
		commitRate, zfs.NiceCount(m.perf.zil["zil_commit_count"])) + slogTxt)
	add("")

	// ── top io (this pool's datasets, by current rate) ────────────
	type talker struct {
		name string
		r, w int64
	}
	var talkers []talker
	for name, io := range m.dsIO {
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
		if i == 5 {
			break
		}
		add("   " + padR(truncate(t.name, w-32), w-32) +
			" r " + padL(zfs.NiceBytes(t.r)+"/s", 8) + "  w " + padL(zfs.NiceBytes(t.w)+"/s", 8))
	}
	if m.perf.err != "" {
		add("")
		add(" " + styWarn.Render("collector: "+truncate(m.perf.err, w-14)))
	}
	if strings.HasPrefix(m.src.Name(), "replay") {
		add("")
		add(" " + styDim.Render("[replay] rates read as zero — counters are frozen"))
	}
	return clampLines(lines, h)
}
