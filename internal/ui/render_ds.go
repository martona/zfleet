package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// Right-panel inspectors for datasets, snapshots, snapshot families, and
// the mark selection. All of them render into the tree's two-pane frame.

// keyChip renders an inline hotkey hint in the cheat line's idiom —
// inverted key, plain label.
func keyChip(key, label string) string {
	return styInv.Render(" "+key+" ") + " " + label
}

// dryReclaim extracts the reclaim bytes from a cached `-nvp` dry-run.
func dryReclaim(r *dryResult) (int64, bool) {
	if r == nil || r.pending || r.errText != "" {
		return 0, false
	}
	for _, l := range strings.Split(r.text, "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "reclaim" {
			n, err := strconv.ParseInt(f[1], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// reclaimLines renders the dry-run verdict for a target: the exact figure
// once known, the Σ lower bound only until then.
func reclaimLines(h *hostState, target string, snaps []*zfs.Snapshot, w int) []string {
	var sum int64
	for _, s := range snaps {
		sum += s.Used
	}
	r := h.dryCache[target]
	switch {
	case r != nil && r.errText != "":
		return []string{
			" Σ used " + zfs.NiceBytes(sum) + styDim.Render(" — lower bound"),
			" " + styDim.Render("dry-run: "+truncate(r.errText, w-12)),
		}
	case r != nil && !r.pending && r.text != "":
		if n, ok := dryReclaim(r); ok {
			return []string{" " + styGood.Render("would reclaim "+zfs.NiceBytes(n))}
		}
		// legacy non-parseable output: show zfs's own words
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

// snapTable is the shared member ledger — name, used, wrote, refer — one
// view for collapsed families and hand-picked selections alike. The wrote
// column only exists when the capture carries it; the column order follows
// the snapshot card (used, written, refer).
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
	max := w - 2 - 9 - 10
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
	head += styDim.Render(padL("REFER", 10))
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
		row += dimUnit(padL(zfs.NiceBytes(s.Refer), 10))
		lines = append(lines, row)
	}
	return lines
}

// selInspector is the collection: every marked thing grouped per dataset,
// each group with its honest number — grouped dry-run reclaim for
// snapshot sets (per-snapshot sums lie about shared blocks), recursive
// `used` for whole datasets — and the Σ across the fleet.
func selInspector(m *Model, w int) []string {
	groups := m.markGroups()
	hosts := map[string]bool{}
	for _, g := range groups {
		if g.h != nil {
			hosts[g.h.name] = true
		}
	}
	head := fmt.Sprintf(" %d marked", len(m.marks))
	if m.multiHost {
		head += fmt.Sprintf(" · %d hosts", len(hosts))
	}
	lines := []string{head, ""}

	// Σ: exact components only; the rest is either still computing
	// (transient) or failed (terminal — and named below in the inventory)
	var sum int64
	computing, failed := 0, 0
	value := func(g markGroup) (int64, bool) {
		switch {
		case g.dsMark:
			u := g.dsUsed()
			return u, u >= 0
		case g.pseudo, g.h == nil:
			return 0, false
		case len(g.snaps) == 0:
			// marks referencing snapshots that no longer exist are worth
			// exactly nothing — but only once the list has confirmed it
			return 0, g.loaded
		default:
			return dryReclaim(g.h.dryCache[g.target()])
		}
	}
	groupErr := func(g markGroup) string {
		if g.dsMark || g.pseudo || g.h == nil || len(g.snaps) == 0 {
			return ""
		}
		r := g.h.dryCache[g.target()]
		switch {
		case r == nil || r.pending:
			return ""
		case r.errText != "":
			return strings.SplitN(r.errText, "\n", 2)[0]
		}
		if _, ok := dryReclaim(r); !ok {
			return "unparseable dry-run output"
		}
		return ""
	}
	for _, g := range groups {
		if n, ok := value(g); ok {
			sum += n
		} else if groupErr(g) != "" {
			failed++
		} else {
			computing++
		}
	}
	sigma := " " + styBold.Render("Σ would free ") + styGood.Render(zfs.NiceBytes(sum))
	if computing+failed > 0 {
		sigma = " " + styBold.Render("Σ would free ≥ ") + zfs.NiceBytes(sum)
		if computing > 0 {
			sigma += styDim.Render(fmt.Sprintf(" · %d computing…", computing))
		}
		if failed > 0 {
			sigma += styBad.Render(fmt.Sprintf(" · %d failed", failed))
		}
	}
	lines = append(lines, sigma, "")

	// the inventory: one line per SELECTED object, full name, nothing
	// else — a dataset path appears only when the dataset itself is
	// marked. The grouped dry-runs stay underneath; only Σ speaks for
	// them, per-line values are each snap's own used (context, dim).
	qualify := func(g markGroup) string {
		if m.multiHost && g.h != nil {
			return g.h.name + ":" + g.ds
		}
		return g.ds
	}
	nameW := w - 13
	if nameW < 16 {
		nameW = 16
	}
	for _, g := range groups {
		switch {
		case g.dsMark:
			val := "…"
			if n, ok := value(g); ok {
				val = zfs.NiceBytes(n)
			}
			lines = append(lines, " "+padR(truncate(qualify(g)+" ", nameW-11), nameW-11)+
				styWarn.Render(padR("dataset -r", 11))+dimUnit(padL(val, 8)))
		case g.pseudo:
			lines = append(lines, " "+styDim.Render(padR(truncate(qualify(g)+"@ (resolving…)", nameW), nameW)))
		default:
			for _, s := range g.snaps {
				lines = append(lines, " "+padR(truncate(qualify(g)+"@"+s.Snap, nameW), nameW)+
					dimUnit(padL(zfs.NiceBytes(s.Used), 8)))
			}
			if len(g.snaps) == 0 && g.loaded {
				lines = append(lines, " "+styDim.Render(truncate(qualify(g)+"@ (marks outlived their snapshots)", nameW+8)))
			}
			// a failed group names its reason right under its lines — a
			// diagnostic annotation, not a selection
			if e := groupErr(g); e != "" {
				lines = append(lines, "   └ "+styBad.Render("dry-run failed: ")+
					styDim.Render(truncate(e, w-24)))
			}
		}
	}
	lines = append(lines, "",
		" "+keyChip("space", "toggle")+styDim.Render(" · ")+keyChip("esc", "clear all"))
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
			// the toggle that replaced the old browse mode — loud on purpose
			hint := "show snapshots in the tree"
			if m.snapsShown[treeDsID(h, d.Name)] {
				hint = "hide snapshots"
			}
			lines = append(lines, " "+keyChip("t", hint))
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
		" "+padR("created", 8)+absDate(s.Creation)+" "+styDim.Render("("+relAge(s.Creation)+")"),
		"",
		" "+keyChip("space", "select for analysis"))
}

func famInspector(m *Model, h *hostState, container string, f *zfs.SnapFamily, w int) []string {
	lines := []string{
		fmt.Sprintf(" @%s · %d snapshots", f.Label(), len(f.Snaps)),
		" " + styDim.Render(absDate(f.Oldest().Creation)+" → "+absDate(f.Newest().Creation)),
		"",
	}
	lines = append(lines, reclaimLines(h, famTarget(container, f), f.Snaps, w)...)
	lines = append(lines, "")
	show := f.Snaps
	if len(show) > 8 {
		show = show[len(show)-8:]
		lines = append(lines, " "+styDim.Render(fmt.Sprintf("… %d older", len(f.Snaps)-8)))
	}
	// scanning the wrote column is how you find the epoch that ate the pool
	lines = append(lines, snapTable(show, w)...)
	lines = append(lines, "", " "+keyChip("space", "select family for analysis"))
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
