package zfs

import (
	"regexp"
	"sort"
	"strings"
)

// DatasetFields is the exact -o list for `zfs list -Hp -t filesystem,volume`.
// The parser indexes into it positionally; collector and fixtures must use
// the same list.
const DatasetFields = "name,type,used,avail,refer," +
	"usedbysnapshots,usedbydataset,usedbychildren,usedbyrefreservation," +
	"mountpoint,mounted,canmount,quota,refquota,reservation,refreservation," +
	"recordsize,volsize,volblocksize,compression,compressratio,origin," +
	"creation,atime,sync,encryption,keystatus,logicalused,logicalreferenced"

// SnapshotFields is the -o list for `zfs list -Hp -t snapshot`.
const SnapshotFields = "name,used,refer,creation,written"

type Dataset struct {
	Name string // full path: rust/recv/bergamo
	Type string // "filesystem" | "volume"

	Used, Avail, Refer                          int64
	UsedSnap, UsedDS, UsedChild, UsedRefReserv  int64
	Quota, RefQuota, Reservation, RefReserv     int64
	Recordsize, Volsize, Volblocksize, Creation int64
	LogicalUsed, LogicalRefer                   int64 // -1 when not captured

	Mountpoint, Mounted, Canmount string
	Compression, Compressratio    string
	Origin                        string // "-" unless a clone
	Atime, Sync                   string
	Encryption, Keystatus         string

	Children []*Dataset
}

// Base returns the last path segment.
func (d *Dataset) Base() string {
	if i := strings.LastIndexByte(d.Name, '/'); i >= 0 {
		return d.Name[i+1:]
	}
	return d.Name
}

func (d *Dataset) IsVolume() bool { return d.Type == "volume" }

// Locked reports an encrypted dataset whose key is not loaded.
func (d *Dataset) Locked() bool { return d.Keystatus == "unavailable" }

type DatasetTree struct {
	ByName map[string]*Dataset
	Roots  []*Dataset
}

// ParseDatasets parses `zfs list -Hp -t filesystem,volume -o DatasetFields`
// output (any number of pools) and links the hierarchy. Rows whose parent is
// absent from the text become roots.
func ParseDatasets(text string) *DatasetTree {
	t := &DatasetTree{ByName: map[string]*Dataset{}}
	var order []*Dataset
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		f := strings.Split(line, "\t")
		if len(f) < 27 {
			continue
		}
		d := &Dataset{
			Name: f[0], Type: f[1],
			Used: parseI64(f[2]), Avail: parseI64(f[3]), Refer: parseI64(f[4]),
			UsedSnap: parseI64(f[5]), UsedDS: parseI64(f[6]),
			UsedChild: parseI64(f[7]), UsedRefReserv: parseI64(f[8]),
			Mountpoint: f[9], Mounted: f[10], Canmount: f[11],
			Quota: parseI64(f[12]), RefQuota: parseI64(f[13]),
			Reservation: parseI64(f[14]), RefReserv: parseI64(f[15]),
			Recordsize: parseI64(f[16]), Volsize: parseI64(f[17]),
			Volblocksize: parseI64(f[18]),
			Compression:  f[19], Compressratio: f[20], Origin: f[21],
			Creation: parseI64(f[22]), Atime: f[23], Sync: f[24],
			Encryption: f[25], Keystatus: f[26],
			LogicalUsed: -1, LogicalRefer: -1,
		}
		// trailing fields added later than the first captures; tolerate both
		if len(f) >= 29 {
			d.LogicalUsed = parseI64(f[27])
			d.LogicalRefer = parseI64(f[28])
		}
		t.ByName[d.Name] = d
		order = append(order, d)
	}
	for _, d := range order {
		if i := strings.LastIndexByte(d.Name, '/'); i >= 0 {
			if p := t.ByName[d.Name[:i]]; p != nil {
				p.Children = append(p.Children, d)
				continue
			}
		}
		t.Roots = append(t.Roots, d)
	}
	return t
}

type Snapshot struct {
	Name     string // full: ds@snap
	Snap     string // after the @
	Used     int64
	Refer    int64
	Creation int64
	// Written is the space referenced by this snapshot that its predecessor
	// does not reference — the data born in the window this snapshot
	// closed. Where Used (unique-only) answers "what would deleting this
	// free", Written answers "how much changed during its epoch", and does
	// not shrink when later snapshots share the blocks. -1 when the capture
	// predates the column.
	Written int64
}

// ParseSnapshots extracts the snapshots belonging directly to ds (not its
// descendants) from `zfs list -Hp -t snapshot -o SnapshotFields` output,
// which may cover any wider scope.
func ParseSnapshots(text, ds string) []*Snapshot {
	var out []*Snapshot
	prefix := ds + "@"
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		f := strings.Split(line, "\t")
		if len(f) < 4 || !strings.HasPrefix(f[0], prefix) {
			continue
		}
		s := &Snapshot{
			Name: f[0], Snap: f[0][len(prefix):],
			Used: parseI64(f[1]), Refer: parseI64(f[2]), Creation: parseI64(f[3]),
			Written: -1, // pre-written captures tolerated
		}
		if len(f) >= 5 {
			s.Written = parseI64(f[4])
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Creation < out[j].Creation })
	return out
}

// ParseAllSnapshots groups a pool-recursive (or wider) snapshot listing by
// container dataset, each list chronological — the fleet-sweep shape the
// `/` filter feeds on.
func ParseAllSnapshots(text string) map[string][]*Snapshot {
	out := map[string][]*Snapshot{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		f := strings.Split(line, "\t")
		if len(f) < 4 {
			continue
		}
		i := strings.IndexByte(f[0], '@')
		if i <= 0 {
			continue
		}
		s := &Snapshot{
			Name: f[0], Snap: f[0][i+1:],
			Used: parseI64(f[1]), Refer: parseI64(f[2]), Creation: parseI64(f[3]),
			Written: -1, // pre-written captures tolerated
		}
		if len(f) >= 5 {
			s.Written = parseI64(f[4])
		}
		out[f[0][:i]] = append(out[f[0][:i]], s)
	}
	for _, snaps := range out {
		sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Creation < snaps[j].Creation })
	}
	return out
}

// SnapFamily is a run of snapshots sharing a name prefix followed by a
// timestamp — automation output (zfsrecvd-*, manual-*, autosnap_*) that
// collapses to one browser row. Named milestone snapshots never group.
type SnapFamily struct {
	Prefix string      // "" when the names are pure timestamps
	Snaps  []*Snapshot // chronological
}

func (f *SnapFamily) Used() int64 {
	var n int64
	for _, s := range f.Snaps {
		n += s.Used
	}
	return n
}

func (f *SnapFamily) Oldest() *Snapshot { return f.Snaps[0] }
func (f *SnapFamily) Newest() *Snapshot { return f.Snaps[len(f.Snaps)-1] }

// Label is the display name, e.g. "zfsrecvd-*" or "*" for pure timestamps.
func (f *SnapFamily) Label() string {
	if f.Prefix == "" {
		return "*"
	}
	return f.Prefix + "-*"
}

// SnapEntry is one browser row: exactly one of Fam/Snap is set.
type SnapEntry struct {
	Fam  *SnapFamily
	Snap *Snapshot
}

func (e SnapEntry) Creation() int64 {
	if e.Fam != nil {
		return e.Fam.Newest().Creation
	}
	return e.Snap.Creation
}

func (e SnapEntry) Used() int64 {
	if e.Fam != nil {
		return e.Fam.Used()
	}
	return e.Snap.Used
}

var snapStampRe = regexp.MustCompile(`^(.*?)[-_]?(?:19|20)\d{2}[-_.:]?\d{2}[-_.:]?\d{2}[-_.:T\d]*Z?$`)

// GroupSnapshots collapses timestamp-suffixed snapshots into families of at
// least minSize members, preserving chronological order of input.
func GroupSnapshots(snaps []*Snapshot, minSize int) []SnapEntry {
	families := map[string][]*Snapshot{}
	prefixOf := map[*Snapshot]string{}
	for _, s := range snaps {
		if m := snapStampRe.FindStringSubmatch(s.Snap); m != nil {
			families[m[1]] = append(families[m[1]], s)
			prefixOf[s] = m[1]
		}
	}

	var out []SnapEntry
	emitted := map[string]bool{}
	for _, s := range snaps {
		p, stamped := prefixOf[s]
		if !stamped || len(families[p]) < minSize {
			out = append(out, SnapEntry{Snap: s})
			continue
		}
		if !emitted[p] {
			emitted[p] = true
			out = append(out, SnapEntry{Fam: &SnapFamily{Prefix: p, Snaps: families[p]}})
		}
	}
	return out
}

// Prop is one row of `zfs get all`: value plus where it came from.
type Prop struct {
	Value  string
	Source string // local, default, received, inherited from X, "-", none
}

// ParseProps extracts ds's properties from `zfs get -Hp all` output.
func ParseProps(text, ds string) map[string]Prop {
	out := map[string]Prop{}
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(raw, "\r"), "\t")
		if len(f) == 4 && f[0] == ds {
			out[f[1]] = Prop{Value: f[2], Source: f[3]}
		}
	}
	return out
}
