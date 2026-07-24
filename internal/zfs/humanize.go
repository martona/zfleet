package zfs

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NiceBytes formats like zfs_nicenum: 1024-based, at most four significant
// characters ("1.88G", "94.2T", "291T"). Negative means unknown.
func NiceBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	return niceScaled(float64(n), 1024)
}

// NiceCount formats an ops/event count, 1000-based.
func NiceCount(n int64) string {
	if n < 0 {
		return "-"
	}
	return niceScaled(float64(n), 1000)
}

func niceScaled(f, base float64) string {
	units := []string{"", "K", "M", "G", "T", "P", "E"}
	i := 0
	for f >= base && i < len(units)-1 {
		f /= base
		i++
	}
	u := units[i]
	if base == 1024 && i == 0 {
		u = "B"
	}
	switch {
	case math.Mod(f, 1) == 0:
		return fmt.Sprintf("%.0f%s", f, u)
	case f >= 100:
		return fmt.Sprintf("%.0f%s", f, u)
	case f >= 10:
		return fmt.Sprintf("%.1f%s", f, u)
	default:
		return fmt.Sprintf("%.2f%s", f, u)
	}
}

var clockDurRe = regexp.MustCompile(`^(?:(\d+) days? )?(\d+):(\d\d):(\d\d)$`)

// NiceClockDur compresses zpool's duration format ("11:12:02",
// "2 days 03:04:05") into "11h12m" / "2d3h". Unparseable input is returned
// as-is.
func NiceClockDur(s string) string {
	m := clockDurRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return s
	}
	days, _ := strconv.Atoi(m[1])
	h, _ := strconv.Atoi(m[2])
	min, _ := strconv.Atoi(m[3])
	sec, _ := strconv.Atoi(m[4])
	switch {
	case days > 0:
		return fmt.Sprintf("%dd%dh", days, h)
	case h > 0:
		return fmt.Sprintf("%dh%dm", h, min)
	default:
		return fmt.Sprintf("%dm%ds", min, sec)
	}
}

// NiceCtimeDate shortens zpool's ctime-style timestamp
// ("Sun Jul 12 11:36:05 2026") to "Jul 12". Unparseable input is returned
// as-is.
func NiceCtimeDate(s string) string {
	t, err := time.Parse("Mon Jan  2 15:04:05 2006", strings.TrimSpace(s))
	if err != nil {
		return s
	}
	return t.Format("Jan 2")
}

// parseI64 parses a decimal int64, returning -1 for "-" or garbage.
func parseI64(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}
