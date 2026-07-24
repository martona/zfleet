package zfs

import (
	"fmt"
	"path"
	"strings"
)

// Parsers for the performance screen's kstat surfaces.

// TxgRow is one line of /proc/spl/kstat/zfs/<pool>/txgs — a ring of the
// last ~100 txgs with phase timings in nanoseconds.
type TxgRow struct {
	Txg      int64
	Birth    int64 // ns since boot
	State    string
	NDirty   int64 // bytes dirtied in this txg
	NRead    int64
	NWritten int64
	Reads    int64
	Writes   int64
	OTime    int64 // open
	QTime    int64 // quiesce
	WTime    int64 // wait
	STime    int64 // sync
}

func ParseTxgs(text string) []TxgRow {
	var out []TxgRow
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Fields(raw)
		if len(f) < 12 || f[0] == "txg" {
			continue
		}
		if v := parseI64(f[0]); v < 0 {
			continue
		}
		out = append(out, TxgRow{
			Txg: parseI64(f[0]), Birth: parseI64(f[1]), State: f[2],
			NDirty: parseI64(f[3]), NRead: parseI64(f[4]), NWritten: parseI64(f[5]),
			Reads: parseI64(f[6]), Writes: parseI64(f[7]),
			OTime: parseI64(f[8]), QTime: parseI64(f[9]),
			WTime: parseI64(f[10]), STime: parseI64(f[11]),
		})
	}
	return out
}

// TxgSummary aggregates the last N committed txgs.
type TxgSummary struct {
	Committed  int
	PerMinute  float64
	DirtyAvg   int64
	DirtyPeak  int64
	OAvg       int64 // ns
	QAvg       int64
	WAvg       int64
	SAvg       int64
	SyncTimes  []int64 // recent stimes, oldest first (sparkline fodder)
	DirtyBytes []int64 // recent ndirty, oldest first
}

func SummarizeTxgs(rows []TxgRow, lastN int) TxgSummary {
	var committed []TxgRow
	for _, r := range rows {
		if r.State == "C" {
			committed = append(committed, r)
		}
	}
	if len(committed) > lastN {
		committed = committed[len(committed)-lastN:]
	}
	s := TxgSummary{Committed: len(committed)}
	if len(committed) == 0 {
		return s
	}
	var oSum, qSum, wSum, stSum int64
	for _, r := range committed {
		oSum += r.OTime
		qSum += r.QTime
		wSum += r.WTime
		stSum += r.STime
		s.DirtyAvg += r.NDirty
		if r.NDirty > s.DirtyPeak {
			s.DirtyPeak = r.NDirty
		}
		s.SyncTimes = append(s.SyncTimes, r.STime)
		s.DirtyBytes = append(s.DirtyBytes, r.NDirty)
	}
	n := int64(len(committed))
	s.OAvg, s.QAvg, s.WAvg, s.SAvg = oSum/n, qSum/n, wSum/n, stSum/n
	s.DirtyAvg /= n
	if len(committed) > 1 {
		span := committed[len(committed)-1].Birth - committed[0].Birth
		if span > 0 {
			s.PerMinute = float64(len(committed)-1) / (float64(span) / 60e9)
		}
	}
	return s
}

// ParseKstatMap parses any "name type value" kstat file into a map.
func ParseKstatMap(text string) map[string]int64 {
	out := map[string]int64{}
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Fields(raw)
		if len(f) != 3 {
			continue
		}
		if v := parseI64(f[2]); v >= 0 {
			out[f[0]] = v
		}
	}
	return out
}

// ParseParams parses "path:value" lines (grep -H format) keyed by the
// parameter's basename.
func ParseParams(text string) map[string]int64 {
	out := map[string]int64{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		i := strings.LastIndexByte(line, ':')
		if i <= 0 {
			continue
		}
		if v := parseI64(line[i+1:]); v >= 0 {
			out[path.Base(line[:i])] = v
		}
	}
	return out
}

// VdevLat is one row of `zpool iostat -Hpvly`: latencies in ns, -1 when the
// interval saw no io ("-"), plus the interval's op counts — the weights
// that make window averaging honest.
type VdevLat struct {
	ROps, WOps       int64
	TotalR, TotalW   int64
	DiskR, DiskW     int64
	SyncQR, SyncQW   int64
	AsyncQR, AsyncQW int64
}

// QueueR/QueueW combine sync+async queue waits (either may be unknown).
func (v VdevLat) QueueR() int64 { return addLat(v.SyncQR, v.AsyncQR) }
func (v VdevLat) QueueW() int64 { return addLat(v.SyncQW, v.AsyncQW) }

func addLat(a, b int64) int64 {
	if a < 0 && b < 0 {
		return -1
	}
	if a < 0 {
		return b
	}
	if b < 0 {
		return a
	}
	return a + b
}

// ParseIostatLatency extracts the named pool's vdev latency rows from
// `zpool iostat -Hpvly 1 1` output. Rows are flat (no indentation), so the
// pool's section runs from its own row to the next known pool name.
func ParseIostatLatency(text, pool string, poolNames map[string]bool) map[string]VdevLat {
	out := map[string]VdevLat{}
	in := false
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(raw, "\r"), "\t")
		if len(f) < 17 {
			continue
		}
		if poolNames[f[0]] {
			in = f[0] == pool
		}
		if !in {
			continue
		}
		out[f[0]] = VdevLat{
			ROps: parseI64(f[3]), WOps: parseI64(f[4]),
			TotalR: parseI64(f[7]), TotalW: parseI64(f[8]),
			DiskR: parseI64(f[9]), DiskW: parseI64(f[10]),
			SyncQR: parseI64(f[11]), SyncQW: parseI64(f[12]),
			AsyncQR: parseI64(f[13]), AsyncQW: parseI64(f[14]),
		}
	}
	return out
}

// OpWeightedLat averages latency samples weighted by each interval's op
// count — an interval with three slow ops must not outvote one with ten
// thousand fast ones, which is exactly what equal-interval averaging does
// to sparsely-bursting drives. Columns with no ops anywhere return -1.
func OpWeightedLat(samples []VdevLat) VdevLat {
	avg := func(pick func(VdevLat) int64, weight func(VdevLat) int64) int64 {
		var num, den float64
		for _, s := range samples {
			l, w := pick(s), weight(s)
			if l < 0 || w <= 0 {
				continue
			}
			num += float64(l) * float64(w)
			den += float64(w)
		}
		if den == 0 {
			return -1
		}
		return int64(num / den)
	}
	r := func(s VdevLat) int64 { return s.ROps }
	w := func(s VdevLat) int64 { return s.WOps }
	out := VdevLat{
		TotalR:  avg(func(s VdevLat) int64 { return s.TotalR }, r),
		TotalW:  avg(func(s VdevLat) int64 { return s.TotalW }, w),
		DiskR:   avg(func(s VdevLat) int64 { return s.DiskR }, r),
		DiskW:   avg(func(s VdevLat) int64 { return s.DiskW }, w),
		SyncQR:  avg(func(s VdevLat) int64 { return s.SyncQR }, r),
		SyncQW:  avg(func(s VdevLat) int64 { return s.SyncQW }, w),
		AsyncQR: avg(func(s VdevLat) int64 { return s.AsyncQR }, r),
		AsyncQW: avg(func(s VdevLat) int64 { return s.AsyncQW }, w),
	}
	for _, s := range samples {
		out.ROps += s.ROps
		out.WOps += s.WOps
	}
	return out
}

// NiceNS renders a nanosecond duration compactly: 850ns, 12µs, 1.7ms, 5.12s.
func NiceNS(ns int64) string {
	if ns < 0 {
		return "-"
	}
	f := float64(ns)
	units := []struct {
		div  float64
		name string
	}{{1e9, "s"}, {1e6, "ms"}, {1e3, "µs"}, {1, "ns"}}
	for _, u := range units {
		if f >= u.div || u.div == 1 {
			v := f / u.div
			switch {
			case v >= 100:
				return fmt.Sprintf("%.0f%s", v, u.name)
			case v >= 10:
				return fmt.Sprintf("%.1f%s", v, u.name)
			default:
				return fmt.Sprintf("%.2f%s", v, u.name)
			}
		}
	}
	return "0ns"
}
