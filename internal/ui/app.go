package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/zfs"
)

const (
	poolsInterval = 5 * time.Second
	statsInterval = 2 * time.Second
)

type Model struct {
	src collect.Source

	w, h int
	mode int
	br   browserState

	// tree screen state
	treeSel      string          // row id: "≡", "p:<pool>", or dataset name
	expanded     map[string]bool // same id scheme
	treeSortUsed bool

	// shared per-dataset caches, used by both the tree screen and the
	// drill browser
	dsTrees     map[string]*zfs.DatasetTree
	dsTreesPend map[string]bool
	dsSnaps     map[string][]*zfs.Snapshot // empty non-nil = "none"
	dsSnapsPend map[string]bool
	dsProps     map[string]map[string]zfs.Prop
	dsPropsPend map[string]bool
	dryCache    map[string]*dryResult // dry-run destroy results by target

	pools      []*zfs.Pool
	rootStats  map[string]zfs.RootStat
	ashift     map[string]int
	arc        zfs.ArcStats
	arcPrev    zfs.ArcStats
	haveArc    bool
	arcMap     map[string]int64 // full arcstats for the perf screen
	arcMapPrev map[string]int64
	arcAt      time.Time
	arcPrevAt  time.Time
	hitHist    []int64 // rolling hit%×10 ring
	io         map[string]zfs.IORates
	ioHist     map[string][]zfs.IORates // pool-level ring for sparklines
	ioText     string

	perf perfState

	// per-dataset io from objset kstat deltas
	dsIO       map[string]zfs.IORates
	dsIOHist   map[string][]zfs.IORates // ring of recent samples, newest last
	objsetPrev map[string]zfs.ObjsetIO
	objsetAt   time.Time

	selName   string
	expandAll bool
	lastErr   error

	// grow-only readout widths; ioW is per-pool (reset on selection change),
	// stripW lives for the session
	ioW    map[string]int
	stripW map[string]int
}

func New(src collect.Source) *Model {
	return &Model{
		src:          src,
		br:           newBrowserState(""),
		treeSortUsed: true,
		expanded:     map[string]bool{},
		dsTrees:      map[string]*zfs.DatasetTree{},
		dsTreesPend:  map[string]bool{},
		dsSnaps:      map[string][]*zfs.Snapshot{},
		dsSnapsPend:  map[string]bool{},
		dsProps:      map[string]map[string]zfs.Prop{},
		dsPropsPend:  map[string]bool{},
		dryCache:     map[string]*dryResult{},
		rootStats:    map[string]zfs.RootStat{},
		ashift:       map[string]int{},
		io:           map[string]zfs.IORates{},
		ioHist:       map[string][]zfs.IORates{},
		dsIO:         map[string]zfs.IORates{},
		dsIOHist:     map[string][]zfs.IORates{},
		ioW:          map[string]int{},
		stripW:       map[string]int{},
	}
}

func (m *Model) setSel(name string) {
	if name != m.selName {
		m.selName = name
		m.ioW = map[string]int{}
	}
}

type poolsDataMsg struct {
	pools     []*zfs.Pool
	rootsText string
	propsText string
	err       error
}
type statsDataMsg struct {
	arcText    string
	ioText     string
	objsetText string
	err        error
}
type poolsTickMsg struct{}
type statsTickMsg struct{}

func fetchPools(src collect.Source) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, list, err := src.PoolTexts(ctx)
		if err != nil {
			return poolsDataMsg{err: err}
		}
		pools := zfs.ParseZpoolStatus(status)
		zfs.AttachListNumbers(pools, list)
		// best-effort extras; the overhead line simply doesn't render
		// when either is unavailable
		roots, _ := src.RootTexts(ctx)
		props, _ := src.PoolProps(ctx)
		return poolsDataMsg{pools: pools, rootsText: roots, propsText: props}
	}
}

func fetchStats(src collect.Source) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		arc, iostat, objsets, err := src.StatTexts(ctx)
		return statsDataMsg{arcText: arc, ioText: iostat, objsetText: objsets, err: err}
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(fetchPools(m.src), fetchStats(m.src),
		tea.Tick(datasetsInterval, func(time.Time) tea.Msg { return datasetsTickMsg{} }))
}

func (m *Model) poolNames() map[string]bool {
	names := map[string]bool{}
	for _, p := range m.pools {
		names[p.Name] = true
	}
	return names
}

func (m *Model) selIdx() int {
	for i, p := range m.pools {
		if p.Name == m.selName {
			return i
		}
	}
	return 0
}

// ApplyPoolData ingests parsed pools outside the tea loop (used by --dump).
func (m *Model) ApplyPoolData(pools []*zfs.Pool) {
	m.pools = pools
	if m.treeSel == "" && len(pools) > 0 {
		// land on the overview — the fleet answer — as the original
		// round-1 concept intended
		m.treeSel = overviewID
	}
	if m.ioText != "" {
		m.io = zfs.ParseIostatPools(m.ioText, m.poolNames())
	}
}

// ApplyAuxPools ingests the per-pool root totals and ashift values that
// feed the allocation-overhead line.
func (m *Model) ApplyAuxPools(rootsText, propsText string) {
	if rootsText != "" {
		m.rootStats = zfs.ParseRootStats(rootsText)
	}
	if propsText != "" {
		m.ashift = zfs.ParsePoolAshift(propsText)
	}
}

// poolGeometry returns the data-vdev raidz shape of a pool, when it has one.
func (m *Model) poolGeometry(pool string) (width, parity, ashift int, ok bool) {
	ashift, haveShift := m.ashift[pool]
	if !haveShift {
		return 0, 0, 0, false
	}
	for _, p := range m.pools {
		if p.Name != pool {
			continue
		}
		data := p.Class("data")
		if data == nil || len(data.Vdevs) == 0 {
			return 0, 0, 0, false
		}
		v := data.Vdevs[0]
		par, isRaidz := zfs.RaidzShape(v.Name)
		if !isRaidz || len(v.Children) <= par {
			return 0, 0, 0, false
		}
		return len(v.Children), par, ashift, true
	}
	return 0, 0, 0, false
}

// ApplyStatData ingests raw stat text outside the tea loop (used by --dump).
func (m *Model) ApplyStatData(arcText, ioText, objsetText string) {
	if strings.TrimSpace(arcText) != "" {
		m.arcPrev = m.arc
		m.arc = zfs.ParseArcstats(arcText)
		m.arcMapPrev, m.arcMap = m.arcMap, zfs.ParseKstatMap(arcText)
		m.arcPrevAt, m.arcAt = m.arcAt, time.Now()
		m.haveArc = true
		if dh, dm := m.arc.Hits-m.arcPrev.Hits, m.arc.Misses-m.arcPrev.Misses; dh+dm > 0 {
			m.hitHist = append(m.hitHist, 1000*dh/(dh+dm))
			if len(m.hitHist) > dsIOHistLen {
				m.hitHist = m.hitHist[len(m.hitHist)-dsIOHistLen:]
			}
		}
	}
	m.ioText = ioText
	if len(m.pools) > 0 {
		m.io = zfs.ParseIostatPools(ioText, m.poolNames())
		for name, r := range m.io {
			hist := append(m.ioHist[name], r)
			if len(hist) > dsIOHistLen {
				hist = hist[len(hist)-dsIOHistLen:]
			}
			m.ioHist[name] = hist
		}
	}
	if objsetText != "" {
		m.applyObjsets(zfs.ParseObjsets(objsetText))
	}
}

// 32 samples at the 2s stat tick ≈ a one-minute sparkline window
const dsIOHistLen = 32

// applyObjsets turns cumulative objset counters into rates against the
// previous sample and appends them to each dataset's history ring.
// Counters reset when a dataset reloads, so negative deltas clamp to zero.
func (m *Model) applyObjsets(cur map[string]zfs.ObjsetIO) {
	now := time.Now()
	dt := now.Sub(m.objsetAt).Seconds()
	if m.objsetPrev != nil && dt > 0.2 {
		rates := map[string]zfs.IORates{}
		for name, c := range cur {
			p, ok := m.objsetPrev[name]
			if !ok {
				continue
			}
			clamp := func(d int64) int64 {
				if d < 0 {
					return 0
				}
				return int64(float64(d) / dt)
			}
			r := zfs.IORates{
				ROps: clamp(c.Reads - p.Reads),
				WOps: clamp(c.Writes - p.Writes),
				RBw:  clamp(c.NRead - p.NRead),
				WBw:  clamp(c.NWritten - p.NWritten),
			}
			rates[name] = r
			hist := append(m.dsIOHist[name], r)
			if len(hist) > dsIOHistLen {
				hist = hist[len(hist)-dsIOHistLen:]
			}
			m.dsIOHist[name] = hist
		}
		m.dsIO = rates
	}
	m.objsetPrev = cur
	m.objsetAt = now
}

// SetSize sets the frame size directly (used by --dump).
func (m *Model) SetSize(w, h int) { m.w, m.h = w, h }

// SetSelected moves the tree cursor to "overview" or a named pool (used by
// --dump --select).
func (m *Model) SetSelected(name string) {
	if name == "overview" {
		m.treeSel = overviewID
		return
	}
	for _, p := range m.pools {
		if p.Name == name {
			m.treeSel = treePoolID(name)
			m.setSel(name)
			return
		}
	}
	if tree := m.dsTrees[poolOf(name)]; tree != nil && tree.ByName[name] != nil {
		m.treeSel = name
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case poolsDataMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.lastErr = nil
			m.ApplyPoolData(msg.pools)
			m.ApplyAuxPools(msg.rootsText, msg.propsText)
		}
		return m, tea.Tick(poolsInterval, func(time.Time) tea.Msg { return poolsTickMsg{} })

	case statsDataMsg:
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.ApplyStatData(msg.arcText, msg.ioText, msg.objsetText)
		}
		return m, tea.Tick(statsInterval, func(time.Time) tea.Msg { return statsTickMsg{} })

	case poolsTickMsg:
		return m, fetchPools(m.src)

	case statsTickMsg:
		return m, fetchStats(m.src)

	case datasetsMsg:
		delete(m.dsTreesPend, msg.pool)
		if msg.err != nil {
			m.lastErr = msg.err
		} else {
			m.dsTrees[msg.pool] = zfs.ParseDatasets(msg.text)
		}
		if m.mode == modeBrowser {
			return m, m.brEnsure()
		}
		return m, m.treeEnsure()

	case snapsMsg:
		delete(m.dsSnapsPend, msg.ds)
		if msg.err == nil {
			m.ApplySnaps(msg.ds, msg.text)
		}
		return m, nil

	case propsMsg:
		delete(m.dsPropsPend, msg.ds)
		if msg.err == nil {
			m.ApplyProps(msg.ds, msg.text)
		}
		return m, nil

	case datasetsTickMsg:
		cmds := []tea.Cmd{tea.Tick(datasetsInterval, func(time.Time) tea.Msg { return datasetsTickMsg{} })}
		need := map[string]bool{}
		if m.mode == modeBrowser && m.br.pool != "" {
			need[m.br.pool] = true
		}
		for id := range m.expanded {
			if strings.HasPrefix(id, "p:") {
				need[strings.TrimPrefix(id, "p:")] = true
			}
		}
		for pool := range need {
			if !m.dsTreesPend[pool] {
				m.dsTreesPend[pool] = true
				cmds = append(cmds, fetchDatasets(m.src, pool))
			}
		}
		if m.mode == modeBrowser {
			if c := m.brContainer(); c != nil && !m.dsSnapsPend[c.Name] {
				m.dsSnapsPend[c.Name] = true
				cmds = append(cmds, fetchSnaps(m.src, c.Name))
			}
		}
		return m, tea.Batch(cmds...)

	case dryTickMsg:
		if msg.gen == m.br.selGen && len(m.br.selSnaps) > 0 {
			return m, m.ensureDryCmd(m.SelectionTarget())
		}
		return m, nil

	case dryRunMsg:
		if r := m.dryCache[msg.target]; r != nil {
			r.pending = false
			r.text = msg.text
			if msg.err != nil {
				r.errText = msg.err.Error()
			}
		}
		return m, nil

	case perfMsg:
		m.applyPerf(msg)
		return m, nil

	case perfTickMsg:
		if m.mode != modePerf {
			return m, nil
		}
		return m, tea.Batch(fetchPerf(m.src, m.perf.pool),
			tea.Tick(perfInterval, func(time.Time) tea.Msg { return perfTickMsg{} }))

	case tea.KeyMsg:
		switch m.mode {
		case modePerf:
			return m.perfKeys(msg)
		case modeBrowser:
			if msg.String() == "p" && !m.br.filterIn {
				return m, m.enterPerf()
			}
			return m.browserKeys(msg)
		default:
			if msg.String() == "p" {
				return m, m.enterPerf()
			}
			return m.treeKeys(msg)
		}
	}
	return m, nil
}

// MarkSnaps selects snapshots on the current browser level (dump helper).
func (m *Model) MarkSnaps(names []string) {
	for _, n := range names {
		n = strings.TrimSpace(strings.TrimPrefix(n, "@"))
		if n != "" {
			m.br.selSnaps[n] = true
		}
	}
	m.br.selGen++
}

// ApplyDryRun stores a dry-run result for a target (dump helper).
func (m *Model) ApplyDryRun(target, text string, err error) {
	r := &dryResult{text: text}
	if err != nil {
		r.errText = err.Error()
	}
	m.dryCache[target] = r
}

func (m *Model) View() string {
	return frame(m)
}
