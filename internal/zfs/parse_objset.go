package zfs

import "strings"

// ObjsetIO is one dataset's cumulative io counters from
// /proc/spl/kstat/zfs/<pool>/objset-*. Entries exist only for LOADED
// objsets (mounted filesystems, active zvols) — absence means "no stats",
// not "idle". Counters reset when a dataset is reloaded, so rate math must
// clamp negative deltas.
type ObjsetIO struct {
	Reads, Writes   int64 // ops, cumulative
	NRead, NWritten int64 // bytes, cumulative
	// zil counters, cumulative. ZilSeen marks kmods that carry per-dataset
	// zil stats (2.2+) — the pool panel sums THESE for its zil line; the
	// host-global zil kstat is only a fallback, because captioning it
	// per-pool once blamed an idle recv pool for a NIC's rasdaemon fsync
	// storm two pools away (zeus, round 36).
	ZilSeen    bool
	ZilCommits int64
	ZilNormalB int64 // itx bytes written to data vdevs
	ZilSlogB   int64 // itx bytes written to a slog
	ZilNormalC int64 // itx count, normal class
	ZilSlogC   int64 // itx count, slog class
}

// ParseObjsets parses any concatenation of objset kstat files. Each file
// carries its own dataset_name row before its counters, so no separators
// are needed.
func ParseObjsets(text string) map[string]ObjsetIO {
	out := map[string]ObjsetIO{}
	cur := ""
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Fields(raw)
		if len(f) < 3 {
			continue
		}
		if f[0] == "dataset_name" {
			// dataset names may contain spaces; the value is everything
			// after the kstat type column
			cur = strings.Join(f[2:], " ")
			continue
		}
		if cur == "" {
			continue
		}
		v := parseI64(f[2])
		if v < 0 {
			continue
		}
		o := out[cur]
		switch f[0] {
		case "reads":
			o.Reads = v
		case "writes":
			o.Writes = v
		case "nread":
			o.NRead = v
		case "nwritten":
			o.NWritten = v
		case "zil_commit_count":
			o.ZilCommits, o.ZilSeen = v, true
		case "zil_itx_metaslab_normal_bytes":
			o.ZilNormalB = v
		case "zil_itx_metaslab_slog_bytes":
			o.ZilSlogB = v
		case "zil_itx_metaslab_normal_count":
			o.ZilNormalC = v
		case "zil_itx_metaslab_slog_count":
			o.ZilSlogC = v
		default:
			continue
		}
		out[cur] = o
	}
	return out
}
