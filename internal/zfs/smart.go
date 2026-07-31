package zfs

import (
	"encoding/json"
	"strings"
)

// SMART, distilled. smartctl -j does the vendor-attribute zoo-keeping;
// what zfleet needs from each drive is: is it dying (smart_status), is it
// warning (the critical counters health.sh taught us to fear), how hot,
// how old, and how much has passed through it.

type Smart struct {
	HaveStatus bool
	Passed     bool // false = the drive itself predicts failure
	TempC      int  // -1 unknown
	TempHigh   int  // device-stated max operating temp (SCT op limit /
	// NVMe warning threshold), -1 unknown — thresholds are never invented
	TempCrit   int // device-stated critical temp, -1 unknown
	PowerOnH   int64
	ReadBytes  int64 // lifetime host reads, -1 unknown
	WriteBytes int64
	LifeUsed   int          // nvme percentage_used, -1 elsewhere
	SparePct   int          // nvme available_spare, -1 elsewhere
	Checks     []SmartCheck // every check performed, canonical order
	Warns      []string     // the warn-tier reasons, derived from Checks
	Standby    bool         // drive was asleep; politely not woken
}

// SmartCheck is one health check the tool performed on a drive: a stable
// id (the future ack key), a display label, the measured value, and the
// verdict tier. Only checks whose data the drive actually reported exist —
// "all clear" and "not checked" must stay distinguishable.
type SmartCheck struct {
	ID    string // "overall", "ata5", "nvme-spare", "scsi-defects", …
	Label string
	Value string
	Sev   int // CheckOK / CheckWarn / CheckFail
}

const (
	CheckOK = iota
	CheckWarn
	CheckFail
)

// ata attribute ids that spell trouble, by id — names vary per vendor
// (Samsung calls 187 Uncorrectable_Error_Cnt, Seagate Reported_Uncorrect)
var ataCritical = map[int]string{
	5:   "reallocated",
	187: "reported uncorrect",
	197: "pending sectors",
	198: "offline uncorrect",
}

// ataLifetime scales attr 241/242 by the unit the vendor NAMED it in:
// Total_LBAs_* count sectors, but SuperMicro says Lifetime_Writes_GiB and
// others say Host_Writes_32MiB — the attribute name is the unit label.
func ataLifetime(name string, raw int64) int64 {
	switch {
	case strings.Contains(name, "32MiB"):
		return raw * 32 << 20
	case strings.Contains(name, "GiB"):
		return raw << 30
	case strings.Contains(name, "MiB"):
		return raw << 20
	default:
		return raw * 512
	}
}

// ParseSmart digests one `smartctl -j -a -n standby` output.
func ParseSmart(text string) (Smart, bool) {
	var j struct {
		Smartctl struct {
			ExitStatus int `json:"exit_status"`
		} `json:"smartctl"`
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature *struct {
			Current          int `json:"current"`
			OpLimitMax       int `json:"op_limit_max"`       // ATA SCT / NVMe WCTEMP
			CriticalLimitMax int `json:"critical_limit_max"` // NVMe CCTEMP
			LimitMax         int `json:"limit_max"`          // ATA hard limit
		} `json:"temperature"`
		PowerOnTime *struct {
			Hours int64 `json:"hours"`
		} `json:"power_on_time"`
		Nvme *struct {
			CriticalWarning         int64 `json:"critical_warning"`
			AvailableSpare          int   `json:"available_spare"`
			AvailableSpareThreshold int   `json:"available_spare_threshold"`
			PercentageUsed          int   `json:"percentage_used"`
			DataUnitsRead           int64 `json:"data_units_read"`
			DataUnitsWritten        int64 `json:"data_units_written"`
			MediaErrors             int64 `json:"media_errors"`
		} `json:"nvme_smart_health_information_log"`
		Ata *struct {
			Table []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
				Raw  struct {
					Value int64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		GrownDefects *int64 `json:"scsi_grown_defect_list"`
	}
	if json.Unmarshal([]byte(text), &j) != nil {
		return Smart{}, false
	}
	s := Smart{TempC: -1, TempHigh: -1, TempCrit: -1,
		PowerOnH: -1, ReadBytes: -1, WriteBytes: -1, LifeUsed: -1, SparePct: -1}
	// exit bit 1 = device open ok but in low-power mode (-n standby honored)
	s.Standby = j.Smartctl.ExitStatus&2 != 0 && j.SmartStatus == nil
	// every verdict flows through here: the check ledger is the record of
	// what was measured, Warns the warn-tier extract of it
	check := func(id, label, value string, sev int) {
		s.Checks = append(s.Checks, SmartCheck{ID: id, Label: label, Value: value, Sev: sev})
		if sev == CheckWarn {
			s.Warns = append(s.Warns, label+" "+value)
		}
	}
	warnIf := func(bad bool) int {
		if bad {
			return CheckWarn
		}
		return CheckOK
	}
	if j.SmartStatus != nil {
		s.HaveStatus, s.Passed = true, j.SmartStatus.Passed
		if s.Passed {
			check("overall", "smart overall", "PASSED", CheckOK)
		} else {
			check("overall", "smart overall", "FAILED", CheckFail)
		}
	}
	if j.Temperature != nil {
		if j.Temperature.Current > 0 {
			s.TempC = j.Temperature.Current
		}
		// device-stated thresholds, sanity-gated: high must be positive,
		// crit must sit strictly above high (Exos report op==hard limit —
		// an equal or inverted pair keeps high and drops crit)
		if v := j.Temperature.OpLimitMax; v > 0 {
			s.TempHigh = v
		}
		crit := j.Temperature.CriticalLimitMax
		if crit == 0 {
			crit = j.Temperature.LimitMax
		}
		if crit > 0 && (s.TempHigh < 0 || crit > s.TempHigh) {
			s.TempCrit = crit
		}
	}
	if j.PowerOnTime != nil {
		s.PowerOnH = j.PowerOnTime.Hours
	}
	if n := j.Nvme; n != nil {
		s.ReadBytes = n.DataUnitsRead * 512000 // units of 1000×512B, per spec
		s.WriteBytes = n.DataUnitsWritten * 512000
		s.LifeUsed = n.PercentageUsed
		s.SparePct = n.AvailableSpare
		// ledger values are EXACT — the drill is the factfinding surface;
		// rounded counts belong to the zpool badges they came from
		check("nvme-critical", "critical warning", itoa(n.CriticalWarning),
			warnIf(n.CriticalWarning != 0))
		check("nvme-media", "media errors", itoa(n.MediaErrors),
			warnIf(n.MediaErrors > 0))
		// health.sh's line: spare under 95 is news (fabric controllers
		// that lie about spare get the ack treatment when acks land)
		check("nvme-spare", "spare", itoa(int64(n.AvailableSpare))+"%",
			warnIf(n.AvailableSpare < 95))
		check("nvme-life", "life used", itoa(int64(n.PercentageUsed))+"%",
			warnIf(n.PercentageUsed >= 90))
	}
	if j.Ata != nil {
		vals := map[int]int64{}
		present := map[int]bool{}
		for _, a := range j.Ata.Table {
			switch a.ID {
			case 241:
				s.WriteBytes = ataLifetime(a.Name, a.Raw.Value)
			case 242:
				s.ReadBytes = ataLifetime(a.Name, a.Raw.Value)
			default:
				if _, crit := ataCritical[a.ID]; crit {
					vals[a.ID], present[a.ID] = a.Raw.Value, true
				}
			}
		}
		// canonical order regardless of the vendor's table order
		for _, id := range []int{5, 187, 197, 198} {
			if present[id] {
				check("ata"+itoa(int64(id)), ataCritical[id], itoa(vals[id]),
					warnIf(vals[id] > 0))
			}
		}
	}
	if j.GrownDefects != nil {
		check("scsi-defects", "grown defects", itoa(*j.GrownDefects),
			warnIf(*j.GrownDefects > 0))
	}
	return s, true
}
