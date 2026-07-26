package zfs

import (
	"encoding/json"
	"strings"
)

// SMART, distilled. smartctl -j does the vendor-attribute zoo-keeping;
// what zfse needs from each drive is: is it dying (smart_status), is it
// warning (the critical counters health.sh taught us to fear), how hot,
// how old, and how much has passed through it.

type Smart struct {
	HaveStatus bool
	Passed     bool // false = the drive itself predicts failure
	TempC      int  // -1 unknown
	PowerOnH   int64
	ReadBytes  int64 // lifetime host reads, -1 unknown
	WriteBytes int64
	LifeUsed   int      // nvme percentage_used, -1 elsewhere
	SparePct   int      // nvme available_spare, -1 elsewhere
	Warns      []string // nonempty = warn tier; the reasons, drill fodder
	Standby    bool     // drive was asleep; politely not woken
}

// ata attribute ids that spell trouble, by id — names vary per vendor
// (Samsung calls 187 Uncorrectable_Error_Cnt, Seagate Reported_Uncorrect)
var ataCritical = map[int]string{
	5:   "realloc",
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
			Current int `json:"current"`
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
	s := Smart{TempC: -1, PowerOnH: -1, ReadBytes: -1, WriteBytes: -1, LifeUsed: -1, SparePct: -1}
	// exit bit 1 = device open ok but in low-power mode (-n standby honored)
	s.Standby = j.Smartctl.ExitStatus&2 != 0 && j.SmartStatus == nil
	if j.SmartStatus != nil {
		s.HaveStatus, s.Passed = true, j.SmartStatus.Passed
	}
	if j.Temperature != nil && j.Temperature.Current > 0 {
		s.TempC = j.Temperature.Current
	}
	if j.PowerOnTime != nil {
		s.PowerOnH = j.PowerOnTime.Hours
	}
	warn := func(msg string) { s.Warns = append(s.Warns, msg) }
	if n := j.Nvme; n != nil {
		s.ReadBytes = n.DataUnitsRead * 512000 // units of 1000×512B, per spec
		s.WriteBytes = n.DataUnitsWritten * 512000
		s.LifeUsed = n.PercentageUsed
		s.SparePct = n.AvailableSpare
		if n.CriticalWarning != 0 {
			warn("critical warning")
		}
		if n.MediaErrors > 0 {
			warn("media errors " + NiceCount(n.MediaErrors))
		}
		// health.sh's line: spare under 95 is news (fabric controllers
		// that lie about spare get the ack treatment in phase 3)
		if n.AvailableSpare < 95 {
			warn("spare " + itoa(int64(n.AvailableSpare)) + "%")
		}
		if n.PercentageUsed >= 90 {
			warn("life used " + itoa(int64(n.PercentageUsed)) + "%")
		}
	}
	if j.Ata != nil {
		for _, a := range j.Ata.Table {
			switch a.ID {
			case 241:
				s.WriteBytes = ataLifetime(a.Name, a.Raw.Value)
			case 242:
				s.ReadBytes = ataLifetime(a.Name, a.Raw.Value)
			default:
				if name, crit := ataCritical[a.ID]; crit && a.Raw.Value > 0 {
					warn(name + " " + NiceCount(a.Raw.Value))
				}
			}
		}
	}
	if j.GrownDefects != nil && *j.GrownDefects > 0 {
		warn("grown defects " + NiceCount(*j.GrownDefects))
	}
	return s, true
}
