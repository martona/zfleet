package zfs

import "strings"

// ParseArcstats reads the /proc/spl/kstat/zfs/arcstats format: a two-line
// header followed by "name type value" rows.
func ParseArcstats(text string) ArcStats {
	var a ArcStats
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Fields(raw)
		if len(f) != 3 {
			continue
		}
		v := parseI64(f[2])
		if v < 0 {
			continue
		}
		switch f[0] {
		case "size":
			a.Size = v
		case "c_max":
			a.CMax = v
		case "hits":
			a.Hits = v
		case "misses":
			a.Misses = v
		}
	}
	return a
}
