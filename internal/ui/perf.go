package ui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// The pool view's live-performance substrate. Since the great unification
// there is no performance MODE — the tree's pool panel is the one pool
// view — so these collectors simply arm while the cursor sits on a pool
// row: txg ring, dmu_tx/zil counters, module params at the 2s tick — all
// instant /proc reads, so ticks can never outrun their fetches. Per-vdev
// latency rides the host's iostat stream instead (stream.go). Leaving the
// pool lets the tick chain lapse; a generation counter keeps a stale chain
// from double-firing when the cursor returns.

const perfInterval = 2 * time.Second

type perfState struct {
	host *hostState
	pool string
	gen  int // tick-chain generation; stale chains see a mismatch and stop

	txgs      []zfs.TxgRow // the kstat ring as last read (~100 rows)
	txgHist   []zfs.TxgRow // committed txgs banked across ticks for the chart
	dmu       map[string]int64
	dmuPrev   map[string]int64
	dmuAt     time.Time
	dmuPrevAt time.Time
	zil       map[string]int64
	zilPrev   map[string]int64
	params    map[string]int64
	err       string
	have      bool
}

// Vdev latency is NOT here: the iostat stream feeds host-owned rings
// (hostState.latHist) for every pool continuously, so the window is
// already warm when the cursor arrives and survives pool switches.

// ~60s of samples at the 2s tick — enough to separate a persistent
// straggler from rotating GC noise.
const perfLatHistLen = 30

// The kernel's txg ring (zfs_txg_history) holds only ~100 rows; the dirty
// chart banks committed txgs across ticks to chart deeper than that.
const perfTxgAccum = 512

// mergeTxgs banks newly committed txgs from a ring read into the
// accumulated history. Dedupe is by txg number — the ring overlaps almost
// entirely between ticks; rows still open/quiescing get banked on a later
// read, once committed.
func mergeTxgs(hist, ring []zfs.TxgRow, cap int) []zfs.TxgRow {
	last := int64(-1)
	if len(hist) > 0 {
		last = hist[len(hist)-1].Txg
	}
	for _, r := range ring {
		if r.State == "C" && r.Txg > last {
			hist = append(hist, r)
		}
	}
	if len(hist) > cap {
		hist = hist[len(hist)-cap:]
	}
	return hist
}

type perfMsg struct {
	host   string
	pool   string
	txgs   string
	dmuTx  string
	zil    string
	params string
	err    error
}
type perfTickMsg struct{ gen int }

func fetchPerf(h *hostState, pool string) tea.Cmd {
	if h == nil {
		return nil
	}
	host, src := h.name, h.src
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		txgs, dmuTx, zil, params, err := src.PerfTexts(ctx, pool)
		return perfMsg{host: host, pool: pool, txgs: txgs, dmuTx: dmuTx, zil: zil,
			params: params, err: err}
	}
}

// ensurePerf arms the collectors for the selected pool row, resetting the
// cache when the selection moves to a different pool. Non-pool rows leave
// the cache alone (nothing renders it) and let the live chain lapse on its
// next tick.
func (m *Model) ensurePerf() tea.Cmd {
	row := m.treeSelected()
	if row.kind != rPool || row.host == nil || row.host.conn == connDown {
		return nil
	}
	if m.perf.host == row.host && m.perf.pool == row.pool.Name {
		return nil // already armed; the live chain keeps fetching
	}
	m.perf = perfState{host: row.host, pool: row.pool.Name, gen: m.perf.gen + 1}
	gen := m.perf.gen
	return tea.Batch(fetchPerf(row.host, row.pool.Name),
		tea.Tick(perfInterval, func(time.Time) tea.Msg { return perfTickMsg{gen} }))
}

func (m *Model) applyPerf(msg perfMsg) {
	if m.perf.host == nil || msg.host != m.perf.host.name || msg.pool != m.perf.pool {
		return
	}
	if msg.err != nil {
		m.perf.err = msg.err.Error()
	} else {
		m.perf.err = ""
	}
	m.perf.txgs = zfs.ParseTxgs(msg.txgs)
	m.perf.txgHist = mergeTxgs(m.perf.txgHist, m.perf.txgs, perfTxgAccum)
	m.perf.dmuPrev, m.perf.dmu = m.perf.dmu, zfs.ParseKstatMap(msg.dmuTx)
	m.perf.dmuPrevAt, m.perf.dmuAt = m.perf.dmuAt, time.Now()
	m.perf.zilPrev, m.perf.zil = m.perf.zil, zfs.ParseKstatMap(msg.zil)
	if p := zfs.ParseParams(msg.params); len(p) > 0 {
		m.perf.params = p
	}
	m.perf.have = true
}

// perfLatRings returns the armed pool's device rings from the host-owned
// stream history.
func (m *Model) perfLatRings() map[string][]zfs.VdevLat {
	if m.perf.host == nil {
		return nil
	}
	return m.perf.host.latHist[m.perf.pool]
}

// latWindow reduces a device's ring to op-weighted column averages — each
// interval votes with its op count, so three slow ops in a quiet second
// can't outvote ten thousand fast ones (equal-interval averaging slanders
// sparse-burst drives). The worst single-sample write-total stays as the
// peak, un-averaged on purpose.
func (m *Model) latWindow(name string) (avg zfs.VdevLat, peakW int64, samples int) {
	ring := m.perfLatRings()[name]
	if len(ring) == 0 {
		return zfs.VdevLat{TotalR: -1, TotalW: -1, DiskR: -1, DiskW: -1,
			SyncQR: -1, SyncQW: -1, AsyncQR: -1, AsyncQW: -1}, -1, 0
	}
	avg = zfs.OpWeightedLat(ring)
	peakW = -1
	for _, s := range ring {
		if s.TotalW > peakW {
			peakW = s.TotalW
		}
	}
	return avg, peakW, len(ring)
}

// SelectedPoolTarget reports the (host, pool) under the cursor when a pool
// row is selected — the dump pipeline feeds its perf blocks with this.
func (m *Model) SelectedPoolTarget() (host, pool string, ok bool) {
	row := m.treeSelected()
	if row.kind != rPool || row.host == nil {
		return "", "", false
	}
	return row.host.name, row.pool.Name, true
}

// ApplyPerf ingests raw perf texts outside the tea loop (dump helper); it
// pins the cache identity first so the pool panel renders the blocks.
func (m *Model) ApplyPerf(host, pool, txgs, dmuTx, zil, params string, err error) {
	if h := m.hostByName(host); h != nil && (m.perf.host != h || m.perf.pool != pool) {
		m.perf = perfState{host: h, pool: pool}
	}
	m.applyPerf(perfMsg{host: host, pool: pool, txgs: txgs, dmuTx: dmuTx, zil: zil,
		params: params, err: err})
}

// perfRate computes a per-second delta for a counter across the last two
// samples.
func (m *Model) perfRate(cur, prev map[string]int64, key string) float64 {
	if prev == nil {
		return 0
	}
	dt := m.perf.dmuAt.Sub(m.perf.dmuPrevAt).Seconds()
	if dt <= 0 {
		return 0
	}
	d := cur[key] - prev[key]
	if d < 0 {
		return 0
	}
	return float64(d) / dt
}
