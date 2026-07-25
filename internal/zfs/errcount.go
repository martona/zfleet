package zfs

import (
	"strconv"
	"strings"
)

// Error counters, numerically. zpool status prints them through zfs_nicenum
// ("0", "12", "1.05K"), and — importantly — does NOT sum children into
// parents: a disk whose read errors parity absorbed shows R 12 while its
// raidz row stays at zero, because the vdev level never failed a request.
// The subtree sums here are zfse's own aggregation, so a pool row can warn
// about a sick disk the pool-level counters never mention.

// ErrCount parses one printed counter into a number; "-" and blanks are
// zero. Suffixes are 1024-based like the nicenum that printed them —
// precision is already lost there, and a verdict only needs magnitude.
func ErrCount(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" || s == "0" {
		return 0
	}
	mult := float64(1)
	switch s[len(s)-1] {
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	case 'T':
		mult = 1 << 40
	}
	if mult > 1 {
		s = s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0
	}
	return int64(v * mult)
}

// ErrSums totals the error counters over a vdev and its whole subtree.
func (v *Vdev) ErrSums() (r, w, c int64) {
	r, w, c = ErrCount(v.ReadErr), ErrCount(v.WriteErr), ErrCount(v.CksumErr)
	for _, child := range v.Children {
		cr, cw, cc := child.ErrSums()
		r, w, c = r+cr, w+cw, c+cc
	}
	return r, w, c
}

// ErrSums totals the error counters over every vdev of every class.
func (p *Pool) ErrSums() (r, w, c int64) {
	for _, cl := range p.Classes {
		for _, v := range cl.Vdevs {
			vr, vw, vc := v.ErrSums()
			r, w, c = r+vr, w+vw, c+vc
		}
	}
	return r, w, c
}
