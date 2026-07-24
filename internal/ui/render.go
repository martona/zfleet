package ui

import (
	"fmt"
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
	if len(m.pools) == 0 {
		if m.lastErr != nil {
			return "zfse: " + m.lastErr.Error()
		}
		return "collecting…"
	}

	leftW := 36
	if m.w < 100 {
		leftW = m.w * 4 / 10
	}
	rightW := m.w - leftW - 3
	contentH := m.h - 4

	sel := m.pools[m.selIdx()]
	left := leftPane(m, leftW, contentH)
	right := inspector(m, sel, rightW, contentH)

	var b strings.Builder
	title := func(t string, w int) string {
		seg := " " + t + " "
		if lipgloss.Width(seg) > w-2 {
			seg = " " + truncate(t, w-4) + " "
		}
		return "─" + seg + rep("─", w-1-lipgloss.Width(seg))
	}
	b.WriteString("┌" + title("pools", leftW) + "┬" + title(sel.Name, rightW) + "┐\n")
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
	b.WriteString("└" + rep("─", m.w-2) + "┘")
	return b.String()
}

func leftPane(m *Model, w, h int) []string {
	nameW := w - 25
	if nameW < 6 {
		nameW = 6
	}
	var lines []string
	for _, p := range m.pools {
		selected := p.Name == m.selName
		prefix := "  "
		if selected {
			prefix = "▸ "
		}
		name := healthStyle(p.State).Render(padR(truncate(p.Name, nameW), nameW))
		row := prefix + name + " " + bar(p.CapPct, 10) + " " +
			padL(pctStr(p.CapPct), 4) + " " + padL(zfs.NiceBytes(p.Size), 6)
		if selected {
			row = styBold.Render(row)
		}
		lines = append(lines, row)
	}
	return clampLines(lines, h)
}

func inspector(m *Model, p *zfs.Pool, w, h int) []string {
	var head []string

	errTxt := p.ErrorsLine
	if errTxt == "No known data errors" {
		errTxt = "no known data errors"
	}
	head = append(head, " "+healthStyle(p.State).Render(p.State)+styDim.Render(" · ")+errTxt)

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

	lines = append(lines, "")
	classLineCount := len(lines) - len(head)

	// row layout: " " + name(nameW) + sum(9) + " " + state(10) + 5 + 6 + 6
	nameW := w - 39
	if nameW > 42 {
		nameW = 42
	}
	if nameW < 12 {
		nameW = 12
	}
	var topo []string
	topo = append(topo, " "+padR("", nameW+9)+styDim.Render(padR("STATE", 10)+padL("READ", 5)+padL("WRITE", 6)+padL("CKSUM", 6)))

	anyCollapsed := false
	var walk func(v *zfs.Vdev, classPrefix string, depth int)
	walk = func(v *zfs.Vdev, classPrefix string, depth int) {
		expanded := len(v.Children) > 0 &&
			(m.expandAll || !v.Healthy() || len(v.Children) <= 3)
		display := classPrefix + v.Name
		sum := ""
		switch {
		case expanded:
			sum = "▾"
		case len(v.Children) > 0:
			leaves := v.Leaves()
			sum = fmt.Sprintf("%d× %s", len(leaves), zfs.NiceBytes(leaves[0].Size))
			anyCollapsed = true
		default:
			sum = zfs.NiceBytes(v.Size)
		}
		row := " " + rep("  ", depth) + padR(truncate(display, nameW-depth*2), nameW-depth*2) +
			padL(sum, 9) + " " +
			healthStyle(v.State).Render(padR(v.State, 10)) +
			counterCell(v.ReadErr, 5) + counterCell(v.WriteErr, 6) + counterCell(v.CksumErr, 6)
		if v.Note != "" {
			if room := w - lipgloss.Width(row) - 2; room >= 4 {
				row += " " + styWarn.Render(truncate(v.Note, room))
			}
		}
		topo = append(topo, row)
		if expanded {
			for _, c := range v.Children {
				walk(c, "", depth+1)
			}
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
	if anyCollapsed {
		topo = append(topo, " "+styDim.Render("(t: expand disks)"))
	} else if m.expandAll {
		topo = append(topo, " "+styDim.Render("(t: auto-collapse)"))
	}

	// The io line must survive short terminals: give topology whatever
	// height remains and elide its tail rather than the sections below it.
	tail := 2 // blank + io line
	budget := h - len(head) - classLineCount - tail
	if budget < 2 {
		budget = 2
	}
	if len(topo) > budget {
		hidden := len(topo) - (budget - 1)
		topo = append(topo[:budget-1],
			" "+styDim.Render(fmt.Sprintf("… %d more rows", hidden)))
	}
	lines = append(lines, topo...)

	lines = append(lines, "")
	if r, ok := m.io[p.Name]; ok {
		lines = append(lines, " io   r "+zfs.NiceBytes(r.RBw)+"/s · "+opsCell(r.RBw, r.ROps)+
			"    w "+zfs.NiceBytes(r.WBw)+"/s · "+opsCell(r.WBw, r.WOps))
	} else {
		lines = append(lines, " io   "+styDim.Render("sampling…"))
	}

	return clampLines(lines, h)
}

// opsCell renders an ops/s figure, refusing to claim "0 ops" when bytes
// moved: zpool iostat floors sub-1.0 ops/s rates to zero (a lone aggregated
// write in a sample window is the classic case), so bandwidth-with-zero-ops
// really means "less than one op per second".
func opsCell(bw, ops int64) string {
	if ops == 0 && bw > 0 {
		return "<1 ops"
	}
	return zfs.NiceCount(ops) + " ops"
}

func strip(m *Model) string {
	var segs []string

	if m.haveArc {
		seg := " arc " + zfs.NiceBytes(m.arc.Size) + "/" + zfs.NiceBytes(m.arc.CMax)
		dh := m.arc.Hits - m.arcPrev.Hits
		dm := m.arc.Misses - m.arcPrev.Misses
		if dh+dm <= 0 {
			dh, dm = m.arc.Hits, m.arc.Misses // no traffic in window: lifetime
		}
		if dh+dm > 0 {
			seg += fmt.Sprintf(" · hit %.1f%%", 100*float64(dh)/float64(dh+dm))
		}
		segs = append(segs, seg)
	} else {
		segs = append(segs, " arc -")
	}

	var rbw, wbw int64
	for _, r := range m.io {
		rbw += r.RBw
		wbw += r.WBw
	}
	segs = append(segs, "Σ r "+zfs.NiceBytes(rbw)+"/s w "+zfs.NiceBytes(wbw)+"/s")

	scans := []string{}
	for _, p := range m.pools {
		if p.Scan.State == zfs.ScanInProgress {
			scans = append(scans, fmt.Sprintf("%s %s %.0f%%", p.Scan.Kind, p.Name, p.Scan.Percent))
		}
	}
	if len(scans) > 0 {
		segs = append(segs, styBold.Render(strings.Join(scans, ", ")))
	} else {
		segs = append(segs, styDim.Render("no scans running"))
	}

	worst := m.pools[0]
	for _, p := range m.pools {
		if zfs.StateRank(p.State) > zfs.StateRank(worst.State) {
			worst = p
		}
	}
	if zfs.StateRank(worst.State) == 0 {
		segs = append(segs, styGood.Render(fmt.Sprintf("%d pools ONLINE", len(m.pools))))
	} else {
		segs = append(segs, healthStyle(worst.State).Render(worst.Name+" "+worst.State))
	}

	if strings.HasPrefix(m.src.Name(), "replay") {
		segs = append(segs, styDim.Render("[replay]"))
	}
	if m.lastErr != nil {
		segs = append(segs, styBad.Render("! "+truncate(m.lastErr.Error(), 30)))
	}

	return strings.Join(segs, styDim.Render(" │ "))
}

func counterCell(v string, w int) string {
	if v == "" {
		return padL("-", w)
	}
	cell := padL(v, w)
	if v != "0" {
		return styBad.Render(cell)
	}
	return cell
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
