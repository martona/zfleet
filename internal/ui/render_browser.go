package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/martona/zfs-explorer/internal/zfs"
)

func (m *Model) breadcrumb() string {
	c := m.brContainer()
	if c == nil {
		return m.br.pool
	}
	crumb := strings.Join(strings.Split(c.Name, "/"), " ▸ ")
	if m.br.filterIn || m.br.filter != "" {
		crumb += "  /" + m.br.filter
		if m.br.filterIn {
			crumb += "▏"
		}
	}
	return crumb
}

func brLeftPane(m *Model, w, h int) []string {
	c := m.brContainer()
	if c == nil {
		return []string{" " + styDim.Render("collecting datasets…")}
	}
	entries := m.brEntries()
	cur := m.brCursorIdx(entries)

	// child rows: "▸   name/......... ▓▓░░░ 37.8T"
	nameW := w - 2 - 2 - 1 - 5 - 1 - 6 - 1
	if nameW < 10 {
		nameW = 10
	}
	var lines []string
	for i, e := range entries {
		marker := "  "
		if i == cur {
			marker = "▸ "
		}
		var row string
		switch e.kind {
		case eSelf:
			row = marker + "· " + styBold.Render(padR(truncate(e.ds.Base(), nameW+4), nameW+4)) +
				" " + padL(zfs.NiceBytes(e.ds.Used), 7)
		case eChild:
			name := e.ds.Base()
			if len(e.ds.Children) > 0 {
				name += "/"
			} else if e.ds.IsVolume() {
				name = truncate(name, nameW-2) + " " + styDim.Render("v")
			}
			share := int64(-1)
			if c.Used > 0 {
				share = e.ds.Used * 100 / c.Used
			}
			row = marker + "  " + padR(truncate(name, nameW), nameW) +
				" " + bar(share, 5) + " " + padL(zfs.NiceBytes(e.ds.Used), 6)
		case eFam:
			label := fmt.Sprintf("@%s (%d)", e.fam.Label(), len(e.fam.Snaps))
			row = marker + padR(truncate(label, nameW+5), nameW+5) +
				" " + padL(zfs.NiceBytes(e.fam.Used()), 6)
		case eSnap:
			row = marker + padR(truncate("@"+e.snap.Snap, nameW+5), nameW+5) +
				" " + padL(zfs.NiceBytes(e.snap.Used), 6)
			row = styDim.Render(row)
		}
		if i == cur {
			row = styBold.Render(row)
		}
		lines = append(lines, row)
	}

	// keep the cursor visible on long levels
	if len(lines) > h && cur >= h-1 {
		start := cur - h + 2
		lines = append(lines[start:], " "+styDim.Render(fmt.Sprintf("… (%d above)", start)))
		copy(lines[1:], lines[:len(lines)-1])
		lines[0] = " " + styDim.Render(fmt.Sprintf("… (%d above)", start))
		lines = lines[:h]
	}
	return clampLines(lines, h)
}

func brRightTitle(sel brEntry) string {
	switch sel.kind {
	case eSelf, eChild:
		return sel.ds.Base()
	case eFam:
		return "@" + sel.fam.Label()
	case eSnap:
		return "@" + sel.snap.Snap
	}
	return ""
}

func brInspector(m *Model, sel brEntry, w, h int) []string {
	switch sel.kind {
	case eSelf, eChild:
		return clampLines(dsInspector(m, sel.ds, w), h)
	case eFam:
		return clampLines(famInspector(sel.fam), h)
	case eSnap:
		return clampLines(snapInspector(m, sel.snap), h)
	}
	return nil
}

func dsInspector(m *Model, d *zfs.Dataset, w int) []string {
	var lines []string

	// type · encryption (the mount story gets its own line below)
	parts := []string{d.Type}
	if d.IsVolume() {
		if d.Origin != "-" && d.Origin != "" {
			parts = append(parts, "clone of "+truncate(d.Origin, w-20))
		}
	}
	if d.Encryption != "off" && d.Encryption != "-" && d.Encryption != "" {
		if d.Locked() {
			parts = append(parts, styWarn.Render("locked ")+styDim.Render(d.Encryption))
		} else {
			parts = append(parts, styDim.Render("unlocked "+d.Encryption))
		}
	}
	lines = append(lines, " "+strings.Join(parts, styDim.Render(" · ")))

	if d.IsVolume() {
		vparts := []string{"volsize " + zfs.NiceBytes(d.Volsize)}
		if d.RefReserv <= 0 {
			vparts = append(vparts, "sparse")
		} else {
			vparts = append(vparts, "refreserv "+zfs.NiceBytes(d.RefReserv))
		}
		vparts = append(vparts, "volblocksize "+zfs.NiceBytes(d.Volblocksize))
		lines = append(lines, " "+strings.Join(vparts, " · "))
	} else {
		if d.Origin != "-" && d.Origin != "" {
			lines = append(lines, " clone of "+truncate(d.Origin, w-12))
		}
		// the whole mount story on one line: state, path, source.
		// green only for a path that is actually mounted.
		var mp string
		switch {
		case d.Mounted == "yes":
			mp = " mounted " + styGood.Render(truncate(d.Mountpoint, w-18))
		case d.Canmount == "off":
			mp = " " + styDim.Render("canmount: off "+truncate(d.Mountpoint, w-24))
		default:
			mp = " " + styDim.Render("not mounted "+truncate(d.Mountpoint, w-22))
		}
		if p, ok := m.br.props[d.Name]; ok {
			switch src := p["mountpoint"].Source; {
			case src == "received":
				mp += styWarn.Render(" ·recv")
			case src == "local":
				mp += styDim.Render(" ·local")
			case strings.HasPrefix(src, "inherited"):
				mp += styDim.Render(" ·inh")
			}
		}
		lines = append(lines, mp)
	}
	lines = append(lines, "")

	// space composition, scaled to used
	lines = append(lines, " used "+zfs.NiceBytes(d.Used)+" · avail "+zfs.NiceBytes(d.Avail)+
		" · refer "+zfs.NiceBytes(d.Refer))

	// logical vs charged, all on one basis. The effective-compression line
	// appears when padding quantization eats the claimed ratio — on raidz,
	// blocks pad to (parity+1)-sector multiples, so compressing a 16K block
	// to 10K often allocates exactly the same 8 sectors.
	if d.LogicalUsed > 0 && d.Used > 0 {
		vw, par, ash, geo := m.poolGeometry(m.br.pool)
		volGeo := geo && d.IsVolume() && d.Volblocksize > 0

		vsLogical := (float64(d.Used)/float64(d.LogicalUsed) - 1) * 100
		line := fmt.Sprintf(" logical %s → charged %s · %+.0f%% vs logical",
			zfs.NiceBytes(d.LogicalUsed), zfs.NiceBytes(d.Used), vsLogical)
		if volGeo {
			expected := (zfs.RaidzChargedInflation(d.Volblocksize, vw, par, ash) - 1) * 100
			line += styDim.Render(fmt.Sprintf(" (expected %+.0f%% @%s)",
				expected, zfs.NiceBytes(d.Volblocksize)))
		}
		lines = append(lines, line)

		claimed, err := strconv.ParseFloat(d.Compressratio, 64)
		if err == nil && claimed > 0 && d.Compression != "off" && d.Compression != "-" {
			if volGeo {
				// judge compression against what this zvol would charge
				// uncompressed — on raidz that baseline is logical ×
				// expected inflation, not logical itself
				uncomp := float64(d.LogicalUsed) *
					zfs.RaidzChargedInflation(d.Volblocksize, vw, par, ash)
				saves := (1 - float64(d.Used)/uncomp) * 100
				switch {
				case saves >= 1:
					lines = append(lines, fmt.Sprintf(" compress %s %s× · saves %.0f%% vs uncompressed %s",
						d.Compression, d.Compressratio, saves, zfs.NiceBytes(int64(uncomp))))
				case claimed >= 1.05:
					lines = append(lines, fmt.Sprintf(" compress %s %s× · no net pool saving @%s blocks",
						d.Compression, d.Compressratio, zfs.NiceBytes(d.Volblocksize)))
				}
			} else {
				// no counterfactual without known block sizes; report
				// charged-vs-logical honestly
				effective := float64(d.LogicalUsed) / float64(d.Used)
				if claimed-effective > 0.1 || effective < 0.95 {
					lines = append(lines, fmt.Sprintf(" compress %s · %s× claimed → %.2f× effective",
						d.Compression, d.Compressratio, effective))
				}
			}
		}
	}
	barW := w - 26
	if barW > 30 {
		barW = 30
	}
	if barW < 8 {
		barW = 8
	}
	comp := func(label string, v int64) string {
		fill := 0
		if d.Used > 0 && v > 0 {
			fill = int(v * int64(barW) / d.Used)
			if fill == 0 {
				fill = 1
			}
		}
		b := styBar.Render(rep("▓", fill)) + styDim.Render(rep("░", barW-fill))
		return " " + padR(label, 10) + padL(zfs.NiceBytes(v), 6) + " " + b
	}
	lines = append(lines,
		comp("dataset", d.UsedDS),
		comp("snapshots", d.UsedSnap),
		comp("children", d.UsedChild),
		comp("refreserv", d.UsedRefReserv),
		"")

	// snapshots summary from lazy fetch. "pinning" splits usedbysnapshots
	// into per-snapshot-unique vs collectively-held space — when shared
	// dwarfs unique, only a range destroy gets the space back.
	if snaps, ok := m.br.snaps[d.Name]; ok {
		if len(snaps) == 0 {
			lines = append(lines, " "+styDim.Render("no snapshots"))
		} else {
			var uniq int64
			for _, s := range snaps {
				uniq += s.Used
			}
			shared := d.UsedSnap - uniq
			if shared < 0 {
				shared = 0
			}
			lines = append(lines, fmt.Sprintf(" snaps %d · pinning %s · newest %s",
				len(snaps), zfs.NiceBytes(d.UsedSnap), relAge(snaps[len(snaps)-1].Creation)))
			if d.UsedSnap > 0 {
				lines = append(lines, "   "+styDim.Render(zfs.NiceBytes(uniq)+" unique · "+
					zfs.NiceBytes(shared)+" shared across snapshots"))
			}
		}
	} else {
		lines = append(lines, " "+styDim.Render("snaps …"))
	}
	lines = append(lines, "")

	// key properties, with source tags once `zfs get` lands
	props := m.br.props[d.Name]
	tag := func(name string) string {
		p, ok := props[name]
		if !ok {
			return ""
		}
		switch {
		case p.Source == "local":
			return styDim.Render(" ·local")
		case p.Source == "received":
			return styWarn.Render(" ·recv")
		case strings.HasPrefix(p.Source, "inherited"):
			return styDim.Render(" ·inh")
		}
		return ""
	}
	if d.IsVolume() {
		lines = append(lines, " compress "+d.Compression+tag("compression")+
			"  "+d.Compressratio+"x  sync "+d.Sync+tag("sync"))
	} else {
		lines = append(lines, " recordsize "+zfs.NiceBytes(d.Recordsize)+tag("recordsize")+
			"  compress "+d.Compression+tag("compression")+
			"  "+d.Compressratio+"x")
		lines = append(lines, " atime "+d.Atime+tag("atime")+
			"  sync "+d.Sync+tag("sync"))
	}
	var lims []string
	if d.Quota > 0 {
		lims = append(lims, "quota "+zfs.NiceBytes(d.Quota))
	}
	if d.RefQuota > 0 {
		lims = append(lims, "refquota "+zfs.NiceBytes(d.RefQuota))
	}
	if d.Reservation > 0 {
		lims = append(lims, "reservation "+zfs.NiceBytes(d.Reservation))
	}
	if len(lims) > 0 {
		lines = append(lines, " "+strings.Join(lims, " · "))
	}
	lines = append(lines, " "+styDim.Render("created "+absDate(d.Creation)))

	// live io from objset kstat deltas. Entries exist only for loaded
	// datasets (mounted fs, active zvols) — when the container itself has
	// no objset, the subtree sum IS the answer and takes the main line.
	lines = append(lines, "")
	own, ownOK := m.dsIO[d.Name]
	sub, subRH, subWH, loaded := m.subtreeIO(d)
	ioLine := func(r zfs.IORates, rh, wh []int64, tag string) string {
		return " io   r " + padL(zfs.NiceBytes(r.RBw)+"/s", 8) + " " + sparkline(rh, 10) +
			"   w " + padL(zfs.NiceBytes(r.WBw)+"/s", 8) + " " + sparkline(wh, 10) + tag
	}
	switch {
	case ownOK:
		hist := m.dsIOHist[d.Name]
		rh := make([]int64, len(hist))
		wh := make([]int64, len(hist))
		for i, s := range hist {
			rh[i] = s.RBw
			wh[i] = s.WBw
		}
		lines = append(lines, ioLine(own, rh, wh, ""))
		if loaded > 1 { // other loaded datasets beneath this one
			lines = append(lines, " tree "+styDim.Render("r ")+padL(zfs.NiceBytes(sub.RBw)+"/s", 8)+
				strings.Repeat(" ", 12)+styDim.Render("w ")+padL(zfs.NiceBytes(sub.WBw)+"/s", 8))
		}
	case loaded > 0:
		lines = append(lines, ioLine(sub, subRH, subWH, styDim.Render(" ·tree")))
	case m.objsetPrev != nil:
		reason := "dataset not loaded"
		if len(d.Children) > 0 {
			reason = "nothing loaded in subtree"
		}
		lines = append(lines, " io   "+styDim.Render("no stats ("+reason+")"))
	}
	return lines
}

// subtreeIO sums current rates and tail-aligned history over d and all its
// descendants that have loaded objsets.
func (m *Model) subtreeIO(d *zfs.Dataset) (cur zfs.IORates, rh, wh []int64, loaded int) {
	var rings [][]zfs.IORates
	maxLen := 0
	var walk func(x *zfs.Dataset)
	walk = func(x *zfs.Dataset) {
		if r, ok := m.dsIO[x.Name]; ok {
			cur.RBw += r.RBw
			cur.WBw += r.WBw
			cur.ROps += r.ROps
			cur.WOps += r.WOps
			loaded++
			ring := m.dsIOHist[x.Name]
			rings = append(rings, ring)
			if len(ring) > maxLen {
				maxLen = len(ring)
			}
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	walk(d)
	rh = make([]int64, maxLen)
	wh = make([]int64, maxLen)
	for _, ring := range rings {
		off := maxLen - len(ring)
		for i, s := range ring {
			rh[off+i] += s.RBw
			wh[off+i] += s.WBw
		}
	}
	return cur, rh, wh, loaded
}

func snapInspector(m *Model, s *zfs.Snapshot) []string {
	container := s.Name[:strings.IndexByte(s.Name, '@')]
	return []string{
		" @" + s.Snap,
		" " + styDim.Render("of "+truncate(container, 60)),
		"",
		" used " + zfs.NiceBytes(s.Used) + styDim.Render(" (unique to this snapshot)"),
		" refer " + zfs.NiceBytes(s.Refer),
		" created " + absDate(s.Creation) + " (" + relAge(s.Creation) + ")",
	}
}

func famInspector(f *zfs.SnapFamily) []string {
	lines := []string{
		fmt.Sprintf(" @%s · %d snapshots", f.Label(), len(f.Snaps)),
		" " + styDim.Render(absDate(f.Oldest().Creation)+" → "+absDate(f.Newest().Creation)),
		"",
		" Σ used " + zfs.NiceBytes(f.Used()) +
			styDim.Render(" — sum of unique sizes; true reclaim needs a dry-run destroy"),
		"",
	}
	show := f.Snaps
	if len(show) > 8 {
		show = show[len(show)-8:]
		lines = append(lines, " "+styDim.Render(fmt.Sprintf("… %d older", len(f.Snaps)-8)))
	}
	for _, s := range show {
		lines = append(lines, " "+styDim.Render("@"+s.Snap+"  "+zfs.NiceBytes(s.Used)))
	}
	return lines
}

func absDate(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	return time.Unix(unix, 0).Format("Jan 2 2006")
}

func relAge(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	d := time.Since(time.Unix(unix, 0))
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 90*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dmo", int(d.Hours()/(24*30)))
	}
}
