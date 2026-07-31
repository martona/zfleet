package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/lipgloss"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The warnings inbox. Two species of yelling, two remedies:
//
// SMART warns — zfse's own interpretations — are ACKED into
// ~/.config/zfse/ack.conf: append-only, hand-editable, yadm-travels with
// the operator. The key is MODEL_SERIAL — no hostname, no transport
// prefix — so a drive can move between hosts and HBAs without lighting up
// again. A warn sleeps while its value matches the acked one and RETURNS
// the moment it grows.
//
//	SuperMicro_SSD_SMC0515D90822DJ06199 ata187 52  # 2026-07-25 commodoreplus4
//
// Later lines win, so re-acking a grown counter is one more line — and
// the file quietly becomes the drive's decay log.
//
// zpool error counters — zfs's OWN testimony — are never masked
// client-side (zfse must not show green where zpool status shows an
// error). Their remedy is zfs's remedy: the inbox line IS the verbatim
// `zpool clear` command (round 33, the write-mode era arriving here as
// prophesied), sudo-gated, enter runs it, the pool refetches immediately.
// The ereports stay in the journal; the clear lands in zpool history —
// clearing the dashboard light never erases the logbook.

// ackKey is the travel-proof drive identity: MODEL_SERIAL, spaces
// underscored. Empty when the drive offers neither — such a drive cannot
// be acked robustly and never enters the popup.
func ackKey(d *zfs.Disk) string {
	if d == nil {
		return ""
	}
	san := func(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), " ", "_") }
	model, serial := san(d.Model), san(d.Serial)
	if serial == "" {
		return ""
	}
	if model == "" {
		return serial
	}
	return model + "_" + serial
}

func defaultAckPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "zfse", "ack.conf")
}

// SetAckFile points the ack ledger at a path ("" = the default) and loads
// it. Called by main before the program runs; also the --dump hook.
func (m *Model) SetAckFile(path string) {
	if path == "" {
		path = defaultAckPath()
	}
	m.ackPath = path
	for k := range m.acks {
		delete(m.acks, k)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return // absent file = no acks; that is the common fresh state
	}
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v := ""
		if len(f) >= 3 {
			v = f[2]
		}
		m.acks[f[0]+"\x00"+f[1]] = v // last line wins per (drive, check)
	}
}

// appendAck records one ack: in memory (the map every hostState shares)
// and on disk, with a date+host trailer the parser ignores.
func (m *Model) appendAck(key string, c zfs.SmartCheck, host string) {
	val := c.Value
	if c.ID == "overall" {
		val = "" // value-less: silence while it stays FAILED
	}
	m.acks[key+"\x00"+c.ID] = val
	line := key + " " + c.ID
	if val != "" {
		line += " " + val
	}
	line += "  # " + time.Now().Format("2006-01-02") + " " + host + "\n"
	if err := os.MkdirAll(filepath.Dir(m.ackPath), 0o755); err != nil {
		m.ackErr = err.Error()
		return
	}
	f, err := os.OpenFile(m.ackPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		m.ackErr = err.Error()
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		m.ackErr = err.Error()
	}
}

// ackedCheck reports whether a check is answered: an entry exists and its
// value still matches (a value-less entry matches any).
func (h *hostState) ackedCheck(d *zfs.Disk, c zfs.SmartCheck) bool {
	key := ackKey(d)
	if key == "" {
		return false
	}
	v, ok := h.acks[key+"\x00"+c.ID]
	return ok && (v == "" || v == c.Value)
}

// checkSev maps one check's tier through the ack ledger.
func (h *hostState) checkSev(d *zfs.Disk, c zfs.SmartCheck) int {
	sev := sevOK
	switch c.Sev {
	case zfs.CheckWarn:
		sev = sevWarn
	case zfs.CheckFail:
		sev = sevErr
	}
	if sev > sevOK && h.ackedCheck(d, c) {
		return sevOK
	}
	return sev
}

// diskSmartSev is a drive's EFFECTIVE smart tier — acked warns are
// answered and count for nothing. The raw tier (smartSev) still decides
// whether the verdict says "ack" instead of "ok".
func (h *hostState) diskSmartSev(node string) int {
	s, ok := h.smart[node]
	if !ok {
		return sevOK
	}
	d := h.diskByNode(node)
	out := sevOK
	for _, c := range s.Checks {
		if v := h.checkSev(d, c); v > out {
			out = v
		}
	}
	return out
}

func (h *hostState) diskByNode(node string) *zfs.Disk {
	for i := range h.disks {
		if h.disks[i].Node == node {
			return &h.disks[i]
		}
	}
	return nil
}

// ackPendingHost counts a host's unanswered warn checks.
func (h *hostState) ackPendingHost() int {
	n := 0
	for node, s := range h.smart {
		d := h.diskByNode(node)
		for _, c := range s.Checks {
			if h.checkSev(d, c) > sevOK {
				n++
			}
		}
	}
	return n
}

// ackPending counts the inbox fleet-wide — unanswered SMART warns plus
// counter-bearing vdevs awaiting a clear.
func (m *Model) ackPending() int {
	n := 0
	for _, h := range m.hosts {
		n += h.ackPendingHost() + len(h.clearFacts())
	}
	return n
}

// The popup: the fleet's unanswered warns as one list, because the drill
// panels have no cursor. One line per CHECK — the ack unit — up/down
// moves, enter acks at the CURRENT value and removes the line, esc
// closes, empty closes itself. This is also the tool's first
// confirmation-shaped surface; write-mode will inherit the pattern.

type ackEntry struct {
	h     *hostState
	node  string
	check string // check ID; label and value resolve live at render
	dup   int    // occurrences collapsed into this line (nvme namespaces
	// echo their controller's one health log — one fact, one line, one ack)

	// clear lines: zpool counter facts. pool != "" marks the kind; vdev ""
	// means pool-wide (interior vdev counters — zpool clear addresses
	// devices, so interior rows clear at the pool).
	pool    string
	vdev    string
	clearSt int
	errText string
}

const (
	clearIdle = iota
	clearRunning
	clearDone // counters gone from zfs; the line retires when the refetch lands
	clearFailed
)

// clearFacts lists a host's counter-bearing vdevs as inbox entries: one
// per leaf with its own counters, plus one pool-wide entry when an
// interior vdev carries any.
func (h *hostState) clearFacts() []ackEntry {
	var out []ackEntry
	for _, p := range h.pools {
		poolWide := false
		var walk func(v *zfs.Vdev)
		walk = func(v *zfs.Vdev) {
			if len(v.Children) == 0 {
				if v.HasErrors() {
					out = append(out, ackEntry{h: h, pool: p.Name, vdev: v.Name})
				}
				return
			}
			if v.HasErrors() {
				poolWide = true
			}
			for _, c := range v.Children {
				walk(c)
			}
		}
		for _, c := range p.Classes {
			for _, v := range c.Vdevs {
				walk(v)
			}
		}
		if poolWide {
			out = append(out, ackEntry{h: h, pool: p.Name})
		}
	}
	return out
}

// vdevBadge renders a vdev's nonzero counters the way the tree does:
// "R 3 C 1", counters as zpool printed them.
func vdevBadge(v *zfs.Vdev) string {
	nz := func(s string) bool { return s != "" && s != "0" }
	var parts []string
	if nz(v.ReadErr) {
		parts = append(parts, "R "+v.ReadErr)
	}
	if nz(v.WriteErr) {
		parts = append(parts, "W "+v.WriteErr)
	}
	if nz(v.CksumErr) {
		parts = append(parts, "C "+v.CksumErr)
	}
	return strings.Join(parts, " ")
}

// clearResolve returns the live counter badge for a clear line — "" once
// the counters are gone (cleared, or the vdev left the pool).
func (e ackEntry) clearResolve() string {
	p := e.h.pool(e.pool)
	if p == nil {
		return ""
	}
	badge := ""
	var walk func(v *zfs.Vdev)
	walk = func(v *zfs.Vdev) {
		if badge != "" {
			return
		}
		leaf := len(v.Children) == 0
		if e.vdev == "" && !leaf && v.HasErrors() {
			badge = vdevBadge(v)
			return
		}
		if e.vdev != "" && leaf && v.Name == e.vdev {
			badge = vdevBadge(v)
			return
		}
		for _, c := range v.Children {
			walk(c)
		}
	}
	for _, c := range p.Classes {
		for _, v := range c.Vdevs {
			walk(v)
		}
	}
	return badge
}

// clearCmd is the verbatim command a clear line will run — showing it IS
// the design: the operator sees this is a remote write, not a local
// ledger entry.
func clearCmd(e ackEntry) string {
	s := "zpool clear " + e.pool
	if e.vdev != "" {
		s += " " + e.vdev
	}
	if e.h.sudoOK {
		s = "sudo -n " + s
	}
	return s
}

type poolClearMsg struct {
	host, pool, vdev, text string
	err                    error
}

func execPoolClear(h *hostState, pool, vdev string) tea.Cmd {
	host, src, sudo := h.name, h.src, h.sudoOK
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		text, err := src.PoolClear(ctx, pool, vdev, sudo)
		return poolClearMsg{host: host, pool: pool, vdev: vdev, text: text, err: err}
	}
}

// applyPoolClear lands one clear's result: failure wears its reason on
// the line; success refetches the pool NOW so the counters vanish from
// every surface at once.
func (m *Model) applyPoolClear(msg poolClearMsg) tea.Cmd {
	h := m.hostByName(msg.host)
	if h == nil {
		return nil
	}
	for i := range m.ackList {
		e := &m.ackList[i]
		if e.h == h && e.pool == msg.pool && e.vdev == msg.vdev && e.clearSt == clearRunning {
			if msg.err != nil {
				e.clearSt = clearFailed
				e.errText = firstLineOf(msg.text)
				if e.errText == "" {
					e.errText = msg.err.Error()
				}
			} else {
				e.clearSt = clearDone
			}
			break
		}
	}
	if msg.err != nil {
		return nil
	}
	h.poolsDue = time.Time{}
	if !h.poolsPend {
		h.poolsPend = true
		return fetchPools(h)
	}
	return nil
}

// OpenAckPopup snapshots the unanswered warns and opens the popup (also
// the --dump hook). Deduped by (drive key, check, value): what enter
// silences is exactly what one line represents. A quiet fleet stays
// popup-less.
func (m *Model) OpenAckPopup() {
	m.ackList = nil
	seen := map[string]int{}
	for _, h := range m.hosts {
		for _, d := range h.disks {
			s, ok := h.smart[d.Node]
			if !ok {
				continue
			}
			dd := h.diskByNode(d.Node)
			key := ackKey(dd)
			if key == "" {
				continue
			}
			for _, c := range s.Checks {
				if h.checkSev(dd, c) <= sevOK {
					continue
				}
				dk := key + "\x00" + c.ID + "\x00" + c.Value
				if i, ok := seen[dk]; ok {
					m.ackList[i].dup++
					continue
				}
				seen[dk] = len(m.ackList)
				m.ackList = append(m.ackList, ackEntry{h: h, node: d.Node, check: c.ID, dup: 1})
			}
		}
	}
	// the second species: zpool counters, each line the clear that answers it
	for _, h := range m.hosts {
		m.ackList = append(m.ackList, h.clearFacts()...)
	}
	m.ackCur = 0
	m.ackPop = len(m.ackList) > 0
}

// ackResolve finds an entry's current check; gone means the warn resolved
// itself (refetch, device change) and the row silently retires.
func (e ackEntry) resolve() (*zfs.Disk, zfs.SmartCheck, bool) {
	d := e.h.diskByNode(e.node)
	s, ok := e.h.smart[e.node]
	if d == nil || !ok {
		return nil, zfs.SmartCheck{}, false
	}
	for _, c := range s.Checks {
		if c.ID == e.check {
			return d, c, true
		}
	}
	return nil, zfs.SmartCheck{}, false
}

// ackRepair drops entries that no longer resolve to a live unacked warn —
// or, for clear lines, to live counters (in-flight and failed clears stay
// visible regardless; the operator deserves the outcome).
func (m *Model) ackRepair() {
	var keep []ackEntry
	for _, e := range m.ackList {
		if e.pool != "" {
			if e.clearSt == clearRunning || e.clearSt == clearFailed || e.clearResolve() != "" {
				keep = append(keep, e)
			}
			continue
		}
		if d, c, ok := e.resolve(); ok && e.h.checkSev(d, c) > sevOK {
			keep = append(keep, e)
		}
	}
	m.ackList = keep
	if m.ackCur > len(m.ackList)-1 {
		m.ackCur = len(m.ackList) - 1
	}
	if m.ackCur < 0 {
		m.ackCur = 0
	}
	if len(m.ackList) == 0 {
		m.ackPop = false
	}
}

func (m *Model) ackKeys(msg string) tea.Cmd {
	m.ackRepair()
	if !m.ackPop {
		return nil
	}
	switch msg {
	case "down", "j":
		if m.ackCur < len(m.ackList)-1 {
			m.ackCur++
		}
	case "up", "k":
		if m.ackCur > 0 {
			m.ackCur--
		}
	case "enter":
		e := &m.ackList[m.ackCur]
		if e.pool != "" {
			// the write species: runs the exact command on the line
			if e.clearSt != clearIdle || !e.h.sudoOK {
				return nil
			}
			e.clearSt = clearRunning
			return execPoolClear(e.h, e.pool, e.vdev)
		}
		if d, c, ok := e.resolve(); ok {
			m.appendAck(ackKey(d), c, e.h.name)
		}
		m.ackRepair()
	case "esc", "a":
		m.ackPop = false
	}
	return nil
}

// ackOverlay floats the popup over the frame: the box replaces a band of
// content rows, everything above and below stays visible.
func ackOverlay(m *Model, frame string) string {
	m.ackRepair()
	if !m.ackPop {
		return frame
	}
	type row struct {
		host, node, model, warn string
		warnSty                 lipgloss.Style
		cmd                     bool // clear line: node IS the verbatim command
	}
	var rows []row
	hw, nw := 4, 4
	for _, e := range m.ackList {
		if e.pool != "" {
			r := row{host: e.h.name, node: clearCmd(e), cmd: true, warnSty: styWarn}
			switch e.clearSt {
			case clearRunning:
				r.warn = "clearing…"
			case clearDone:
				r.warn, r.warnSty = "cleared", styGood
			case clearFailed:
				r.warn, r.warnSty = "FAILED: "+truncate(e.errText, 40), styBad
			default:
				r.warn = e.clearResolve()
				if !e.h.sudoOK {
					r.warn += " · no sudo"
					r.warnSty = styDim
				}
			}
			rows = append(rows, r)
			if n := len(e.h.name); n > hw {
				hw = n
			}
			continue
		}
		d, c, _ := e.resolve()
		node := e.node
		if e.dup > 1 {
			// namespaces of one controller collapse to the controller
			if i := strings.LastIndexByte(node, 'n'); strings.HasPrefix(node, "nvme") && i > 4 {
				node = fmt.Sprintf("%s ×%dns", node[:i], e.dup)
			} else {
				node = fmt.Sprintf("%s ×%d", node, e.dup)
			}
		}
		rows = append(rows, row{host: e.h.name, node: node, model: d.Model,
			warn: c.Label + " " + c.Value, warnSty: styWarn})
		if n := len(e.h.name); n > hw {
			hw = n
		}
		if n := len(node); n > nw {
			nw = n
		}
	}
	title := fmt.Sprintf(" warnings (%d) ", len(rows))
	var body []string
	for i, r := range rows {
		lead := "  "
		if i == m.ackCur {
			lead = "▸ "
		}
		var line string
		if r.cmd {
			// the command is the content — dim host, VERBATIM line, state
			line = lead + padR(r.host, hw+2) + r.node + "  "
		} else {
			line = lead + padR(r.host, hw+2) + padR(r.node, nw+2) +
				padR(truncate(r.model, 18), 19)
		}
		if i == m.ackCur {
			body = append(body, styInv.Render(line+r.warn))
		} else if r.cmd {
			body = append(body, styDim.Render(lead+padR(r.host, hw+2))+r.node+"  "+
				r.warnSty.Render(r.warn))
		} else {
			body = append(body, styDim.Render(line)+r.warnSty.Render(r.warn))
		}
	}
	if m.ackErr != "" {
		body = append(body, styBad.Render(" ack write failed: "+truncate(m.ackErr, 40)))
	}
	boxW := lipgloss.Width(title) + 4
	for _, l := range body {
		if n := lipgloss.Width(l) + 4; n > boxW {
			boxW = n
		}
	}
	inner := m.w - 2
	if boxW > inner-4 {
		boxW = inner - 4
	}
	var box []string
	box = append(box, "┌─"+title+rep("─", boxW-2-lipgloss.Width(title)-1)+"┐")
	for _, l := range body {
		box = append(box, "│"+fit(" "+l, boxW-2)+"│")
	}
	box = append(box, "└"+rep("─", boxW-2)+"┘")

	lines := strings.Split(frame, "\n")
	top := 3
	if len(lines) > len(box)+6 {
		top = (len(lines) - len(box)) / 3
	}
	pad := (inner - boxW) / 2
	spliceOverlay(lines, box, top, 1+pad)
	return strings.Join(lines, "\n")
}
