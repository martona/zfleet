package ui

import (
	"fmt"
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
		lines = append(lines, " "+from+" · "+styWarn.Render(hostOutage(h)))
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

	// vitals
	var vit []string
	if h.uptimeSec > 0 {
		vit = append(vit, "up "+zfs.NiceUptime(h.uptimeSec))
	}
	if h.load1 != "" {
		vit = append(vit, "load "+h.load1)
	}
	if h.cpuPct >= 0 {
		vit = append(vit, fmt.Sprintf("cpu %d%%", h.cpuPct))
	}
	if h.haveTemp {
		vit = append(vit, fmt.Sprintf("%s %d°C", h.tempSrc, h.tempC))
	}
	if len(vit) > 0 {
		line := " "
		for i, v := range vit {
			if i > 0 {
				line += styDim.Render(" · ")
			}
			line += v
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")

	// arc + aggregate io
	if h.haveArc {
		hit := ""
		if dh, dm := h.arc.Hits-h.arcPrev.Hits, h.arc.Misses-h.arcPrev.Misses; dh+dm > 0 {
			hit = fmt.Sprintf(" · hit %.1f%% ", 100*float64(dh)/float64(dh+dm)) + sparkline(h.hitHist, 8)
		}
		lines = append(lines, " arc  "+zfs.NiceBytes(h.arc.Size)+" / "+zfs.NiceBytes(h.arc.CMax)+hit)
	}
	if len(h.hostIOHist) > 0 {
		r, wr := hostLiveIO(h)
		rh, wh := hostIORings(h)
		lines = append(lines, " io   r "+padL(zfs.NiceBytes(r)+"/s", 8)+" "+sparkline(rh, 10)+
			"   w "+padL(zfs.NiceBytes(wr)+"/s", 8)+" "+sparkline(wh, 10))
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
