package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// F8 — the destroy surface, the tool's first destructive command. The mark
// set IS the argument list: markGroups() already produces exactly the
// destroy units (a whole-dataset mark = `destroy -r ds`, a snapshot set =
// the same grouped `ds@a,b,c` the dry-run priced). F8 opens a wide popup
// showing, per host, the EXACT command each group becomes; enter executes
// the highlighted one, shift+F8 (arriving as f20 — both xterm's ;2~ and
// rxvt's legacy code decode to it) executes everything. Execution is
// sequential per host, parallel across hosts, and never retries — zfs
// errors are deterministic (Marton's doctrine). A finished command
// refreshes its host immediately, cadence tiers be damned: successes drop
// their marks and caches, failures keep their marks and carry their error
// into the popup row and the selection inspector.

const (
	destroyIdle = iota
	destroyRunning
	destroyDone
	destroyFailed
)

// generous: async_destroy makes dataset destroys cheap, but a long
// snapshot chain on spinners can genuinely take minutes
const destroyTimeout = 10 * time.Minute

type destroyRow struct {
	h       *hostState
	ds      string
	dsMark  bool
	target  string // the zfs argument: "ds" or "ds@a,b,c"
	sudo    bool
	nsnaps  int
	blocked string // nonempty = shown but unexecutable, this is why
	status  int
	errText string // full failure output; the unfold shows all of it
}

// cmdString is the exact command the row will execute — what the popup
// promises is what the Source runs (zfs names cannot contain spaces, so
// transport-level quoting adds nothing here).
func (r destroyRow) cmdString() string {
	s := "zfs destroy "
	if r.dsMark {
		s += "-r "
	}
	if r.sudo {
		s = "sudo -n " + s
	}
	return s + r.target
}

// rootFsGuard refuses commands whose blast radius includes the running
// system: any dataset in the subtree currently MOUNTED at / or /boot.
// Mountpoint alone is not the test — stale boot environments carry
// mountpoint=/ unmounted, and destroying those is legitimate hygiene.
func rootFsGuard(h *hostState, ds string) string {
	tree := h.dsTrees[poolOf(ds)]
	if tree == nil || tree.ByName[ds] == nil {
		return "dataset tree not loaded"
	}
	var bad string
	var walk func(d *zfs.Dataset)
	walk = func(d *zfs.Dataset) {
		if bad != "" {
			return
		}
		if d.Mounted == "yes" && (d.Mountpoint == "/" || d.Mountpoint == "/boot") {
			bad = "running system: " + d.Name + " on " + d.Mountpoint
			return
		}
		for _, c := range d.Children {
			walk(c)
		}
	}
	walk(tree.ByName[ds])
	return bad
}

// destroyKey identifies a mark group for the failure ledger the selection
// inspector reads after the popup closes.
func destroyKey(h *hostState, ds string) string { return h.name + "\x00" + ds }

// OpenDestroyPopup builds one row per mark group and opens the popup
// (also the --dump hook). Blocked rows are shown, dim, with their reason —
// the operator sees the whole selection, including what won't run.
func (m *Model) OpenDestroyPopup() {
	m.destroyRows = nil
	m.destroyAll = false
	for _, g := range m.markGroups() {
		if g.h == nil {
			continue
		}
		r := destroyRow{h: g.h, ds: g.ds, dsMark: g.dsMark, sudo: g.h.sudoOK}
		switch {
		case g.dsMark:
			r.target = g.ds
			r.blocked = rootFsGuard(g.h, g.ds)
		case g.pseudo:
			r.target = g.ds + "@…"
			r.blocked = "snapshot list still loading"
		case len(g.snaps) == 0:
			continue // marks outlived their snapshots: nothing to destroy
		default:
			r.target = g.target()
			r.nsnaps = len(g.snaps)
		}
		if r.blocked == "" {
			switch {
			case g.h.conn == connDown:
				r.blocked = "host unreachable"
			case !g.h.sudoOK:
				r.blocked = "no sudo"
			}
		}
		m.destroyRows = append(m.destroyRows, r)
	}
	m.destroyCur = 0
	m.destroyPop = len(m.destroyRows) > 0
}

// rowReclaim resolves a row's price live at render time — dry-runs still
// in flight when the popup opened land while it is up.
func rowReclaim(r destroyRow) (int64, bool) {
	if r.dsMark {
		tree := r.h.dsTrees[poolOf(r.ds)]
		if tree == nil || tree.ByName[r.ds] == nil {
			return 0, false
		}
		return tree.ByName[r.ds].Used, true
	}
	return dryReclaim(r.h.dryCache[r.target])
}

type destroyDoneMsg struct {
	host   string
	target string
	text   string
	err    error
}

func execDestroy(h *hostState, target string, recursive, sudo bool) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), destroyTimeout)
		defer cancel()
		text, err := src.Destroy(ctx, target, recursive, sudo)
		return destroyDoneMsg{host: host, target: target, text: text, err: err}
	}
}

// startRow launches one idle, unblocked row.
func (m *Model) startRow(i int) tea.Cmd {
	r := &m.destroyRows[i]
	if r.status != destroyIdle || r.blocked != "" || m.hostRunning(r.h.name) {
		return nil
	}
	r.status = destroyRunning
	return execDestroy(r.h, r.target, r.dsMark, r.sudo)
}

// hostRunning enforces sequential-per-host: one in-flight destroy per host.
func (m *Model) hostRunning(host string) bool {
	for i := range m.destroyRows {
		if m.destroyRows[i].h.name == host && m.destroyRows[i].status == destroyRunning {
			return true
		}
	}
	return false
}

// startNextForHost continues the shift+F8 chain after a completion.
func (m *Model) startNextForHost(host string) tea.Cmd {
	for i := range m.destroyRows {
		if m.destroyRows[i].h.name == host && m.destroyRows[i].status == destroyIdle &&
			m.destroyRows[i].blocked == "" {
			return m.startRow(i)
		}
	}
	return nil
}

func (m *Model) destroyKeys(msg string) tea.Cmd {
	switch msg {
	case "down", "j":
		if m.destroyCur < len(m.destroyRows)-1 {
			m.destroyCur++
		}
	case "up", "k":
		if m.destroyCur > 0 {
			m.destroyCur--
		}
	case "enter":
		return m.startRow(m.destroyCur)
	case "f20", "shift+f8":
		// the whammy: everything runnable, one command in flight per host
		m.destroyAll = true
		var cmds []tea.Cmd
		for i := range m.destroyRows {
			if c := m.startRow(i); c != nil {
				cmds = append(cmds, c)
			}
		}
		return tea.Batch(cmds...)
	case "esc", "f8":
		// closing stops the chain; in-flight commands finish and their
		// results still land (unmark, refresh) — zfs has already been asked
		m.destroyPop = false
		m.destroyAll = false
	}
	return nil
}

// applyDestroyDone processes one completed command. The bookkeeping runs
// off (host, target), not row identity — a result must land even if the
// popup was closed or rebuilt while the command was in flight.
func (m *Model) applyDestroyDone(msg destroyDoneMsg) tea.Cmd {
	h := m.hostByName(msg.host)
	if h == nil {
		return nil
	}
	ds, snapList, isSnaps := strings.Cut(msg.target, "@")

	// the popup row, if it still exists
	var row *destroyRow
	for i := range m.destroyRows {
		if m.destroyRows[i].h == h && m.destroyRows[i].target == msg.target {
			row = &m.destroyRows[i]
			break
		}
	}

	if msg.err != nil {
		errText := strings.TrimSpace(msg.text)
		if errText == "" {
			errText = msg.err.Error()
		}
		if row != nil {
			row.status = destroyFailed
			row.errText = errText
		}
		if m.destroyErrs == nil {
			m.destroyErrs = map[string]string{}
		}
		m.destroyErrs[destroyKey(h, ds)] = firstLineOf(errText)
	} else {
		if row != nil {
			row.status = destroyDone
		}
		// the destroyed things leave the selection
		if isSnaps {
			for _, s := range strings.Split(snapList, ",") {
				delete(m.marks, treeDsID(h, ds+"@"+s))
			}
		} else {
			delete(m.marks, treeDsID(h, ds))
		}
		m.markGen++
		delete(m.destroyErrs, destroyKey(h, ds))
		// a destroyed dataset takes its cached snapshot lists (and its
		// subtree's) and any t-toggles with it
		if !isSnaps {
			for name := range h.dsSnaps {
				if name == ds || strings.HasPrefix(name, ds+"/") {
					delete(h.dsSnaps, name)
				}
			}
			for id := range m.snapsShown {
				pfx := treeDsID(h, ds)
				if id == pfx || strings.HasPrefix(id, pfx+"/") {
					delete(m.snapsShown, id)
				}
			}
		}
	}

	// refresh NOW, success or failure — cadence tiers are for idling, not
	// for staring at a stale corpse. Neighbors' reclaim math is stale
	// either way (shared blocks moved), so the dry-run cache goes wholesale;
	// markEnsureCmds re-runs whatever the surviving selection still owes.
	h.dryCache = map[string]*dryResult{}
	h.statsDue, h.poolsDue, h.dsDue = time.Time{}, time.Time{}, time.Time{}
	var cmds []tea.Cmd
	if pool := poolOf(ds); !h.dsTreesPend[pool] {
		h.dsTreesPend[pool] = true
		cmds = append(cmds, fetchDatasets(h, pool))
	}
	if !h.poolsPend {
		h.poolsPend = true
		cmds = append(cmds, fetchPools(h))
	}
	if isSnaps && !h.dsSnapsPend[ds] {
		if _, cached := h.dsSnaps[ds]; cached {
			h.dsSnapsPend[ds] = true
			cmds = append(cmds, fetchSnaps(h, ds))
		}
	}
	if len(m.marks) > 0 {
		cmds = append(cmds, m.markDebounce())
	}
	if m.destroyAll && m.destroyPop {
		cmds = append(cmds, m.startNextForHost(msg.host))
	}
	return tea.Batch(cmds...)
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// destroyCmdDisplay fits a command into w cells for an unhighlighted row:
// the head (`sudo -n zfs destroy -r ds@first`) stays intact, the middle of
// the snapshot list elides with the count — shape and size survive the
// truncation. The highlighted row never needs this: it unfolds in full.
func destroyCmdDisplay(r destroyRow, w int) string {
	cmd := r.cmdString()
	if utf8.RuneCountInString(cmd) <= w {
		return cmd
	}
	if i := strings.IndexByte(cmd, '@'); i >= 0 && r.nsnaps > 1 {
		names := strings.Split(cmd[i+1:], ",")
		for keep := len(names) - 1; keep >= 1; keep-- {
			s := cmd[:i+1] + strings.Join(names[:keep], ",") +
				fmt.Sprintf(",…(+%d)", len(names)-keep)
			if utf8.RuneCountInString(s) <= w {
				return s
			}
		}
	}
	return truncate(cmd, w)
}

// hardWrap chunks a string every w cells regardless of word boundaries —
// a grouped destroy command is one giant token, and the unfold's promise
// is the VERBATIM string: word-wrap would overflow and clip it.
func hardWrap(s string, w int) []string {
	if w < 1 {
		return []string{s}
	}
	r := []rune(s)
	var out []string
	for len(r) > w {
		out = append(out, string(r[:w]))
		r = r[w:]
	}
	return append(out, string(r))
}

// destroyOverlay floats the popup over the frame — the ack overlay's
// splice, grown wide: this surface earns near full frame width. The
// highlighted row unfolds in place (full command wrapped, full error
// beneath it when failed), everything else keeps to one line.
func destroyOverlay(m *Model, frame string) string {
	if !m.destroyPop || len(m.destroyRows) == 0 {
		return frame
	}
	frameLines := strings.Split(frame, "\n")
	inner := m.w - 2
	boxW := inner - 6
	bodyW := boxW - 4 // "│ " + " │"

	// title: the inventory and its price
	snaps, dss := 0, 0
	var sum int64
	exact := true
	for _, r := range m.destroyRows {
		snaps += r.nsnaps
		if r.dsMark {
			dss++
		}
		if r.blocked != "" {
			continue
		}
		if n, ok := rowReclaim(r); ok {
			sum += n
		} else {
			exact = false
		}
	}
	plural := func(n int, noun string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s", noun)
		}
		return fmt.Sprintf("%d %ss", n, noun)
	}
	var parts []string
	if snaps > 0 {
		parts = append(parts, plural(snaps, "snapshot"))
	}
	if dss > 0 {
		parts = append(parts, plural(dss, "dataset")+" -r")
	}
	sigma := "reclaims " + zfs.NiceBytes(sum)
	if !exact {
		sigma = "reclaims ≥ " + zfs.NiceBytes(sum)
	}
	title := " F8 destroy · " + strings.Join(parts, " · ") + " · " + sigma + " "

	// body: host headers + rows, the highlighted one unfolded
	var body []string
	cursorLine := 0
	lastHost := ""
	for i, r := range m.destroyRows {
		if r.h.name != lastHost {
			if lastHost != "" {
				body = append(body, "")
			}
			body = append(body, styBold.Render(r.h.name))
			lastHost = r.h.name
		}
		cur := i == m.destroyCur
		if cur {
			cursorLine = len(body)
		}
		lead := "  "
		if cur {
			lead = "▸ "
		}

		// right column: the row's state in words, never hue alone
		var right string
		switch {
		case r.status == destroyRunning:
			right = styWarn.Render("running…")
		case r.status == destroyDone:
			right = styGood.Render("done")
		case r.status == destroyFailed:
			right = styBad.Render("FAILED")
		case r.blocked != "":
			right = styDim.Render(r.blocked)
		default:
			if n, ok := rowReclaim(r); ok {
				right = dimUnit(zfs.NiceBytes(n))
			} else {
				right = styDim.Render("…")
			}
		}
		rw := lipgloss.Width(right)
		cmdW := bodyW - 2 - rw - 2

		if cur {
			// the unfold: the exact command, complete, hard-wrapped
			wrapped := hardWrap(r.cmdString(), cmdW)
			body = append(body, styInv.Render(fit(lead+padR(wrapped[0], cmdW)+"  "+right, bodyW)))
			for _, wl := range wrapped[1:] {
				body = append(body, styInv.Render(fit("  "+wl, bodyW)))
			}
			if r.status == destroyFailed && r.errText != "" {
				for _, el := range hardWrap(r.errText, bodyW-4) {
					body = append(body, "    "+styBad.Render(el))
				}
			}
			continue
		}
		cmd := destroyCmdDisplay(r, cmdW)
		if r.blocked != "" || r.status == destroyDone {
			// spent and unrunnable rows recede; the right column already
			// carries their words
			body = append(body, styDim.Render(lead+padR(cmd, cmdW))+"  "+right)
		} else if r.status == destroyFailed {
			hint := ""
			if ew := cmdW - lipgloss.Width(cmd) - 3; ew > 8 {
				hint = " " + truncate(firstLineOf(r.errText), ew)
			}
			body = append(body, lead+padR(cmd+styDim.Render(hint), cmdW)+"  "+right)
		} else {
			body = append(body, lead+padR(cmd, cmdW)+"  "+right)
		}
	}

	// window the body if it outgrows the frame, keeping the cursor visible
	maxBody := len(frameLines) - 8
	if maxBody < 4 {
		maxBody = 4
	}
	if len(body) > maxBody {
		off := cursorLine - maxBody/2
		if off < 0 {
			off = 0
		}
		if off > len(body)-maxBody {
			off = len(body) - maxBody
		}
		clipped := body[off : off+maxBody]
		var win []string
		if off > 0 {
			win = append(win, styDim.Render(fmt.Sprintf("↑ %d more", off)))
		}
		win = append(win, clipped...)
		if rest := len(body) - off - maxBody; rest > 0 {
			win = append(win, styDim.Render(fmt.Sprintf("↓ %d more", rest)))
		}
		body = win
	}

	var box []string
	tW := lipgloss.Width(title)
	if tW > boxW-2 {
		title = " " + truncate(strings.TrimSpace(title), boxW-6) + " "
		tW = lipgloss.Width(title)
	}
	box = append(box, "┌─"+title+rep("─", boxW-2-tW-1)+"┐")
	for _, l := range body {
		box = append(box, "│ "+fit(l, bodyW)+" │")
	}
	box = append(box, "└"+rep("─", boxW-2)+"┘")

	top := 3
	if len(frameLines) > len(box)+6 {
		top = (len(frameLines) - len(box)) / 3
	}
	pad := (inner - boxW) / 2
	for i, bl := range box {
		li := top + i
		if li < 1 || li > len(frameLines)-4 {
			continue
		}
		frameLines[li] = "│" + rep(" ", pad) + bl + rep(" ", inner-pad-boxW) + "│"
	}
	return strings.Join(frameLines, "\n")
}
