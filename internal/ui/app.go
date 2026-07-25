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

// HostSpec names one host for the Model: a display name, the ssh
// destination ("" for the local host), and its collector.
type HostSpec struct {
	Name string
	Dest string
	Src  collect.Source
}

type Model struct {
	hosts     []*hostState
	multiHost bool // any remote registered: host rows appear everywhere

	w, h int
	mode int
	br   browserState

	// tree screen state
	treeSel      string          // row id: "≡", "h:<host>", "p:<host>\x00<pool>", or "<host>\x00<dataset>"
	expanded     map[string]bool // same id scheme
	treeSortUsed bool

	perf    perfState
	perfMem map[string]string // per-host remembered perf pool

	selName string // current inspector pool ("host\x00pool"), for elastic resets
	fleetW  map[string]int

	// right-panel scrolling: j/k adjust the offset; the key identifies the
	// panel's content so any context change resets to the top
	panelScroll   int
	panelKey      string
	rightOverflow bool
}

func New(specs []HostSpec, multiHost bool) *Model {
	m := &Model{
		multiHost:    multiHost,
		br:           newBrowserState(nil, ""),
		treeSortUsed: true,
		expanded:     map[string]bool{},
		perfMem:      map[string]string{},
		fleetW:       map[string]int{},
	}
	for _, s := range specs {
		m.hosts = append(m.hosts, newHostState(s.Name, s.Dest, s.Src))
	}
	return m
}

func (m *Model) hostByName(name string) *hostState {
	for _, h := range m.hosts {
		if h.name == name {
			return h
		}
	}
	return nil
}

// setSel notes the pool whose inspector is showing, resetting its elastic
// readout widths on change.
func (m *Model) setSel(h *hostState, pool string) {
	key := h.name + "\x00" + pool
	if key != m.selName {
		m.selName = key
		h.ioW = map[string]int{}
	}
}

type poolsDataMsg struct {
	host      string
	pools     []*zfs.Pool
	rootsText string
	propsText string
	err       error
}
type statsDataMsg struct {
	host       string
	arcText    string
	ioText     string
	objsetText string
	uptime     string
	loadavg    string
	procStat   string
	hwmon      string
	err        error
}
type infoMsg struct {
	host   string
	zfsVer string
	kernel string
	osRel  string
}
type poolsTickMsg struct{}
type statsTickMsg struct{}

func fetchPools(h *hostState) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, list, err := src.PoolTexts(ctx)
		if err != nil {
			return poolsDataMsg{host: host, err: err}
		}
		pools := zfs.ParseZpoolStatus(status)
		zfs.AttachListNumbers(pools, list)
		// best-effort extras; the overhead line simply doesn't render
		// when either is unavailable
		roots, _ := src.RootTexts(ctx)
		props, _ := src.PoolProps(ctx)
		return poolsDataMsg{host: host, pools: pools, rootsText: roots, propsText: props}
	}
}

func fetchStats(h *hostState) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		arc, iostat, objsets, err := src.StatTexts(ctx)
		msg := statsDataMsg{host: host, arcText: arc, ioText: iostat, objsetText: objsets, err: err}
		if err == nil {
			msg.uptime, msg.loadavg, msg.procStat, msg.hwmon = src.HostTexts(ctx)
		}
		return msg
	}
}

func fetchInfo(h *hostState) tea.Cmd {
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ver, kernel, osrel := src.InfoTexts(ctx)
		return infoMsg{host: host, zfsVer: ver, kernel: kernel, osRel: osrel}
	}
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.Tick(poolsInterval, func(time.Time) tea.Msg { return poolsTickMsg{} }),
		tea.Tick(statsInterval, func(time.Time) tea.Msg { return statsTickMsg{} }),
		tea.Tick(datasetsInterval, func(time.Time) tea.Msg { return datasetsTickMsg{} }),
	}
	for _, h := range m.hosts {
		h.poolsPend, h.statsPend, h.infoPend = true, true, true
		cmds = append(cmds, fetchPools(h), fetchStats(h), fetchInfo(h))
	}
	return tea.Batch(cmds...)
}

// ApplyPoolData ingests parsed pools for a host (also used by --dump).
func (m *Model) ApplyPoolData(host string, pools []*zfs.Pool) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	h.pools = pools
	if m.treeSel == "" && len(pools) > 0 {
		// land on the overview — the fleet answer — as the original
		// round-1 concept intended
		m.treeSel = overviewID
	}
	if h.ioText != "" {
		h.io = zfs.ParseIostatPools(h.ioText, h.poolNames())
	}
}

// ApplyAuxPools ingests the per-pool root totals and ashift values that
// feed the allocation-overhead line.
func (m *Model) ApplyAuxPools(host, rootsText, propsText string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	if rootsText != "" {
		h.rootStats = zfs.ParseRootStats(rootsText)
	}
	if propsText != "" {
		h.ashift = zfs.ParsePoolAshift(propsText)
	}
}

// ApplyStatData ingests raw stat text for a host (also used by --dump).
func (m *Model) ApplyStatData(host, arcText, ioText, objsetText string) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	if strings.TrimSpace(arcText) != "" {
		h.arcPrev = h.arc
		h.arc = zfs.ParseArcstats(arcText)
		h.arcMapPrev, h.arcMap = h.arcMap, zfs.ParseKstatMap(arcText)
		h.arcPrevAt, h.arcAt = h.arcAt, time.Now()
		h.haveArc = true
		if dh, dm := h.arc.Hits-h.arcPrev.Hits, h.arc.Misses-h.arcPrev.Misses; dh+dm > 0 {
			h.hitHist = append(h.hitHist, 1000*dh/(dh+dm))
			if len(h.hitHist) > dsIOHistLen {
				h.hitHist = h.hitHist[len(h.hitHist)-dsIOHistLen:]
			}
		}
	}
	h.ioText = ioText
	if len(h.pools) > 0 {
		h.io = zfs.ParseIostatPools(ioText, h.poolNames())
		var agg zfs.IORates
		for name, r := range h.io {
			hist := append(h.ioHist[name], r)
			if len(hist) > dsIOHistLen {
				hist = hist[len(hist)-dsIOHistLen:]
			}
			h.ioHist[name] = hist
			agg.RBw += r.RBw
			agg.WBw += r.WBw
		}
		h.hostIOHist = append(h.hostIOHist, agg)
		if len(h.hostIOHist) > dsIOHistLen {
			h.hostIOHist = h.hostIOHist[len(h.hostIOHist)-dsIOHistLen:]
		}
	}
	if objsetText != "" {
		h.applyObjsets(zfs.ParseObjsets(objsetText))
	}
}

// ApplyHostVitals ingests the HostTexts surfaces (also used by --dump).
func (m *Model) ApplyHostVitals(host, uptime, loadavg, stat, hwmon string) {
	if h := m.hostByName(host); h != nil {
		h.applyVitals(uptime, loadavg, stat, hwmon)
	}
}

// ApplyHostInfo ingests the identity surfaces (also used by --dump).
func (m *Model) ApplyHostInfo(host, zfsVer, kernel, osRel string) {
	if h := m.hostByName(host); h != nil {
		h.applyInfo(zfsVer, kernel, osRel)
	}
}

// MarkHostLive forces the connection state to live (dump helper — dumps
// have no tick loop to establish it).
func (m *Model) MarkHostLive(host string) {
	if h := m.hostByName(host); h != nil {
		h.noteStatsOK()
	}
}

// 64 samples at the 2s stat tick ≈ a two-minute window; braille sparklines
// pack two samples per cell, so a full-width line shows all of it
const dsIOHistLen = 64

// SetSize sets the frame size directly (used by --dump).
func (m *Model) SetSize(w, h int) { m.w, m.h = w, h }

// SetSelected moves the tree cursor to "overview", a host name, a pool, or
// a dataset path on the given host (used by --dump --select).
func (m *Model) SetSelected(host, name string) {
	if name == "overview" {
		m.treeSel = overviewID
		return
	}
	h := m.hostByName(host)
	if h == nil {
		return
	}
	if name == "" || name == h.name {
		m.treeSel = treeHostID(h)
		return
	}
	for _, p := range h.pools {
		if p.Name == name {
			m.treeSel = treePoolID(h, name)
			m.setSel(h, name)
			return
		}
	}
	if tree := h.dsTrees[poolOf(name)]; tree != nil && tree.ByName[name] != nil {
		m.treeSel = treeDsID(h, name)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, nil

	case poolsDataMsg:
		h := m.hostByName(msg.host)
		if h == nil {
			return m, nil
		}
		h.poolsPend = false
		if msg.err != nil {
			h.lastErr = msg.err
		} else {
			h.lastErr = nil
			m.ApplyPoolData(msg.host, msg.pools)
			m.ApplyAuxPools(msg.host, msg.rootsText, msg.propsText)
		}
		return m, nil

	case statsDataMsg:
		h := m.hostByName(msg.host)
		if h == nil {
			return m, nil
		}
		h.statsPend = false
		if msg.err != nil {
			h.noteStatsFail(msg.err)
			return m, nil
		}
		h.noteStatsOK()
		m.ApplyStatData(msg.host, msg.arcText, msg.ioText, msg.objsetText)
		h.applyVitals(msg.uptime, msg.loadavg, msg.procStat, msg.hwmon)
		return m, nil

	case infoMsg:
		h := m.hostByName(msg.host)
		if h == nil {
			return m, nil
		}
		h.infoPend = false
		h.applyInfo(msg.zfsVer, msg.kernel, msg.osRel)
		return m, nil

	case poolsTickMsg:
		cmds := []tea.Cmd{tea.Tick(poolsInterval, func(time.Time) tea.Msg { return poolsTickMsg{} })}
		for _, h := range m.hosts {
			if h.poolsPend || (h.conn == connDown && time.Now().Before(h.nextTry)) {
				continue
			}
			h.poolsPend = true
			cmds = append(cmds, fetchPools(h))
		}
		return m, tea.Batch(cmds...)

	case statsTickMsg:
		cmds := []tea.Cmd{tea.Tick(statsInterval, func(time.Time) tea.Msg { return statsTickMsg{} })}
		for _, h := range m.hosts {
			if h.statsPend || (h.conn == connDown && time.Now().Before(h.nextTry)) {
				continue
			}
			h.statsPend = true
			cmds = append(cmds, fetchStats(h))
			if h.conn == connLive && !h.haveInfo && !h.infoPend {
				h.infoPend = true
				cmds = append(cmds, fetchInfo(h))
			}
		}
		return m, tea.Batch(cmds...)

	case datasetsMsg:
		h := m.hostByName(msg.host)
		if h == nil {
			return m, nil
		}
		delete(h.dsTreesPend, msg.pool)
		if msg.err != nil {
			h.lastErr = msg.err
		} else {
			h.dsTrees[msg.pool] = zfs.ParseDatasets(msg.text)
		}
		if m.mode == modeBrowser {
			return m, m.brEnsure()
		}
		return m, m.treeEnsure()

	case snapsMsg:
		if h := m.hostByName(msg.host); h != nil {
			delete(h.dsSnapsPend, msg.ds)
			if msg.err == nil {
				m.ApplySnaps(msg.host, msg.ds, msg.text)
			}
		}
		return m, nil

	case propsMsg:
		if h := m.hostByName(msg.host); h != nil {
			delete(h.dsPropsPend, msg.ds)
			if msg.err == nil {
				m.ApplyProps(msg.host, msg.ds, msg.text)
			}
		}
		return m, nil

	case datasetsTickMsg:
		cmds := []tea.Cmd{tea.Tick(datasetsInterval, func(time.Time) tea.Msg { return datasetsTickMsg{} })}
		type needKey struct {
			h    *hostState
			pool string
		}
		need := map[needKey]bool{}
		if m.mode == modeBrowser && m.br.host != nil && m.br.pool != "" {
			need[needKey{m.br.host, m.br.pool}] = true
		}
		for id := range m.expanded {
			if h, pool, ok := splitPoolID(m, id); ok {
				need[needKey{h, pool}] = true
			}
		}
		for k := range need {
			if !k.h.dsTreesPend[k.pool] {
				k.h.dsTreesPend[k.pool] = true
				cmds = append(cmds, fetchDatasets(k.h, k.pool))
			}
		}
		if m.mode == modeBrowser && m.br.host != nil {
			if c := m.brContainer(); c != nil && !m.br.host.dsSnapsPend[c.Name] {
				m.br.host.dsSnapsPend[c.Name] = true
				cmds = append(cmds, fetchSnaps(m.br.host, c.Name))
			}
		}
		return m, tea.Batch(cmds...)

	case dryTickMsg:
		if msg.gen == m.br.selGen && len(m.br.selSnaps) > 0 {
			return m, m.ensureDryCmd(m.SelectionTarget())
		}
		return m, nil

	case dryRunMsg:
		if h := m.hostByName(msg.host); h != nil {
			if r := h.dryCache[msg.target]; r != nil {
				r.pending = false
				r.text = msg.text
				if msg.err != nil {
					r.errText = msg.err.Error()
				}
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
		return m, tea.Batch(fetchPerf(m.perf.host, m.perf.pool),
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
func (m *Model) ApplyDryRun(host, target, text string, err error) {
	h := m.hostByName(host)
	if h == nil {
		return
	}
	r := &dryResult{text: text}
	if err != nil {
		r.errText = err.Error()
	}
	h.dryCache[target] = r
}

func (m *Model) View() string {
	return frame(m)
}
