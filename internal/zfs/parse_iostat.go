package zfs

import "strings"

// ParseIostatPools extracts pool-level rows from `zpool iostat -Hp` output.
// Fixture-verified: -H iostat prints every row at column 0 with no
// indentation, tab-separated, the first 7 fields always
// (name alloc free rops wops rbw wbw) — the -l latency columns of the
// streaming form simply follow. Rows are matched against known pool names
// (vdev rows never collide with them), and for multi-sample input the last
// occurrence wins.
func ParseIostatPools(text string, poolNames map[string]bool) map[string]IORates {
	out := map[string]IORates{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, "\r")
		f := strings.Split(line, "\t")
		if len(f) < 7 || !poolNames[f[0]] {
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

// IostatTimestamp reports whether a line is a `-T u` timestamp — a bare
// epoch integer with no tabs. These frame the sample blocks of a streaming
// iostat; data rows always carry tabs. Consecutive timestamps occur (-y
// suppresses the boot sample's rows but not its timestamp line).
func IostatTimestamp(line string) bool {
	if line == "" {
		return false
	}
	for i := 0; i < len(line); i++ {
		if line[i] < '0' || line[i] > '9' {
			return false
		}
	}
	return true
}
