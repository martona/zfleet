package ui

import (
	"strings"
	"time"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/zfs"
)

// hostState is everything zfse knows about one host: its collector, its
// connection lifecycle, identity, vitals, and every per-host data cache
// that lived directly on Model in the single-host era. The single-host
// view is simply a fleet of one with the host chrome hidden.

type hostConn int

const (
	connWaiting hostConn = iota // no successful fetch yet
	connLive
	connDown
)

// a live host goes stale-dark after this many consecutive failed stat
// fetches (~6s at the 2s tick); reconnect attempts then back off 5s→30s
const (
	connFailLimit = 3
	backoffMin    = 5 * time.Second
	backoffMax    = 30 * time.Second
)

type hostState struct {
	name string // display name: first hostname label, uniquified
	dest string // ssh destination; "" = the local Exec host
	src  collect.Source

	// connection lifecycle
	conn      hostConn
	lastOK    time.Time // last successful stats fetch
	firstFail time.Time // when the current outage began
	failCount int
	backoff   time.Duration
	nextTry   time.Time
	statsPend bool
	poolsPend bool
	infoPend  bool
	errText   string
	lastErr   error

	// identity, collected once per successful connect
	haveInfo bool
	osName   string
	kernel   string
	zfsVer   string
	zfsKmod  string

	// vitals, riding the stats tick
	uptimeSec int64
	load1     string
	cpuBusy   int64
	cpuTotal  int64
	cpuPct    int // -1 until two samples bracket a delta
	tempC     int
	tempSrc   string
	haveTemp  bool

	// per-host ZFS data
	pools      []*zfs.Pool
	rootStats  map[string]zfs.RootStat
	ashift     map[string]int
	arc        zfs.ArcStats
	arcPrev    zfs.ArcStats
	haveArc    bool
	arcMap     map[string]int64
	arcMapPrev map[string]int64
	arcAt      time.Time
	arcPrevAt  time.Time
	hitHist    []int64
	io         map[string]zfs.IORates
	ioHist     map[string][]zfs.IORates
	ioText     string
	hostIOHist []zfs.IORates // aggregate ring for the host row's sparklines

	// per-dataset io from objset kstat deltas
	dsIO       map[string]zfs.IORates
	dsIOHist   map[string][]zfs.IORates
	objsetPrev map[string]zfs.ObjsetIO
	objsetAt   time.Time

	// disk layer: inventory, alias/block maps for vdev resolution, and
	// the hwmon chip→disk joins that turn chip readings into drive temps
	disks      []zfs.Disk
	aliases    map[string]string
	blocks     map[string]zfs.BlockNode
	chipDisk   map[string]string // hwmon dir → disk node
	vdevDisk   map[string]string // status leaf name → disk node
	chips      []zfs.HwmonChip   // latest hwmon readings, all chips
	smart      map[string]zfs.Smart
	sudoOK     bool
	sudoProbed bool
	diskPend   bool
	haveDisks  bool

	// dataset caches shared by the tree screen and the drill browser
	dsTrees     map[string]*zfs.DatasetTree
	dsTreesPend map[string]bool
	dsSnaps     map[string][]*zfs.Snapshot // empty non-nil = "none"
	dsSnapsPend map[string]bool
	dsProps     map[string]map[string]zfs.Prop
	dsPropsPend map[string]bool
	dryCache    map[string]*dryResult

	// fleet-sweep bookkeeping (the / filter): per-pool recursive snapshot
	// fetches, TTL-cached so retyping narrows in memory
	snapSweepAt   map[string]time.Time
	snapSweepPend map[string]bool

	// grow-only readout widths; ioW is per-pool (reset on selection
	// change), stripW lives for the session
	ioW    map[string]int
	stripW map[string]int
}

func newHostState(name, dest string, src collect.Source) *hostState {
	return &hostState{
		name:          name,
		dest:          dest,
		src:           src,
		cpuPct:        -1,
		rootStats:     map[string]zfs.RootStat{},
		ashift:        map[string]int{},
		io:            map[string]zfs.IORates{},
		ioHist:        map[string][]zfs.IORates{},
		dsIO:          map[string]zfs.IORates{},
		dsIOHist:      map[string][]zfs.IORates{},
		dsTrees:       map[string]*zfs.DatasetTree{},
		dsTreesPend:   map[string]bool{},
		dsSnaps:       map[string][]*zfs.Snapshot{},
		dsSnapsPend:   map[string]bool{},
		dsProps:       map[string]map[string]zfs.Prop{},
		dsPropsPend:   map[string]bool{},
		dryCache:      map[string]*dryResult{},
		snapSweepAt:   map[string]time.Time{},
		snapSweepPend: map[string]bool{},
		ioW:           map[string]int{},
		stripW:        map[string]int{},
	}
}

func (h *hostState) poolNames() map[string]bool {
	names := map[string]bool{}
	for _, p := range h.pools {
		names[p.Name] = true
	}
	return names
}

func (h *hostState) pool(name string) *zfs.Pool {
	for _, p := range h.pools {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// noteStatsOK / noteStatsFail run the connection state machine off the
// stats tick — the heartbeat every host answers every 2s when healthy.
func (h *hostState) noteStatsOK() {
	h.conn = connLive
	h.lastOK = time.Now()
	h.failCount = 0
	h.backoff = 0
	h.nextTry = time.Time{}
	h.errText = ""
}

func (h *hostState) noteStatsFail(err error) {
	h.failCount++
	h.errText = err.Error()
	down := h.conn == connWaiting || // unreachable from the start
		(h.conn == connLive && h.failCount >= connFailLimit) ||
		h.conn == connDown
	if !down {
		return // a live host rides out a couple of missed ticks
	}
	if h.conn != connDown {
		h.conn = connDown
		h.firstFail = time.Now()
	}
	if h.dest == "" {
		return // the local host retries at tick cadence — no ssh to spare
	}
	if h.backoff < backoffMin {
		h.backoff = backoffMin
	} else if h.backoff *= 2; h.backoff > backoffMax {
		h.backoff = backoffMax
	}
	h.nextTry = time.Now().Add(h.backoff)
}

// sev is the host's alarm tier: unreachable is an ERROR outright; a
// reachable host inherits the worst of its pools and its drives — a
// warning boot disk belongs to no pool but still belongs to the host.
func (h *hostState) sev() int {
	if h.conn == connDown {
		return sevErr
	}
	s := sevOK
	for _, p := range h.pools {
		if v := h.poolSevFull(p); v > s {
			s = v
		}
	}
	for _, sm := range h.smart {
		if v := smartSev(sm); v > s {
			s = v
		}
	}
	return s
}

// outageAge is how long the host has been dark.
func (h *hostState) outageAge() time.Duration {
	if h.firstFail.IsZero() {
		return 0
	}
	return time.Since(h.firstFail)
}

// applyVitals ingests the HostTexts surfaces.
func (h *hostState) applyVitals(uptime, loadavg, stat, hwmon string) {
	if v := zfs.ParseUptime(uptime); v > 0 {
		h.uptimeSec = v
	}
	if v := zfs.ParseLoad1(loadavg); v != "" {
		h.load1 = v
	}
	if busy, total := zfs.ParseCPUStat(stat); total > 0 {
		if h.cpuTotal > 0 && total > h.cpuTotal {
			pct := 100 * (busy - h.cpuBusy) / (total - h.cpuTotal)
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
			h.cpuPct = int(pct)
		}
		h.cpuBusy, h.cpuTotal = busy, total
	}
	if chips := zfs.ParseHwmon(hwmon); len(chips) > 0 {
		h.chips = chips
		h.refreshTemps()
	}
}

// refreshTemps distributes the latest chip readings: drive-linked chips
// update their disk's temperature (smartctl fills in where hwmon is
// blind — HBAs, spinners without drivetemp), and the host line takes the
// hottest reading across sensors and drives alike — whatever is cooking,
// it is a named, explorable row in the host view.
func (h *hostState) refreshTemps() {
	best, bestSrc := -1, ""
	for i := range h.disks {
		h.disks[i].TempC = -1
		if s, ok := h.smart[h.disks[i].Node]; ok && s.TempC > 0 {
			h.disks[i].TempC, h.disks[i].TempSrc = s.TempC, "smart"
		}
	}
	for _, c := range h.chips {
		mc := c.MaxC()
		if mc < 0 {
			continue
		}
		if node, ok := h.chipDisk[c.Dir]; ok {
			for i := range h.disks {
				if h.disks[i].Node == node && mc > h.disks[i].TempC {
					h.disks[i].TempC, h.disks[i].TempSrc = mc, c.Name
				}
			}
			continue
		}
		if mc > best {
			best, bestSrc = mc, c.Name
			if zfs.IsCPUChip(c.Name) {
				bestSrc = "cpu"
			}
		}
	}
	for _, d := range h.disks {
		if d.TempC > best {
			best, bestSrc = d.TempC, d.Node
		}
	}
	if best >= 0 {
		h.tempC, h.tempSrc, h.haveTemp = best, bestSrc, true
	}
}

// applySmart ingests per-drive smartctl output and the sudo probe result.
func (h *hostState) applySmart(texts map[string]string, sudoOK bool) {
	h.sudoProbed = true
	h.sudoOK = sudoOK
	h.smart = map[string]zfs.Smart{}
	for node, text := range texts {
		if s, ok := zfs.ParseSmart(text); ok {
			h.smart[node] = s
		}
	}
	h.refreshTemps()
}

// smartSev is a drive's alarm tier: a failed self-assessment is an ERROR,
// any critical counter is a WARN — the leading indicator zpool's lagging
// counters can't give.
func smartSev(s zfs.Smart) int {
	switch {
	case s.HaveStatus && !s.Passed:
		return sevErr
	case len(s.Warns) > 0:
		return sevWarn
	}
	return sevOK
}

// poolSevFull extends poolSev with the pre-failure tier: the health of the
// physical drives its vdevs stand on.
func (h *hostState) poolSevFull(p *zfs.Pool) int {
	s := poolSev(p)
	for _, c := range p.Classes {
		for _, v := range c.Vdevs {
			for _, leaf := range v.Leaves() {
				if len(leaf.Children) > 0 {
					continue
				}
				if sm, ok := h.smart[h.vdevDisk[leaf.Name]]; ok {
					if ds := smartSev(sm); ds > s {
						s = ds
					}
				}
			}
		}
	}
	return s
}

// applyDisks ingests the DiskTexts surfaces and joins everything: alias
// universe, block map, inventory, chip→disk links, vdev resolution.
func (h *hostState) applyDisks(aliases, sysBlock, lsblk, hwmonDev string) {
	h.aliases = zfs.ParseDiskAliases(aliases)
	h.blocks = zfs.ParseSysBlock(sysBlock)
	h.disks = zfs.ParseLsblkDisks(lsblk)
	for i := range h.disks {
		h.disks[i].DevPath = h.blocks[h.disks[i].Node].DevPath
	}
	h.chipDisk = map[string]string{}
	for dir, dev := range zfs.ParseHwmonDevs(hwmonDev) {
		for _, d := range h.disks {
			if d.DevPath != "" && strings.HasPrefix(d.DevPath, dev+"/") {
				h.chipDisk[dir] = d.Node
				break
			}
		}
	}
	h.haveDisks = len(h.disks) > 0 || len(h.aliases) > 0
	h.resolveVdevs()
	h.refreshTemps()
}

// resolveVdevs maps every pool leaf to its physical disk; called when
// either side of the join (pools, aliases) arrives.
func (h *hostState) resolveVdevs() {
	if h.aliases == nil {
		return
	}
	h.vdevDisk = map[string]string{}
	for _, p := range h.pools {
		for _, c := range p.Classes {
			for _, v := range c.Vdevs {
				for _, leaf := range v.Leaves() {
					if len(leaf.Children) > 0 {
						continue
					}
					if node := zfs.ResolveVdev(leaf.Name, h.aliases, h.blocks); node != "" {
						h.vdevDisk[leaf.Name] = node
					}
				}
			}
		}
	}
}

// diskFor returns the resolved disk for a status leaf name.
func (h *hostState) diskFor(leaf string) *zfs.Disk {
	node, ok := h.vdevDisk[leaf]
	if !ok {
		return nil
	}
	for i := range h.disks {
		if h.disks[i].Node == node {
			return &h.disks[i]
		}
	}
	return nil
}

// applyInfo ingests the once-per-connect identity surfaces.
func (h *hostState) applyInfo(zfsVer, kernel, osRelease string) {
	user, kmod := zfs.ParseZfsVersion(zfsVer)
	h.zfsVer, h.zfsKmod = user, kmod
	h.kernel = kernel
	h.osName = zfs.ParseOsRelease(osRelease)
	h.haveInfo = user != "" || kernel != "" || h.osName != ""
}

// poolGeometry returns the data-vdev raidz shape of a pool, when it has one.
func (h *hostState) poolGeometry(pool string) (width, parity, ashift int, ok bool) {
	ashift, haveShift := h.ashift[pool]
	if !haveShift {
		return 0, 0, 0, false
	}
	p := h.pool(pool)
	if p == nil {
		return 0, 0, 0, false
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

// applyObjsets turns cumulative objset counters into rates against the
// previous sample and appends them to each dataset's history ring.
// Counters reset when a dataset reloads, so negative deltas clamp to zero.
func (h *hostState) applyObjsets(cur map[string]zfs.ObjsetIO) {
	now := time.Now()
	dt := now.Sub(h.objsetAt).Seconds()
	if h.objsetPrev != nil && dt > 0.2 {
		rates := map[string]zfs.IORates{}
		for name, c := range cur {
			p, ok := h.objsetPrev[name]
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
			hist := append(h.dsIOHist[name], r)
			if len(hist) > dsIOHistLen {
				hist = hist[len(hist)-dsIOHistLen:]
			}
			h.dsIOHist[name] = hist
		}
		h.dsIO = rates
	}
	h.objsetPrev = cur
	h.objsetAt = now
}

// subtreeIO sums current rates and tail-aligned history over d and all its
// descendants that have loaded objsets.
func (h *hostState) subtreeIO(d *zfs.Dataset) (cur zfs.IORates, rh, wh []int64, loaded int) {
	var rings [][]zfs.IORates
	maxLen := 0
	var walk func(x *zfs.Dataset)
	walk = func(x *zfs.Dataset) {
		if r, ok := h.dsIO[x.Name]; ok {
			cur.RBw += r.RBw
			cur.WBw += r.WBw
			cur.ROps += r.ROps
			cur.WOps += r.WOps
			loaded++
			ring := h.dsIOHist[x.Name]
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

// arcRate computes a per-second delta for an arcstats counter.
func (h *hostState) arcRate(key string) float64 {
	if h.arcMapPrev == nil {
		return 0
	}
	dt := h.arcAt.Sub(h.arcPrevAt).Seconds()
	if dt <= 0 {
		return 0
	}
	d := h.arcMap[key] - h.arcMapPrev[key]
	if d < 0 {
		return 0
	}
	return float64(d) / dt
}
