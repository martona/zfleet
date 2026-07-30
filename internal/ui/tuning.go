package ui

import "time"

// Tuning is the collector cadence policy: the selection's host polls at
// full rate (its inspector shows arc rates, scan lines, dataset io), every
// other host idles at background cadence — still updated in perpetuity,
// just lazily. The iostat streams are exempt from tiering: they fork
// nothing, and they keep every io cell, sparkline and latency ring live at
// 2s for the whole fleet.
type Tuning struct {
	BgStats time.Duration // arc/objsets/vitals off-selection (fg: statsInterval)
	BgPools time.Duration // status/list/roots/props + dataset trees off-selection (fg: poolsInterval)
	BgDisks time.Duration // lsblk/smartctl off-selection (fg: diskInterval)
	Promote time.Duration // cursor dwell on a host before it goes foreground
	Demote  time.Duration // grace after the cursor leaves before it drops back
}

func DefaultTuning() Tuning {
	return Tuning{
		BgStats: 15 * time.Second,
		BgPools: 60 * time.Second,
		BgDisks: 5 * time.Minute,
		Promote: 1 * time.Second,
		Demote:  30 * time.Second,
	}
}

// tickTier runs one host's fg/bg state machine at stats-tick time. The
// dwell requirement is the debounce — the cursor passing through a host's
// rows promotes nothing — and the grace keeps short excursions from
// thrashing the tier. Promotion zeroes the due gates so every collector
// fires at its next tick instead of waiting out a stale background timer.
func (h *hostState) tickTier(selected, dwelled bool, now time.Time, t Tuning) {
	if selected && dwelled {
		h.selLast = now
	}
	fg := !h.selLast.IsZero() && now.Sub(h.selLast) <= t.Demote
	if fg && !h.fg {
		h.statsDue, h.poolsDue, h.disksDue, h.dsDue = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	}
	h.fg = fg
}

// cadence picks a collector's next-due interval by tier.
func (h *hostState) cadence(fg, bg time.Duration) time.Duration {
	if h.fg || bg < fg {
		return fg
	}
	return bg
}
