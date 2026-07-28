package zfs

import (
	"sort"
	"strconv"
	"strings"
)

// Host-level surfaces for the multi-host tree: /proc vitals and identity
// strings, collected per host alongside the ZFS stats. Every parser
// tolerates empty or garbage input — a host that exposes none of these
// simply shows no vitals.

// ParseUptime reads /proc/uptime and returns whole seconds of uptime.
func ParseUptime(s string) int64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return int64(v)
}

// ParseLoad1 returns /proc/loadavg's one-minute figure as the kernel
// formatted it.
func ParseLoad1(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// ParseCPUStat reads /proc/stat's aggregate cpu line into busy and total
// jiffy counters; utilization is a delta between two samples, computed by
// the caller. Returns zeros when the line is absent.
func ParseCPUStat(s string) (busy, total int64) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		// "cpu  user nice system idle iowait irq softirq steal ..."
		if len(f) < 8 || f[0] != "cpu" {
			continue
		}
		var vals [8]int64
		for i := range vals {
			v, err := strconv.ParseInt(f[i+1], 10, 64)
			if err != nil {
				return 0, 0
			}
			vals[i] = v
		}
		idle := vals[3] + vals[4] // idle + iowait
		for _, v := range vals {
			total += v
		}
		return total - idle, total
	}
	return 0, 0
}

// cpuChips are hwmon names that mean "the processor" — their readings wear
// the label "cpu" and their per-core rows collapse to package rows.
var cpuChips = map[string]bool{
	"coretemp": true, "k10temp": true, "zenpower": true, "cpu_thermal": true,
}

// driveChips are hwmon names whose readings belong to a specific disk, not
// the sensors list — they join the drive roster via their device link.
var driveChips = map[string]bool{"nvme": true, "drivetemp": true}

func IsCPUChip(name string) bool   { return cpuChips[name] }
func IsDriveChip(name string) bool { return driveChips[name] }

// HwmonTemp is one labeled reading of a chip, with the driver-stated
// thresholds when the chip exports sane ones (-1 otherwise — thresholds
// are never invented).
type HwmonTemp struct {
	Label      string
	MilliC     int64
	MaxMilliC  int64 // temp*_max, -1 unknown
	CritMilliC int64 // temp*_crit, -1 unknown
}

// HwmonChip is one sensor chip with all its temperature readings. Every
// chip is surfaced — the rule since the protan round: anything eligible
// for the host line must be a named, explorable row in the host view.
type HwmonChip struct {
	Dir   string // /sys/class/hwmon/hwmonN — the device-link join key
	Name  string
	Temps []HwmonTemp
}

// MaxC returns the chip's hottest reading in whole °C.
func (c *HwmonChip) MaxC() int {
	var best int64 = -1 << 62
	for _, t := range c.Temps {
		if t.MilliC > best {
			best = t.MilliC
		}
	}
	if best == -1<<62 {
		return -1
	}
	return int((best + 500) / 1000)
}

// ParseHwmon digests `grep -H .` output over hwmon name/temp files
// ("path:value" lines) into chips with paired labels, ordered by dir.
func ParseHwmon(s string) []HwmonChip {
	type acc struct {
		name   string
		inputs map[string]int64
		labels map[string]string
		maxes  map[string]int64
		crits  map[string]int64
	}
	chips := map[string]*acc{}
	get := func(dir string) *acc {
		if chips[dir] == nil {
			chips[dir] = &acc{inputs: map[string]int64{}, labels: map[string]string{},
				maxes: map[string]int64{}, crits: map[string]int64{}}
		}
		return chips[dir]
	}
	for _, line := range strings.Split(s, "\n") {
		i := strings.LastIndexByte(line, ':')
		if i <= 0 {
			continue
		}
		path, val := line[:i], strings.TrimSpace(line[i+1:])
		j := strings.LastIndexByte(path, '/')
		if j < 0 {
			continue
		}
		dir, file := path[:j], path[j+1:]
		switch {
		case file == "name":
			get(dir).name = val
		case strings.HasPrefix(file, "temp") && strings.HasSuffix(file, "_input"):
			// non-positive readings are sensor fiction (virtual nvme
			// targets report sub-zero composites) — drop them at the door
			if v, err := strconv.ParseInt(val, 10, 64); err == nil && v > 0 {
				get(dir).inputs[strings.TrimSuffix(file, "_input")] = v
			}
		case strings.HasPrefix(file, "temp") && strings.HasSuffix(file, "_label"):
			get(dir).labels[strings.TrimSuffix(file, "_label")] = val
		case strings.HasPrefix(file, "temp") && strings.HasSuffix(file, "_max"):
			if v, err := strconv.ParseInt(val, 10, 64); err == nil && v > 0 {
				get(dir).maxes[strings.TrimSuffix(file, "_max")] = v
			}
		case strings.HasPrefix(file, "temp") && strings.HasSuffix(file, "_crit"):
			if v, err := strconv.ParseInt(val, 10, 64); err == nil && v > 0 {
				get(dir).crits[strings.TrimSuffix(file, "_crit")] = v
			}
		}
	}
	var dirs []string
	for d := range chips {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		// hwmon10 sorts after hwmon9, not after hwmon1
		a, b := dirs[i], dirs[j]
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	})
	var out []HwmonChip
	for _, d := range dirs {
		a := chips[d]
		if len(a.inputs) == 0 {
			continue // chips without temperatures (AC, BAT) are not sensors here
		}
		var keys []string
		for k := range a.inputs {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) != len(keys[j]) {
				return len(keys[i]) < len(keys[j])
			}
			return keys[i] < keys[j]
		})
		chip := HwmonChip{Dir: d, Name: a.name}
		for _, k := range keys {
			// thresholds are trusted only when sane: max positive, crit
			// strictly above max when both exist — a zero or inverted pair
			// would flag every reading forever
			max, crit := int64(-1), int64(-1)
			if v, ok := a.maxes[k]; ok {
				max = v
			}
			if v, ok := a.crits[k]; ok && (max < 0 || v > max) {
				crit = v
			}
			chip.Temps = append(chip.Temps, HwmonTemp{Label: a.labels[k],
				MilliC: a.inputs[k], MaxMilliC: max, CritMilliC: crit})
		}
		out = append(out, chip)
	}
	return out
}

// ParseZfsVersion splits `zfs version` output into userland and kmod
// versions, trimmed of packaging suffixes ("2.2.2-0ubuntu9.4" → "2.2.2").
func ParseZfsVersion(s string) (userland, kmod string) {
	trim := func(v string) string {
		if i := strings.IndexByte(v, '-'); i > 0 {
			return v[:i]
		}
		return v
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "zfs-kmod-"):
			kmod = trim(strings.TrimPrefix(line, "zfs-kmod-"))
		case strings.HasPrefix(line, "zfs-"):
			userland = trim(strings.TrimPrefix(line, "zfs-"))
		}
	}
	return userland, kmod
}

// ParseOsRelease reduces /etc/os-release to "id version" ("ubuntu 24.04"),
// falling back to PRETTY_NAME.
func ParseOsRelease(s string) string {
	kv := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		i := strings.IndexByte(line, '=')
		if i <= 0 {
			continue
		}
		kv[line[:i]] = strings.Trim(strings.TrimSpace(line[i+1:]), `"`)
	}
	if kv["ID"] != "" && kv["VERSION_ID"] != "" {
		return kv["ID"] + " " + kv["VERSION_ID"]
	}
	return kv["PRETTY_NAME"]
}

// NiceUptime renders seconds as the two most significant units, ncdu-terse.
func NiceUptime(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	d, h, m := sec/86400, sec%86400/3600, sec%3600/60
	switch {
	case d > 0:
		return itoa(d) + "d " + itoa(h) + "h"
	case h > 0:
		return itoa(h) + "h " + itoa(m) + "m"
	default:
		return itoa(m) + "m"
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
