package ui

import (
	"context"
	"path"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// Dataset-cache machinery for the tree: lazy fetches for dataset trees,
// snapshot lists, and properties, plus the snapshot mark set with its
// debounced `zfs destroy -nv` reclaim math. Snapshots surface as tree rows
// (toggled per dataset with `t`), so there is no separate browsing mode —
// one navigation surface, one cursor.

const modePools = 0 // the tree screen — the one navigation surface

const datasetsInterval = 10 * time.Second

const familyMinSize = 3

// poolOf returns the pool component of a dataset path.
func poolOf(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// dryResult caches one `zfs destroy -nv` output, keyed by its exact target
// string — a changed selection or snapshot set is a different key, so the
// cache self-invalidates. Errors cache like answers, because they are:
// zfs failures are deterministic (clones, permissions, vanished snaps) —
// re-asking gets the same reply (Marton's ruling).
type dryResult struct {
	text    string
	errText string
	pending bool
	// memoized reclaim parse — Σ reads every group every frame
	reclaim int64
	tried   bool
	haveN   bool
}

// The mark set: reclaim selections across datasets and hosts. Mark ids
// are tree row ids — "host\x00ds" marks a whole dataset (delete means -r,
// worth its `used`), "host\x00ds@snap" one snapshot, and the internal
// "host\x00ds@" pseudo-mark means "every snapshot of ds" (minted by a
// split before the snapshot list has loaded; resolves when it lands).
//
// Selection INHERITS: a marked dataset covers its whole subtree — covered
// rows render selected but are never in the set. Deselecting inside a
// covered subtree SPLITS the ancestor into the zfs-true decomposition
// (the ancestor survives: its snaps and the untouched siblings become
// manual marks), and marking a dataset absorbs any marks beneath it. By
// construction the set never double-counts.

// dsMarkable rejects the rows a mark may not claim: pool root datasets
// (that is pool demolition, not housekeeping).
func dsMarkable(r treeRow) bool {
	return r.kind == rDataset && r.depth > 1
}

// filterMarkable: while filtering, space addresses only what the pattern
// matched — snap rows in @-hunts, hit dataset rows in name hunts. A
// dataset row in an @-hunt is a container heading, not a match; space
// skips over it (the streak flows through), and whole-dataset marking
// waits in the real tree, one esc away.
func (m *Model) filterMarkable(r treeRow) bool {
	_, _, hasSnap := splitFilter(m.filter)
	switch r.kind {
	case rSnap:
		return true
	case rDataset:
		return !hasSnap && r.hit && dsMarkable(r)
	}
	return false
}

// coveringDs returns the marked dataset at or above dsName ("" when
// uncovered). The invariant allows at most one; pool roots are never
// markable, so two segments is as high as the scan goes.
func (m *Model) coveringDs(h *hostState, dsName string) string {
	segs := strings.Split(dsName, "/")
	for i := len(segs); i >= 2; i-- {
		p := strings.Join(segs[:i], "/")
		if m.marks[treeDsID(h, p)] {
			return p
		}
	}
	return ""
}

// toggleMark flips selection on a dataset, snapshot, or whole family.
func (m *Model) toggleMark(r treeRow) bool {
	h := r.host
	switch r.kind {
	case rDataset:
		if !dsMarkable(r) {
			return false
		}
		id := r.id
		switch {
		case m.marks[id]:
			delete(m.marks, id)
		case m.coveringDs(h, parentPath(r.ds.Name)) != "":
			m.splitAround(h, r.ds.Name, nil)
		default:
			m.absorbSubtree(h, r.ds.Name)
			m.marks[id] = true
		}
	case rSnap:
		sid := r.id
		switch {
		case m.marks[sid]:
			delete(m.marks, sid)
		case m.marks[treeDsID(h, r.ds.Name)+"@"] || m.coveringDs(h, r.ds.Name) != "":
			m.splitAround(h, r.ds.Name+"@", map[string]bool{r.snap.Snap: true})
		default:
			m.marks[sid] = true
		}
	case rFam:
		members := map[string]bool{}
		for _, s := range r.fam.Snaps {
			members[s.Snap] = true
		}
		switch {
		case m.marks[treeDsID(h, r.ds.Name)+"@"] || m.coveringDs(h, r.ds.Name) != "":
			// covered family: deselecting it splits everything around it
			m.splitAround(h, r.ds.Name+"@", members)
		default:
			all := true
			for _, s := range r.fam.Snaps {
				if !m.marks[treeDsID(h, r.ds.Name+"@"+s.Snap)] {
					all = false
					break
				}
			}
			for _, s := range r.fam.Snaps {
				id := treeDsID(h, r.ds.Name+"@"+s.Snap)
				if all {
					delete(m.marks, id)
				} else {
					m.marks[id] = true
				}
			}
		}
	default:
		return false
	}
	m.markGen++
	return true
}

// parentPath is the dataset path one level up ("" at the pool root).
func parentPath(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return ""
}

// splitAround deselects one thing inside a covered subtree by decomposing
// the covering mark level by level: at each dataset on the path the snaps
// and the siblings off the path become manual marks; the covering
// ancestor is unmarked; the excluded thing ends up unselected. exclude:
// target "ds@" with a snap-name set excludes snapshots of ds; target a
// dataset path with nil excludes that dataset itself.
func (m *Model) splitAround(h *hostState, target string, exclSnaps map[string]bool) {
	dsPath := strings.TrimSuffix(target, "@")
	snapSplit := strings.HasSuffix(target, "@")

	// the pseudo case: "all snaps of ds" resolving into all-but-excluded.
	// A pseudo never coexists with a covering ancestor (marking one
	// absorbs the other), so this is the whole story.
	if snapSplit && m.marks[treeDsID(h, dsPath)+"@"] {
		delete(m.marks, treeDsID(h, dsPath)+"@")
		for _, s := range h.dsSnaps[dsPath] {
			if !exclSnaps[s.Snap] {
				m.marks[treeDsID(h, dsPath+"@"+s.Snap)] = true
			}
		}
		return
	}

	bottom := dsPath
	if !snapSplit {
		bottom = parentPath(dsPath) // decompose down to the excluded ds's parent
	}
	anc := m.coveringDs(h, bottom+"/x") // covering of anything at/below bottom
	if anc == "" {
		return
	}
	delete(m.marks, treeDsID(h, anc))

	tree := h.dsTrees[poolOf(anc)]
	if tree == nil {
		return // cannot decompose without the tree; marks drop honestly
	}
	// walk anc → bottom; at each level mark snaps and off-path children
	level := anc
	for {
		d := tree.ByName[level]
		if d == nil {
			return
		}
		next := "" // the on-path child to descend into
		if level != bottom {
			rest := strings.TrimPrefix(bottom, level+"/")
			next = level + "/" + strings.SplitN(rest, "/", 2)[0]
		}
		// this level's snapshots join the selection (unless this is the
		// level whose snaps are being excluded)
		if snapSplit && level == bottom {
			for _, s := range h.dsSnaps[level] {
				if !exclSnaps[s.Snap] {
					m.marks[treeDsID(h, level+"@"+s.Snap)] = true
				}
			}
		} else if snaps, ok := h.dsSnaps[level]; ok {
			for _, s := range snaps {
				m.marks[treeDsID(h, level+"@"+s.Snap)] = true
			}
		} else {
			m.marks[treeDsID(h, level)+"@"] = true // list pending: pseudo
		}
		for _, k := range d.Children {
			if k.Name != next && !(level == bottom && !snapSplit && k.Name == dsPath) {
				m.marks[treeDsID(h, k.Name)] = true
			}
		}
		if level == bottom {
			return
		}
		level = next
	}
}

// absorbSubtree removes every mark beneath a dataset about to be marked —
// inheritance replaces them.
func (m *Model) absorbSubtree(h *hostState, dsName string) {
	pfx := treeDsID(h, dsName)
	for id := range m.marks {
		if strings.HasPrefix(id, pfx+"/") || strings.HasPrefix(id, pfx+"@") {
			delete(m.marks, id)
		}
	}
}

func (m *Model) clearMarks() {
	if len(m.marks) > 0 {
		m.marks = map[string]bool{}
		m.markGen++
	}
}

// markGroup is one dataset's worth of selection — the unit the math runs
// on: a whole-dataset mark (worth its recursive `used`), or a snapshot
// set (worth one grouped dry-run).
type markGroup struct {
	h      *hostState
	ds     string
	dsMark bool
	snaps  []*zfs.Snapshot // chronological, resolved against the live list
	pseudo bool            // all-snaps mark whose list has not loaded yet
	loaded bool            // the snapshot list is in; empty snaps = truly 0
}

// target is the grouped dry-run argument ("ds@a,b,c"), "" when not a
// resolvable snapshot group.
func (g markGroup) target() string {
	if g.dsMark || g.pseudo || len(g.snaps) == 0 {
		return ""
	}
	names := make([]string, len(g.snaps))
	for i, s := range g.snaps {
		names[i] = s.Snap
	}
	return g.ds + "@" + strings.Join(names, ",")
}

// markGroups serves the grouped selection from cache — the Σ math and the
// inventory read it every frame, and at thousands of marks the grouping
// itself is frame budget. Rebuilds on mark changes (gen) or data changes
// (dirtyData).
func (m *Model) markGroups() []markGroup {
	if m.groupsOK && m.groupsGen == m.markGen {
		return m.groupsCache
	}
	m.groupsCache = m.buildMarkGroups()
	m.groupsOK, m.groupsGen = true, m.markGen
	return m.groupsCache
}

func (m *Model) buildMarkGroups() []markGroup {
	type key struct {
		host, ds string
	}
	byDs := map[key]*markGroup{}
	var order []key
	group := func(hName, ds string) *markGroup {
		k := key{hName, ds}
		if g, ok := byDs[k]; ok {
			return g
		}
		g := &markGroup{h: m.hostByName(hName), ds: ds}
		byDs[k] = g
		order = append(order, k)
		return g
	}
	for id := range m.marks {
		i := strings.IndexByte(id, 0)
		if i < 0 {
			continue
		}
		hName, rest := id[:i], id[i+1:]
		if j := strings.IndexByte(rest, '@'); j >= 0 {
			g := group(hName, rest[:j])
			if rest[j+1:] == "" {
				g.pseudo = true // resolved below if the list is in
			}
		} else {
			group(hName, rest).dsMark = true
		}
	}
	for k, g := range byDs {
		if g.dsMark || g.h == nil {
			continue
		}
		snaps, loaded := g.h.dsSnaps[k.ds]
		g.loaded = loaded
		if g.pseudo && loaded {
			g.pseudo, g.snaps = false, snaps
			continue
		}
		if g.pseudo {
			continue
		}
		for _, s := range snaps {
			if m.marks[treeDsID(g.h, k.ds+"@"+s.Snap)] {
				g.snaps = append(g.snaps, s)
			}
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].host != order[j].host {
			return order[i].host < order[j].host
		}
		return order[i].ds < order[j].ds
	})
	out := make([]markGroup, 0, len(order))
	for _, k := range order {
		out = append(out, *byDs[k])
	}
	return out
}

// dsUsed is a marked dataset's recursive footprint — exactly what
// `destroy -r` would free — or -1 while unknown.
func (g markGroup) dsUsed() int64 {
	if g.h == nil {
		return -1
	}
	tree := g.h.dsTrees[poolOf(g.ds)]
	if tree == nil || tree.ByName[g.ds] == nil {
		return -1
	}
	return tree.ByName[g.ds].Used
}

// inMarkContext reports whether the cursor row belongs to the selection's
// world — a selected row, or any row of a dataset with marks in play.
func (m *Model) inMarkContext(r treeRow) bool {
	if len(m.marks) == 0 {
		return false
	}
	if r.sel {
		return true
	}
	if r.ds == nil || r.host == nil {
		return false
	}
	pfx := treeDsID(r.host, r.ds.Name)
	for id := range m.marks {
		if id == pfx || strings.HasPrefix(id, pfx+"@") || strings.HasPrefix(id, pfx+"/") {
			return true
		}
	}
	return false
}

// famTarget builds the dry-run target for a whole family.
func famTarget(container string, f *zfs.SnapFamily) string {
	if container == "" || len(f.Snaps) == 0 {
		return ""
	}
	names := make([]string, len(f.Snaps))
	for i, s := range f.Snaps {
		names[i] = s.Snap
	}
	return container + "@" + strings.Join(names, ",")
}

// bulkToggleMatches is mc's `*`, scoped to the hunt: every row the filter
// matched, one key. Everything selected → clear them; otherwise select
// the rest. Only DIRECT marks are cleared — stars inherited from a
// dataset mark the operator placed are that mark's business, not `*`'s.
func (m *Model) bulkToggleMatches() bool {
	if m.filter == "" {
		return false
	}
	var todo []treeRow
	allSel, anyDirect := true, false
	for _, r := range m.treeRows() {
		if !m.filterMarkable(r) {
			continue
		}
		todo = append(todo, r)
		if !r.sel {
			allSel = false
		}
		if m.marks[r.id] {
			anyDirect = true
		}
	}
	if len(todo) == 0 {
		return false
	}
	switch {
	case allSel && anyDirect:
		for _, r := range todo {
			delete(m.marks, r.id)
		}
	case allSel:
		return false // covered entirely by dataset marks; not ours to break
	default:
		for _, r := range todo {
			if !r.sel {
				m.toggleMark(r)
			}
		}
	}
	m.markGen++
	return true
}

// markDebounce schedules the reclaim computation a beat after the last
// mark change, so spacebar streaks cost one exec, not one per press.
func (m *Model) markDebounce() tea.Cmd {
	gen := m.markGen
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return dryTickMsg{gen: gen} })
}

// The `/` filter: one gesture, progressive reach. It prunes what is loaded
// instantly, and — because pool-recursive listing makes the fleet's whole
// snapshot universe a dozen-odd commands — it also sweeps what isn't,
// streaming matches in as each pool reports.

// sweepTTL bounds re-fetching during a hunt: retyping narrows in memory,
// a fresh sweep happens only when the data has had time to go stale.
const sweepTTL = time.Minute

// splitFilter parses the pattern grammar, which mirrors zfs naming:
// "ds@snap" — dataset part and snapshot part; a bare "@..." hunts snaps on
// any dataset; no "@" filters dataset/pool names only (no snapshot cost).
func splitFilter(pat string) (dsPat, snapPat string, hasSnap bool) {
	if i := strings.IndexByte(pat, '@'); i >= 0 {
		return pat[:i], pat[i+1:], true
	}
	return pat, "", false
}

// filterMatch is case-insensitive substring, or an anchored glob when the
// pattern carries metacharacters — families display as globs, so globs
// must just work.
func filterMatch(name, pat string) bool {
	if pat == "" {
		return true
	}
	name, pat = strings.ToLower(name), strings.ToLower(pat)
	if strings.ContainsAny(pat, "*?[") {
		ok, err := path.Match(pat, name)
		return err == nil && ok
	}
	return strings.Contains(name, pat)
}

// ensureFilterCmd fans out whatever the active filter is missing: every
// live pool's dataset tree, plus the pool-recursive snapshot sweep when
// the pattern hunts snapshots.
func (m *Model) ensureFilterCmd() tea.Cmd {
	if m.filter == "" && !m.filterIn {
		return nil
	}
	_, _, hasSnap := splitFilter(m.filter)
	var cmds []tea.Cmd
	for _, h := range m.hosts {
		if h.conn == connDown {
			continue
		}
		for _, p := range h.pools {
			cmds = append(cmds, m.ensureTreeCmd(h, p.Name))
			if !hasSnap || h.snapSweepPend[p.Name] ||
				time.Since(h.snapSweepAt[p.Name]) < sweepTTL {
				continue
			}
			h.snapSweepPend[p.Name] = true
			cmds = append(cmds, fetchSweep(h, p.Name))
		}
	}
	return tea.Batch(cmds...)
}

// sweepPending counts pools still owing the filter data — the "sweeping N"
// note in the title.
func (m *Model) sweepPending() int {
	n := 0
	for _, h := range m.hosts {
		for _, p := range h.pools {
			if h.dsTreesPend[p.Name] || h.snapSweepPend[p.Name] {
				n++
			}
		}
	}
	return n
}

// lazy-fetch helpers

func (m *Model) ensureTreeCmd(h *hostState, pool string) tea.Cmd {
	if h == nil || h.dsTrees[pool] != nil || h.dsTreesPend[pool] {
		return nil
	}
	h.dsTreesPend[pool] = true
	return fetchDatasets(h, pool)
}

func (m *Model) ensureSnapsCmd(h *hostState, name string) tea.Cmd {
	if h == nil {
		return nil
	}
	if _, ok := h.dsSnaps[name]; ok || h.dsSnapsPend[name] {
		return nil
	}
	h.dsSnapsPend[name] = true
	return fetchSnaps(h, name)
}

func (m *Model) ensurePropsCmd(h *hostState, name string) tea.Cmd {
	if h == nil {
		return nil
	}
	if _, ok := h.dsProps[name]; ok || h.dsPropsPend[name] {
		return nil
	}
	h.dsPropsPend[name] = true
	return fetchProps(h, name)
}

// ensureDryCmd runs a dry-run destroy for a target once, caching forever —
// the target string embeds the exact snapshot set, so changes mint new keys.
func (m *Model) ensureDryCmd(h *hostState, target string) tea.Cmd {
	if target == "" || h == nil {
		return nil
	}
	if _, ok := h.dryCache[target]; ok {
		return nil
	}
	h.dryCache[target] = &dryResult{pending: true}
	return fetchDryRun(h, target)
}

// markEnsureCmds fans out whatever the selection's math still owes: one
// grouped dry-run per snapshot set, a snapshot-list fetch for unresolved
// pseudos. Fired on mark changes (debounced) and re-checked by the
// datasets tick so failures and races self-heal.
func (m *Model) markEnsureCmds() []tea.Cmd {
	var cmds []tea.Cmd
	for _, g := range m.markGroups() {
		switch {
		case g.dsMark || g.h == nil:
		case g.pseudo:
			cmds = append(cmds, m.ensureSnapsCmd(g.h, g.ds))
		default:
			if t := g.target(); t != "" {
				cmds = append(cmds, m.ensureDryCmd(g.h, t))
			}
		}
	}
	return cmds
}

// messages and fetch commands

type datasetsMsg struct {
	host string
	pool string
	text string
	err  error
}
type snapsMsg struct {
	host string
	ds   string
	text string
	err  error
}
type propsMsg struct {
	host string
	ds   string
	text string
	err  error
}
type datasetsTickMsg struct{}
type dryTickMsg struct{ gen int }
type dryRunMsg struct {
	host   string
	target string
	text   string
	err    error
}
type sweepMsg struct {
	host string
	pool string
	text string
	err  error
}

func fetchSweep(h *hostState, pool string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		// snapshot-heavy pools take a beat; each pool streams in on its own
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		text, err := src.PoolSnapshotTexts(ctx, pool)
		return sweepMsg{host: host, pool: pool, text: text, err: err}
	}
}

func fetchDryRun(h *hostState, target string) tea.Cmd {
	host, src, gate := h.name, h.src, h.dryGate
	return func() tea.Msg {
		// queue politely: the mux dies at ~10 concurrent ssh sessions, and
		// a fleet-wide selection can owe dozens of dry-runs at once
		gate <- struct{}{}
		defer func() { <-gate }()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		text, err := src.DestroyDryRun(ctx, target)
		return dryRunMsg{host: host, target: target, text: text, err: err}
	}
}

func fetchDatasets(h *hostState, pool string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.DatasetTexts(ctx, pool)
		return datasetsMsg{host: host, pool: pool, text: text, err: err}
	}
}

func fetchSnaps(h *hostState, ds string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.SnapshotTexts(ctx, ds)
		return snapsMsg{host: host, ds: ds, text: text, err: err}
	}
}

func fetchProps(h *hostState, ds string) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		text, err := src.PropTexts(ctx, ds)
		return propsMsg{host: host, ds: ds, text: text, err: err}
	}
}

// exported helpers for --dump

func (m *Model) ApplyDatasets(host, pool, text string) {
	if h := m.hostByName(host); h != nil {
		h.dsTrees[pool] = zfs.ParseDatasets(text)
		delete(h.dsTreesPend, pool)
	}
}

func (m *Model) ApplySnaps(host, ds, text string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	snaps := zfs.ParseSnapshots(text, ds)
	if snaps == nil {
		snaps = []*zfs.Snapshot{}
	}
	h.dsSnaps[ds] = snaps
	delete(h.dsSnapsPend, ds)
}

func (m *Model) ApplyProps(host, ds, text string) {
	if h := m.hostByName(host); h != nil {
		h.dsProps[ds] = zfs.ParseProps(text, ds)
		delete(h.dsPropsPend, ds)
	}
}

// ApplySweep ingests one pool's recursive snapshot listing into the
// per-dataset cache (also used by --dump).
func (m *Model) ApplySweep(host, pool, text string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	for ds, snaps := range zfs.ParseAllSnapshots(text) {
		h.dsSnaps[ds] = snaps
	}
	h.snapSweepAt[pool] = time.Now()
	delete(h.snapSweepPend, pool)
}

// SetFilter activates the filter (dump helper).
func (m *Model) SetFilter(pat string) { m.filter = pat }

// ShowSnaps unfolds a dataset's snapshots into the tree — the `t` toggle,
// ancestors included. A "@label" suffix also unfolds that family (dump
// helper).
func (m *Model) ShowSnaps(host, path string) bool {
	h := m.hostByName(host)
	if h == nil {
		return false
	}
	ds := path
	if i := strings.IndexByte(path, '@'); i >= 0 {
		ds = path[:i]
		m.expanded[treeFamID(h, ds, path[i+1:])] = true
	}
	m.snapsShown[treeDsID(h, ds)] = true
	m.ExpandFor(host, ds)
	return true
}

// MarkSnaps marks snapshots on a dataset (dump helper).
func (m *Model) MarkSnaps(host, ds string, names []string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	for _, n := range names {
		n = strings.TrimSpace(strings.TrimPrefix(n, "@"))
		if n != "" {
			m.marks[treeDsID(h, ds+"@"+n)] = true
		}
	}
	m.markGen++
}

// MarkDataset marks a whole dataset (dump helper).
func (m *Model) MarkDataset(host, ds string) {
	if h := m.hostByName(host); h != nil {
		m.marks[treeDsID(h, ds)] = true
		m.markGen++
	}
}

// MarkTargets lists the grouped dry-run targets as (host, target) pairs
// (dump helper — the pipeline runs them synchronously).
func (m *Model) MarkTargets() [][2]string {
	var out [][2]string
	for _, g := range m.markGroups() {
		if t := g.target(); t != "" && g.h != nil {
			out = append(out, [2]string{g.h.name, t})
		}
	}
	return out
}

// SetCursorRow moves the tree cursor to the first visible row matching by
// name: @snap or @family label, dataset base or full path, pool, or host
// (dump helper).
func (m *Model) SetCursorRow(name string) bool {
	name = strings.TrimSpace(name)
	bare := strings.TrimPrefix(name, "@")
	for _, r := range m.treeRows() {
		hit := false
		switch r.kind {
		case rSnap:
			hit = r.snap.Snap == bare
		case rFam:
			hit = r.fam.Label() == bare
		case rDataset:
			hit = r.ds.Base() == name || r.ds.Name == name
		case rPool:
			hit = r.pool.Name == name
		case rHost:
			hit = r.host.name == name
		}
		if hit {
			m.treeSel = r.id
			if r.kind == rPool {
				m.setSel(r.host, r.pool.Name)
			}
			return true
		}
	}
	return false
}

// SelectedFamTarget returns the host and dry-run target when the cursor
// rests on a family row (for --dump prefetching).
func (m *Model) SelectedFamTarget() (string, string) {
	if r := m.treeSelected(); r.kind == rFam {
		return r.host.name, famTarget(r.ds.Name, r.fam)
	}
	return "", ""
}
