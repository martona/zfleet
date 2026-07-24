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
	ZilCommits      int64 // cumulative; 0 on kmods without the field
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
			o.ZilCommits = v
		default:
			continue
		}
		out[cur] = o
	}
	return out
}
