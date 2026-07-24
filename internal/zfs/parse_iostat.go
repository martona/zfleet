package zfs

import "strings"

// ParseIostatPools extracts pool-level rows from `zpool iostat -Hp` output
// (with or without -v). Fixture-verified: -H iostat prints every row at
// column 0 with no indentation, 7 tab-separated fields
// (name alloc free rops wops rbw wbw), classes separated by blank lines and
// samples not separated at all — so rows are matched against known pool
// names, and for multi-sample input the last occurrence wins.
func ParseIostatPools(text string, poolNames map[string]bool) map[string]IORates {
	out := map[string]IORates{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		f := strings.Split(line, "\t")
		if len(f) != 7 || !poolNames[f[0]] {
			continue
		}
		out[f[0]] = IORates{
			ROps: parseI64(f[3]),
			WOps: parseI64(f[4]),
			RBw:  parseI64(f[5]),
			WBw:  parseI64(f[6]),
		}
	}
	return out
}
