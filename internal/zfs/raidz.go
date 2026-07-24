package zfs

import "strings"

// Raidz allocation geometry. A block on raidz costs its data sectors plus
// parity per stripe row, padded up to a multiple of (parity+1) so freed
// segments stay usable. Dataset `used` charges that raw cost deflated by a
// fixed baseline computed at 128K blocks — so blocks smaller than the
// baseline show up as `used` exceeding logical space. Validated against a
// live 8-wide raidz3 @ ashift 12: predicted ×1.143 for 16K volblocksize,
// measured ×1.14.

// raidzRawSectors returns raw sectors allocated for one block of size bytes.
func raidzRawSectors(size int64, width, parity, ashift int) int64 {
	sector := int64(1) << ashift
	data := (size + sector - 1) / sector
	if data == 0 {
		data = 1
	}
	rows := (data + int64(width-parity) - 1) / int64(width-parity)
	total := data + rows*int64(parity)
	pad := int64(parity + 1)
	return (total + pad - 1) / pad * pad
}

// RaidzRawPerCharged returns raw bytes consumed per byte charged to
// datasets — the deflation baseline, computed at 128K as ZFS does.
func RaidzRawPerCharged(width, parity, ashift int) float64 {
	const base = int64(128 * 1024)
	dataSectors := base >> ashift
	return float64(raidzRawSectors(base, width, parity, ashift)) / float64(dataSectors)
}

// RaidzChargedInflation returns the expected `used`-visible inflation
// (charged/logical, before compression) for blocks of blockSize.
func RaidzChargedInflation(blockSize int64, width, parity, ashift int) float64 {
	if blockSize <= 0 {
		return 1
	}
	sector := int64(1) << ashift
	dataSectors := (blockSize + sector - 1) / sector
	rawPerData := float64(raidzRawSectors(blockSize, width, parity, ashift)) / float64(dataSectors)
	return rawPerData / RaidzRawPerCharged(width, parity, ashift)
}

// RaidzShape extracts (parity, ok) from a vdev name like "raidz3-0",
// "raidz1-4", or legacy "raidz-0".
func RaidzShape(vdevName string) (parity int, ok bool) {
	if !strings.HasPrefix(vdevName, "raidz") {
		return 0, false
	}
	rest := strings.TrimPrefix(vdevName, "raidz")
	if rest == "" || rest[0] == '-' {
		return 1, true
	}
	if rest[0] >= '1' && rest[0] <= '3' {
		return int(rest[0] - '0'), true
	}
	return 0, false
}

// RootStat is one pool root's charged-vs-logical pair.
type RootStat struct {
	Used, LogicalUsed int64
}

// ParseRootStats parses `zfs list -Hp -d 0 -o name,used,logicalused`.
func ParseRootStats(text string) map[string]RootStat {
	out := map[string]RootStat{}
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(raw, "\r"), "\t")
		if len(f) == 3 {
			out[f[0]] = RootStat{Used: parseI64(f[1]), LogicalUsed: parseI64(f[2])}
		}
	}
	return out
}

// ParsePoolAshift parses `zpool get -Hp ashift` output.
func ParsePoolAshift(text string) map[string]int {
	out := map[string]int{}
	for _, raw := range strings.Split(text, "\n") {
		f := strings.Split(strings.TrimRight(raw, "\r"), "\t")
		if len(f) >= 3 && f[1] == "ashift" {
			if v := parseI64(f[2]); v >= 9 && v <= 16 {
				out[f[0]] = int(v)
			}
		}
	}
	return out
}
