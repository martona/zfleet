package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// Host rows and the host inspector. A host is an organizational anchor —
// always open in the tree — so its own row carries what the pools below it
// cannot: machine vitals and the aggregate io of everything it serves.

// uptimeCoarse is the row-width form ("up 3d"); the inspector shows the
// two-unit NiceUptime.
func uptimeCoarse(sec int64) string {
	switch {
	case sec <= 0:
		return ""
	case sec >= 86400:
		return fmt.Sprintf("up %dd", sec/86400)
	case sec >= 3600:
		return fmt.Sprintf("up %dh", sec/3600)
	default:
		return fmt.Sprintf("up %dm", sec/60)
	}
}

// hostVitalsRow renders the vitals cell for tree rows: full variant
// "up 3d · cpu 2% · 45°C" or, when tight, "up 3d · 45°C".
func hostVitalsRow(h *hostState, full bool) string {
	var parts []string
	if up := uptimeCoarse(h.uptimeSec); up != "" {
		parts = append(parts, up)
	}
	if full && h.cpuPct >= 0 {
		parts = append(parts, fmt.Sprintf("cpu %d%%", h.cpuPct))
	}
	if h.haveTemp {
		parts = append(parts, fmt.Sprintf("%d°C", h.tempC))
	}
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}

// hostOutage renders the down-state cell: how long dark, and when the next
// attempt is due.
func hostOutage(h *hostState) string {
	age := h.outageAge()
	txt := "unreachable"
	if age > 0 {
		txt += " " + niceAge(age)
	}
	if wait := time.Until(h.nextTry); wait > time.Second {
		txt += fmt.Sprintf(" · retry %ds", int(wait.Seconds()))
	}
	return txt
}

// hostOutageCompact is the row-width outage form — terse enough to always
// fit the shared cells, while hostOutage keeps the full phrasing for the
// inspector and strip.
func hostOutageCompact(h *hostState) string {
	txt := "down"
	if age := h.outageAge(); age > 0 {
		txt += " " + niceAge(age)
	}
	if wait := time.Until(h.nextTry); wait > time.Second {
		txt += fmt.Sprintf(" · retry %ds", int(wait.Seconds()))
	}
	return txt
}

func niceAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// ioRate renders one right-aligned rate readout: idle recedes wholesale,
// motion keeps bright digits over a dim unit.
func ioRate(rate int64, w int) string {
	cell := padL(zfs.NiceBytes(rate)+"/s", w)
	if rate == 0 {
		return styDim.Render(cell)
	}
	return dimUnit(cell)
}

// hostLiveIO sums the host's current pool rates.
func hostLiveIO(h *hostState) (r, w int64) {
	for _, io := range h.io {
		r += io.RBw
		w += io.WBw
	}
	return r, w
}

// hostIORings splits the aggregate ring into read/write series.
func hostIORings(h *hostState) (rh, wh []int64) {
	rh = make([]int64, len(h.hostIOHist))
	wh = make([]int64, len(h.hostIOHist))
	for i, s := range h.hostIOHist {
		rh[i] = s.RBw
		wh[i] = s.WBw
	}
	return rh, wh
}

func hostInspector(m *Model, h *hostState, w int) []string {
	var lines []string

	// connection line: where this data comes from and how fresh it is
	from := "local"
	if h.dest != "" {
		from = h.dest + styDim.Render(" · ssh")
	}
	switch h.conn {
	case connDown:
		lines = append(lines, " "+from+" · "+styBad.Render(hostOutage(h)))
		if !h.lastOK.IsZero() {
			lines = append(lines, " "+styDim.Render("last data "+niceAge(time.Since(h.lastOK))+" ago"))
		} else {
			lines = append(lines, " "+styDim.Render("never reached"))
		}
		if h.errText != "" {
			for _, ln := range wrap(h.errText, w-3) {
				lines = append(lines, " "+styDim.Render(ln))
			}
		}
	case connLive:
		live := styGood.Render("live")
		if age := time.Since(h.lastOK); age > 2*statsInterval+time.Second {
			live = styWarn.Render("live · " + niceAge(age) + " ago")
		}
		if h.sudoOK {
			live += styDim.Render(" · sudo")
		}
		lines = append(lines, " "+from+" · "+live)
	default:
		lines = append(lines, " "+from+" · "+styDim.Render("connecting…"))
	}

	// identity: os · kernel, zfs userland + kmod
	if h.haveInfo {
		id := h.osName
		if h.kernel != "" {
			if id != "" {
				id += " · "
			}
			id += h.kernel
		}
		if id != "" {
			lines = append(lines, " "+styDim.Render(id))
		}
		if h.zfsVer != "" {
			zline := " zfs " + h.zfsVer
			if h.zfsKmod != "" && h.zfsKmod != h.zfsVer {
				zline += styDim.Render(" (kmod " + h.zfsKmod + ")")
			}
			lines = append(lines, zline)
		}
	}

	// vitals: labels and units muted, numbers bright — unless the number
	// is zero, in which case the whole vital recedes
	var vit []string
	if h.uptimeSec > 0 {
		vit = append(vit, styDim.Render("up ")+dimLabels(zfs.NiceUptime(h.uptimeSec)))
	}
	if h.load1 != "" {
		seg := styDim.Render("load " + h.load1)
		if f, err := strconv.ParseFloat(h.load1, 64); err == nil && f > 0 {
			seg = styDim.Render("load ") + h.load1
		}
		vit = append(vit, seg)
	}
	if h.cpuPct == 0 {
		vit = append(vit, styDim.Render("cpu 0%"))
	} else if h.cpuPct > 0 {
		vit = append(vit, styDim.Render("cpu ")+fmt.Sprintf("%d", h.cpuPct)+styDim.Render("%"))
	}
	if h.haveTemp {
		// "cpu 2% · cpu 45°C" stutters — the label earns its place only
		// when the hottest sensor is NOT the cpu
		label := ""
		if h.tempSrc != "cpu" {
			label = h.tempSrc + " "
		}
		vit = append(vit, styDim.Render(label)+fmt.Sprintf("%d", h.tempC)+styDim.Render("°C"))
	}
	if len(vit) > 0 {
		lines = append(lines, " "+strings.Join(vit, styDim.Render(" · ")))
	}
	lines = append(lines, "")

	// arc, hit ratio, and aggregate io share one label column so their
	// sparklines sit on the same x, all showing the same window
	sw := w - 19
	if sw > dsIOHistLen/2 {
		sw = dsIOHistLen / 2
	}
	if h.haveArc {
		lines = append(lines, " "+styBold.Render("arc")+"    "+
			dimUnit(zfs.NiceBytes(h.arc.Size))+styDim.Render(" / ")+dimUnit(zfs.NiceBytes(h.arc.CMax)))
		hit := "-"
		if dh, dm := h.arc.Hits-h.arcPrev.Hits, h.arc.Misses-h.arcPrev.Misses; dh+dm > 0 {
			hit = fmt.Sprintf("%.1f%%", 100*float64(dh)/float64(dh+dm))
		}
		lines = append(lines, " hit    "+dimUnit(padL(hit, 7))+"  "+sparklineFam(sparkSteel, h.hitHist, sw))
	}
	if len(h.hostIOHist) > 0 {
		r, wr := hostLiveIO(h)
		rh, wh := hostIORings(h)
		lines = append(lines,
			" "+styBold.Render("io")+"   r "+ioRate(r, 7)+"  "+sparklineFam(sparkSteel, rh, sw),
			"      w "+ioRate(wr, 7)+"  "+sparklineFam(sparkGold, wh, sw))
	}
	lines = append(lines, "")

	// drives: every disk the machine carries — temps, lifetime traffic and
	// the health verdict where smartctl reaches. Columns hug their longest
	// occupants.
	head := " " + styBold.Render("drives")
	if h.sudoProbed && !h.sudoOK {
		head += " " + styDim.Render("(no passwordless sudo — smart data unavailable)")
	}
	lines = append(lines, head)
	if len(h.disks) == 0 {
		lines = append(lines, "  "+styDim.Render("none detected"))
	}
	nodeW, modelW := 6, 8
	for _, d := range h.disks {
		if n := len(d.Node); n > nodeW {
			nodeW = n
		}
		if n := len(d.Model); n > modelW {
			modelW = n
		}
	}
	rw := func(v int64) string {
		if v < 0 {
			return styDim.Render(padL("-", 7))
		}
		return dimUnit(padL(zfs.NiceBytes(v), 7))
	}
	for _, d := range h.disks {
		temp := styDim.Render(padL("-", 5))
		if d.TempC >= 0 {
			temp = dimUnit(padL(fmt.Sprintf("%d°C", d.TempC), 5))
		}
		media := "ssd"
		switch {
		case d.Rota:
			media = "hdd"
		case strings.HasPrefix(d.Node, "nvme"):
			media = "nvme"
		}
		model := d.Model
		if model == "" {
			model = "?"
		}
		row := "  " + padR(d.Node, nodeW+2) + padR(model, modelW+1) +
			dimUnit(padL(zfs.NiceBytes(d.Size), 7)) + temp
		if s, ok := h.smart[d.Node]; ok {
			row += styDim.Render("  r") + rw(s.ReadBytes) + styDim.Render(" w") + rw(s.WriteBytes)
			verdict := "  " + styDim.Render("ok")
			switch {
			case s.Standby:
				verdict = "  " + styDim.Render("zzz")
			case smartSev(s) == sevErr:
				verdict = "  " + styBad.Render("FAIL")
			case smartSev(s) == sevWarn:
				verdict = "  " + styWarn.Render("WARN")
			}
			row += verdict
		}
		lines = append(lines, row+" "+styDim.Render(media))
	}
	lines = append(lines, "")

	// sensors: every chip with a temperature — anything eligible for the
	// host line is a named row here; drive-linked chips live above instead
	lines = append(lines, " "+styBold.Render("sensors"))
	type sensorRow struct {
		name, label string
		milli       int64
	}
	var srows []sensorRow
	for _, c := range h.chips {
		if _, isDrive := h.chipDisk[c.Dir]; isDrive {
			continue
		}
		name := c.Name
		if zfs.IsCPUChip(c.Name) {
			name = "cpu"
		}
		var rows []zfs.HwmonTemp
		for _, t := range c.Temps {
			if strings.HasPrefix(t.Label, "Package") {
				rows = append(rows, t)
			}
		}
		if len(rows) == 0 {
			best := c.Temps[0]
			for _, t := range c.Temps {
				if t.MilliC > best.MilliC {
					best = t
				}
			}
			rows = []zfs.HwmonTemp{best}
		}
		for _, t := range rows {
			srows = append(srows, sensorRow{name, t.Label, t.MilliC})
		}
	}
	if len(srows) == 0 {
		lines = append(lines, "  "+styDim.Render("none"))
	}
	snameW, slabelW := 6, 0
	for _, r := range srows {
		if n := len(r.name); n > snameW {
			snameW = n
		}
		if n := len(r.label); n > slabelW {
			slabelW = n
		}
	}
	for _, r := range srows {
		lines = append(lines, "  "+padR(r.name, snameW+2)+styDim.Render(padR(r.label, slabelW+2))+
			dimUnit(padL(fmt.Sprintf("%d°C", int((r.milli+500)/1000)), 5)))
	}
	lines = append(lines, "")

	// pools mini-list: the tree below has the detail; this is the roster
	lines = append(lines, " "+styBold.Render("pools"))
	if len(h.pools) == 0 {
		lines = append(lines, "  "+styDim.Render("none"))
	}
	for _, p := range h.pools {
		lines = append(lines, "  "+padR(truncate(p.Name, 12), 12)+
			healthStyle(p.State).Render(padR(p.State, 10))+
			bar(p.CapPct, 8)+" "+padL(pctStr(p.CapPct), 4)+
			styDim.Render(" of ")+zfs.NiceBytes(p.Size))
	}
	return lines
}
