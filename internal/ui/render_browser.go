package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/martona/zfs-explorer/internal/zfs"
)

func (m *Model) breadcrumb() string {
	var crumb string
	switch c := m.brContainer(); {
	case m.brAtHostLevel():
		crumb = m.br.host.name
	case c == nil:
		crumb = m.brQualify(m.br.pool)
	default:
		segs := strings.Split(c.Name, "/")
		segs[0] = m.brQualify(segs[0])
		crumb = strings.Join(segs, " ▸ ")
	}
	if m.br.filterIn || m.br.filter != "" {
		crumb += "  /" + m.br.filter
		if m.br.filterIn {
			crumb += "▏"
		}
	}
	if n := len(m.br.selSnaps); n > 0 {
		crumb += fmt.Sprintf(" · %d sel", n)
	}
	return crumb
}

// brQualify prefixes a pool name with its host, scp-style, once there is
// more than one host to tell apart.
func (m *Model) brQualify(pool string) string {
	if m.multiHost && m.br.host != nil {
		return m.br.host.name + ":" + pool
	}
	return pool
}

func brLeftPane(m *Model, w, h int) []string {
	if m.brAtHostLevel() {
		return brHostLevelPane(m, w, h)
	}
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
		onCur := i == cur
		var row string
		switch e.kind {
		case eSelf:
			// leftmost row of the level: the container out-dents its own
			// snapshots and children, never the other way around
			name := e.ds.Base()
			if e.ds.IsVolume() {
				name += " v"
			}
			body := padR(truncate(name, nameW+6), nameW+6) + " " + padL(zfs.NiceBytes(e.ds.Used), 7)
			if onCur {
				row = styInv.Render(padR("▸ "+body, w))
			} else {
				row = "  " + styBold.Render(body)
			}
		case eChild:
			name := e.ds.Base()
			if len(e.ds.Children) > 0 {
				name += "/"
			} else if e.ds.IsVolume() {
				name = truncate(name, nameW-2) + " v"
			}
			share := int64(-1)
			if c.Used > 0 {
				share = e.ds.Used * 100 / c.Used
			}
			left := "    " + padR(truncate(name, nameW), nameW) + " "
			right := " " + padL(zfs.NiceBytes(e.ds.Used), 6)
			if onCur {
				left = styInv.Render("▸   " + padR(truncate(name, nameW), nameW) + " ")
				row = left + bar(share, 5) + styInv.Render(padR(right, w-nameW-11))
			} else {
				row = left + bar(share, 5) + right
			}
		case eFam:
			all := len(e.fam.Snaps) > 0
			for _, s := range e.fam.Snaps {
				if !m.br.selSnaps[s.Snap] {
					all = false
					break
				}
			}
			selMark := " "
			if all {
				selMark = "*"
			}
			body := selMark + padR(truncate("@"+e.fam.Label()+fmt.Sprintf(" (%d)", len(e.fam.Snaps)), nameW+5), nameW+5) +
				" " + padL(zfs.NiceBytes(e.fam.Used()), 6)
			switch {
			case onCur:
				row = styInv.Render(padR("▸"+body, w))
			case all:
				row = " " + styWarn.Render(body)
			default:
				row = " " + body
			}
		case eSnap:
			selMark := " "
			if m.br.selSnaps[e.snap.Snap] {
				selMark = "*"
			}
			indent := ""
			if e.member {
				indent = " "
			}
			body := selMark + indent + padR(truncate("@"+e.snap.Snap, nameW+5-len(indent)), nameW+5-len(indent)) +
				" " + padL(zfs.NiceBytes(e.snap.Used), 6)
			switch {
			case onCur:
				row = styInv.Render(padR("▸"+body, w))
			case selMark == "*":
				row = " " + styWarn.Render(body)
			default:
				row = " " + styDim.Render(body)
			}
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

// brHostLevelPane renders the pools-of-a-host level: the host as its own
// leftmost self row, pools as children with share-of-allocated bars.
func brHostLevelPane(m *Model, w, h int) []string {
	host := m.br.host
	entries := m.brEntries()
	cur := m.brCursorIdx(entries)

	var total int64
	for _, p := range host.pools {
		if p.Alloc > 0 {
			total += p.Alloc
		}
	}
	nameW := w - 2 - 2 - 1 - 5 - 1 - 6 - 1
	if nameW < 10 {
		nameW = 10
	}
	var lines []string
	for i, e := range entries {
		onCur := i == cur
		var row string
		switch e.kind {
		case eHostSelf:
			body := padR(truncate(host.name, nameW+6), nameW+6) + " " + padL(zfs.NiceBytes(total), 7)
			if onCur {
				row = styInv.Render(padR("▸ "+body, w))
			} else {
				row = "  " + styBold.Render(body)
			}
		case ePool:
			share := int64(-1)
			if total > 0 && e.pool.Alloc >= 0 {
				share = e.pool.Alloc * 100 / total
			}
			name := e.pool.Name + "/"
			left := "    " + padR(truncate(name, nameW), nameW) + " "
			right := " " + padL(zfs.NiceBytes(e.pool.Alloc), 6)
			if onCur {
				left = styInv.Render("▸   " + padR(truncate(name, nameW), nameW) + " ")
				row = left + bar(share, 5) + styInv.Render(padR(right, w-nameW-11))
			} else {
				row = left + bar(share, 5) + right
			}
		}
		lines = append(lines, row)
	}
	return clampLines(lines, h)
}

func brRightTitle(m *Model, sel brEntry) string {
	switch sel.kind {
	case eSelf, eChild:
		return sel.ds.Base()
	case eFam:
		return "@" + sel.fam.Label()
	case eSnap:
		return "@" + sel.snap.Snap
	case eHostSelf:
		return m.br.host.name
	case ePool:
		return sel.pool.Name
	}
	return ""
}

func brInspector(m *Model, sel brEntry, w int) []string {
	if len(m.br.selSnaps) > 0 {
		return selInspector(m, w)
	}
	switch sel.kind {
	case eSelf, eChild:
		return dsInspector(m, m.br.host, sel.ds, w)
	case eFam:
		return famInspector(m, sel.fam, w)
	case eSnap:
		return snapInspector(m, sel.snap)
	case eHostSelf:
		return hostInspector(m, m.br.host, w)
	case ePool:
		return inspector(m, m.br.host, sel.pool, w)
	}
	return nil
}

// reclaimLines renders the dry-run verdict for a target: the verbatim
// "would reclaim" line once known, the Σ lower bound only until then.
func (m *Model) reclaimLines(target string, snaps []*zfs.Snapshot, w int) []string {
	var sum int64
	for _, s := range snaps {
		sum += s.Used
	}
	r := m.br.host.dryCache[target]
	switch {
	case r != nil && r.errText != "":
		return []string{
			" Σ used " + zfs.NiceBytes(sum) + styDim.Render(" — lower bound"),
			" " + styDim.Render("dry-run: "+truncate(r.errText, w-12)),
		}
	case r != nil && !r.pending && r.text != "":
		reclaim := ""
		for _, l := range strings.Split(r.text, "\n") {
			if strings.Contains(l, "would reclaim") {
				reclaim = strings.TrimSpace(l)
			}
		}
		if reclaim == "" {
			reclaim = "would reclaim: (no output)"
		}
		return []string{" " + styGood.Render(reclaim)}
	default:
		return []string{
			" Σ used " + zfs.NiceBytes(sum) + styDim.Render(" — lower bound"),
			" " + styDim.Render("computing true reclaim…"),
		}
	}
}

// snapTable is the shared member ledger — name, used, wrote — one view for
// collapsed families and hand-picked selections alike. The wrote column
// only exists when the capture carries it.
func snapTable(snaps []*zfs.Snapshot, w int) []string {
	haveW := false
	// the name column hugs its longest occupant — numbers read next to
	// names, not across a gulf of pane
	nameW := 12
	for _, s := range snaps {
		if s.Written >= 0 {
			haveW = true
		}
		if n := len("@" + s.Snap); n > nameW {
			nameW = n
		}
	}
	max := w - 2 - 9
	if haveW {
		max -= 10
	}
	if nameW > max {
		nameW = max
	}
	head := " " + padR("", nameW) + styDim.Render(padL("USED", 9))
	if haveW {
		head += styDim.Render(padL("WROTE", 10))
	}
	lines := []string{head}
	for _, s := range snaps {
		row := " " + styDim.Render(padR(truncate("@"+s.Snap, nameW), nameW)) +
			dimUnit(padL(zfs.NiceBytes(s.Used), 9))
		if haveW {
			wv := "-"
			if s.Written >= 0 {
				wv = zfs.NiceBytes(s.Written)
			}
			row += dimUnit(padL(wv, 10))
		}
		lines = append(lines, row)
	}
	return lines
}

// selInspector shows the marked snapshots and the authoritative reclaim
// figure — per-snapshot sums lie (shared blocks), the dry-run doesn't.
func selInspector(m *Model, w int) []string {
	sel := m.brSelection()
	c := m.brContainer()
	lines := []string{
		fmt.Sprintf(" %d snapshots selected", len(sel)),
		" " + styDim.Render("of "+truncate(c.Name, w-6)),
		"",
	}
	lines = append(lines, m.reclaimLines(m.SelectionTarget(), sel, w)...)
	lines = append(lines, "")
	show := sel
	if len(show) > 12 {
		lines = append(lines, " "+styDim.Render(fmt.Sprintf("… %d more", len(show)-12)))
		show = show[len(show)-12:]
	}
	lines = append(lines, snapTable(show, w)...)
	lines = append(lines, "", " "+styDim.Render("space: toggle · esc: clear"))
	return lines
}

func dsInspector(m *Model, h *hostState, d *zfs.Dataset, w int) []string {
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
		if p, ok := h.dsProps[d.Name]; ok {
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
		vw, par, ash, geo := h.poolGeometry(poolOf(d.Name))
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
	lines = append(lines, "")
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

	// key properties, with source tags once `zfs get` lands
	props := h.dsProps[d.Name]
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

	// live io from objset kstat deltas, nesting like `used`: one reading
	// covering this dataset and everything beneath it, in the pool block's
	// grammar — rates, aligned sparklines, ops. Entries exist only for
	// loaded datasets (mounted fs, active zvols).
	lines = append(lines, "")
	sub, rh, wh, loaded := h.subtreeIO(d)
	switch {
	case loaded > 0:
		sw := w - 33
		if sw > dsIOHistLen/2 {
			sw = dsIOHistLen / 2
		}
		if sw < 8 {
			sw = 8
		}
		ops := func(bw, n int64) string {
			cell := elastic(h.ioW, "ops", opsCell(bw, n))
			if bw == 0 && n == 0 {
				return styDim.Render(cell)
			}
			return dimUnit(cell)
		}
		lines = append(lines,
			" "+styBold.Render("io")+"   r "+ioRate(sub.RBw, 7)+"  "+sparklineFam(sparkSteel, rh, sw)+
				"  "+ops(sub.RBw, sub.ROps),
			"      w "+ioRate(sub.WBw, 7)+"  "+sparklineFam(sparkGold, wh, sw)+
				"  "+ops(sub.WBw, sub.WOps))
	case h.objsetPrev != nil:
		reason := "dataset not loaded"
		if len(d.Children) > 0 {
			reason = "nothing loaded in subtree"
		}
		lines = append(lines, " "+styBold.Render("io")+"   "+styDim.Render("no stats ("+reason+")"))
	}
	lines = append(lines, "")

	// the snapshot ledger closes the view. "pinning" splits usedbysnapshots
	// into per-snapshot-unique vs collectively-held space — when shared
	// dwarfs unique, only a range destroy gets the space back.
	if snaps, ok := h.dsSnaps[d.Name]; ok {
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
	return lines
}

func snapInspector(m *Model, s *zfs.Snapshot) []string {
	container := s.Name[:strings.IndexByte(s.Name, '@')]
	// one label column, values right-aligned so the units anchor. used is
	// the accountant (what deleting this frees), written the historian
	// (what changed in the window this snapshot closed — it doesn't shrink
	// when later snapshots still share the blocks).
	val := func(label string, v int64, gloss string) string {
		return " " + padR(label, 8) + dimUnit(padL(zfs.NiceBytes(v), 8)) +
			" " + styDim.Render("("+gloss+")")
	}
	lines := []string{
		" @" + s.Snap,
		" " + styDim.Render("of "+truncate(container, 60)),
		"",
		val("used", s.Used, "unique to this snapshot"),
	}
	if s.Written >= 0 {
		lines = append(lines, val("written", s.Written, "new data since the previous snapshot"))
	}
	return append(lines,
		val("refer", s.Refer, "data referenced by this snapshot"),
		"",
		" "+padR("created", 8)+absDate(s.Creation)+" "+styDim.Render("("+relAge(s.Creation)+")"))
}

func famInspector(m *Model, f *zfs.SnapFamily, w int) []string {
	lines := []string{
		fmt.Sprintf(" @%s · %d snapshots", f.Label(), len(f.Snaps)),
		" " + styDim.Render(absDate(f.Oldest().Creation)+" → "+absDate(f.Newest().Creation)),
		"",
	}
	lines = append(lines, m.reclaimLines(m.famTarget(f), f.Snaps, w)...)
	lines = append(lines, "")
	show := f.Snaps
	if len(show) > 8 {
		show = show[len(show)-8:]
		lines = append(lines, " "+styDim.Render(fmt.Sprintf("… %d older", len(f.Snaps)-8)))
	}
	// scanning the wrote column is how you find the epoch that ate the pool
	lines = append(lines, snapTable(show, w)...)
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
