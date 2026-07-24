package zfs

// Vdev is a node in a pool's device tree: a top-level vdev (mirror-0,
// raidz3-1, a bare disk), an inner grouping (replacing-N, spare-N), or a
// leaf disk. Topology and states come from `zpool status`; sizes come from
// `zpool list -Hpv` joined by name. -1 means unknown (leaf disks report no
// alloc/free, for example).
type Vdev struct {
	Name     string
	State    string
	ReadErr  string // error counters kept as printed ("0", "3", "1.05K")
	WriteErr string
	CksumErr string
	Note     string // trailing status text, e.g. "cannot open", "(resilvering)"
	Size     int64
	Alloc    int64
	Free     int64
	Children []*Vdev
}

func nonZero(s string) bool { return s != "" && s != "0" }

func (v *Vdev) HasErrors() bool {
	return nonZero(v.ReadErr) || nonZero(v.WriteErr) || nonZero(v.CksumErr)
}

// Healthy reports whether the vdev and its whole subtree are ONLINE with
// zero error counters.
func (v *Vdev) Healthy() bool {
	if v.State != "ONLINE" || v.HasErrors() {
		return false
	}
	for _, c := range v.Children {
		if !c.Healthy() {
			return false
		}
	}
	return true
}

// Leaves returns the leaf devices under this vdev (itself if childless).
func (v *Vdev) Leaves() []*Vdev {
	if len(v.Children) == 0 {
		return []*Vdev{v}
	}
	var out []*Vdev
	for _, c := range v.Children {
		out = append(out, c.Leaves()...)
	}
	return out
}

// VdevClass groups top-level vdevs by allocation class in the order zpool
// status prints them: "data" (unlabeled), then special/logs/cache/spares/
// dedup as labeled. Size/Alloc are sums over member vdevs where known.
type VdevClass struct {
	Name  string
	Vdevs []*Vdev
	Size  int64
	Alloc int64
}

type ScanState int

const (
	ScanNone ScanState = iota
	ScanDone
	ScanInProgress
)

// Scan is the parsed "scan:" section. Summary is pre-rendered for display;
// Raw preserves the original lines for anything the parser didn't extract.
type Scan struct {
	State   ScanState
	Kind    string // "scrub" or "resilver"
	Summary string
	Errors  int64
	Percent float64
	Raw     []string
}

type Pool struct {
	Name       string
	State      string
	Notes      []string // status:/action:/see: lines, prefixed with their key
	Scan       Scan
	ErrorsLine string
	Classes    []*VdevClass
	Size       int64
	Alloc      int64
	Free       int64
	FragPct    int64
	CapPct     int64
	Dedup      string
}

func (p *Pool) Class(name string) *VdevClass {
	for _, c := range p.Classes {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (p *Pool) Healthy() bool {
	if p.State != "ONLINE" {
		return false
	}
	for _, c := range p.Classes {
		for _, v := range c.Vdevs {
			if !v.Healthy() {
				return false
			}
		}
	}
	return true
}

// StateRank orders pool states by severity, for worst-first selection.
func StateRank(state string) int {
	switch state {
	case "FAULTED", "UNAVAIL", "SUSPENDED":
		return 3
	case "DEGRADED":
		return 2
	case "OFFLINE", "REMOVED":
		return 1
	default:
		return 0
	}
}

// IORates is one pool-level sample from zpool iostat: ops/s and bytes/s.
type IORates struct {
	ROps, WOps, RBw, WBw int64
}

type ArcStats struct {
	Size, CMax, Hits, Misses int64
}
