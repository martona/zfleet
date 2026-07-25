package zfs

import (
	"sort"
	"strings"
)

// Disk-layer resolution. Pools reference their members by whatever path
// they were built with — by-id, by-partuuid, by-partlabel, EUI forms, bare
// kernel names, with or without partition suffixes. Rather than guessing
// schemes, we collect the whole alias universe under /dev/disk and the
// kernel block map from /sys/class/block, and let every leaf resolve
// through the same two lookups: alias → node, partition → parent disk.

// Disk is one physical block device from the lsblk inventory.
type Disk struct {
	Node    string // kernel name: sda, nvme0n1
	Size    int64
	Rota    bool
	Model   string
	Serial  string
	DevPath string // sysfs device path ("devices/..."), the hwmon join key
	TempC   int    // -1 until a sensor claims it
	TempSrc string // hwmon chip that provided the reading
}

// BlockNode is one kernel block device or partition from /sys/class/block.
type BlockNode struct {
	Parent  string // owning disk for partitions, "" for whole devices
	DevPath string // sysfs path, normalized to start at "devices/"
}

// ParseDiskAliases digests `find /dev/disk -type l -printf '%p %l\n'` into
// alias-basename → kernel node. Loop-device aliases are dropped.
func ParseDiskAliases(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.LastIndexByte(line, ' ')
		if i <= 0 {
			continue
		}
		path, target := line[:i], line[i+1:]
		node := target[strings.LastIndexByte(target, '/')+1:]
		if node == "" || strings.HasPrefix(node, "loop") {
			continue
		}
		base := path[strings.LastIndexByte(path, '/')+1:]
		out[base] = node
	}
	return out
}

// ParseSysBlock digests `find /sys/class/block -maxdepth 1 -type l -printf
// '%f %l\n'`: for each node, its parent disk (partitions live inside their
// disk's sysfs directory — no name parsing) and its device path. A parent
// candidate only counts if it is itself a block node: an NVMe namespace
// nests inside its CONTROLLER's directory (nvme0/nvme0n1), and nvme0 is
// not a disk.
func ParseSysBlock(text string) map[string]BlockNode {
	out := map[string]BlockNode{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.IndexByte(line, ' ')
		if i <= 0 {
			continue
		}
		node, target := line[:i], line[i+1:]
		segs := strings.Split(strings.TrimLeft(target, "./"), "/")
		parent := ""
		if len(segs) >= 2 && segs[len(segs)-1] == node {
			parent = segs[len(segs)-2]
		}
		out[node] = BlockNode{Parent: parent, DevPath: strings.Join(segs, "/")}
	}
	for node, b := range out {
		if _, isNode := out[b.Parent]; !isNode {
			b.Parent = ""
			out[node] = b
		}
	}
	return out
}

// ParseLsblkDisks digests `lsblk -bdP -o NAME,SIZE,ROTA,MODEL,SERIAL`.
// Loop devices, zvols (zfs's own output, not its substrate), network block
// devices, and optical drives are not disks here.
func ParseLsblkDisks(text string) []Disk {
	var out []Disk
	for _, line := range strings.Split(text, "\n") {
		kv := parseKV(line)
		name := kv["NAME"]
		if name == "" || strings.HasPrefix(name, "loop") ||
			strings.HasPrefix(name, "zd") || strings.HasPrefix(name, "sr") ||
			strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "nbd") {
			continue
		}
		out = append(out, Disk{
			Node:   name,
			Size:   parseI64(kv["SIZE"]),
			Rota:   kv["ROTA"] == "1",
			Model:  strings.TrimSpace(kv["MODEL"]),
			Serial: strings.TrimSpace(kv["SERIAL"]),
			TempC:  -1,
		})
	}
	sort.Slice(out, func(i, j int) bool { return NaturalLess(out[i].Node, out[j].Node) })
	return out
}

// NaturalLess orders embedded numbers numerically: nvme9n1 before
// nvme10n1, where lexical sort files 10 behind 1.
func NaturalLess(a, b string) bool {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	for len(a) > 0 && len(b) > 0 {
		if isDigit(a[0]) && isDigit(b[0]) {
			ai, bi := 1, 1
			for ai < len(a) && isDigit(a[ai]) {
				ai++
			}
			for bi < len(b) && isDigit(b[bi]) {
				bi++
			}
			an := strings.TrimLeft(a[:ai], "0")
			bn := strings.TrimLeft(b[:bi], "0")
			if len(an) != len(bn) {
				return len(an) < len(bn)
			}
			if an != bn {
				return an < bn
			}
			a, b = a[ai:], b[bi:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

// parseKV splits lsblk -P output: KEY="VALUE" pairs.
func parseKV(line string) map[string]string {
	out := map[string]string{}
	for len(line) > 0 {
		eq := strings.IndexByte(line, '=')
		if eq < 0 || eq+1 >= len(line) || line[eq+1] != '"' {
			break
		}
		key := strings.TrimSpace(line[:eq])
		rest := line[eq+2:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			break
		}
		out[key] = rest[:end]
		line = rest[end+1:]
	}
	return out
}

// ParseHwmonDevs digests the hwmon chip → canonical device path listing.
func ParseHwmonDevs(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		i := strings.IndexByte(line, ' ')
		if i <= 0 {
			continue
		}
		dev := strings.TrimPrefix(line[i+1:], "/sys/")
		out[line[:i]] = dev
	}
	return out
}

// ResolveVdev maps one zpool-status leaf name to its whole physical disk:
// alias lookup (any /dev/disk scheme), bare kernel names accepted as-is,
// partitions collapsed to their disk. "" when nothing matches — file
// vdevs and exotica render blank, not wrong.
func ResolveVdev(name string, aliases map[string]string, blocks map[string]BlockNode) string {
	node, ok := aliases[name]
	if !ok {
		if _, isNode := blocks[name]; isNode {
			node = name
		} else {
			return ""
		}
	}
	if b, ok := blocks[node]; ok && b.Parent != "" {
		return b.Parent
	}
	return node
}
