package zfs

import (
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

// hwmon chips whose temperature is worth calling the host's: CPU packages
// and storage. Anything else (NIC PHYs, ambient, chipset) may well be the
// hottest sensor in the box — commodoreplus4's 10GbE runs 20°C above both
// Xeon packages — but it is not the reading anyone asks a storage host for.
var hwmonPreferred = map[string]string{
	"coretemp":    "cpu",
	"k10temp":     "cpu",
	"zenpower":    "cpu",
	"cpu_thermal": "cpu",
	"nvme":        "nvme",
	"drivetemp":   "disk",
}

// ParseHwmonTemp digests `grep -H .` output over hwmon name/temp files
// ("path:value" lines) and returns the hottest reading among preferred
// chips, in whole °C, labeled by chip kind.
func ParseHwmonTemp(s string) (c int, src string, ok bool) {
	chip := map[string]string{} // hwmon dir → chip name
	type reading struct {
		dir   string
		milli int64
	}
	var temps []reading
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
			chip[dir] = val
		case strings.HasPrefix(file, "temp") && strings.HasSuffix(file, "_input"):
			if v, err := strconv.ParseInt(val, 10, 64); err == nil {
				temps = append(temps, reading{dir, v})
			}
		}
	}
	var best int64 = -1 << 62
	for _, r := range temps {
		kind, pref := hwmonPreferred[chip[r.dir]]
		if !pref {
			continue
		}
		if r.milli > best {
			best, src, ok = r.milli, kind, true
		}
	}
	if !ok {
		return 0, "", false
	}
	return int((best + 500) / 1000), src, true
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
