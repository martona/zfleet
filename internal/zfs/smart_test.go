package zfs

import (
	"path/filepath"
	"testing"
)

func TestParseSmartNvme(t *testing.T) {
	s, ok := ParseSmart(readFixture(t,
		filepath.Join("../../testdata/fixtures/lemur/2026-07-25-smart", "smart-nvme0n1.json")))
	if !ok || !s.HaveStatus || !s.Passed {
		t.Fatalf("nvme parse = %+v ok=%v", s, ok)
	}
	if s.TempC != 36 || s.SparePct != 100 || s.LifeUsed != 0 {
		t.Errorf("temp/spare/life = %d/%d/%d, want 36/100/0", s.TempC, s.SparePct, s.LifeUsed)
	}
	if s.ReadBytes != 7832491*512000 || s.WriteBytes != 45368*512000 {
		t.Errorf("r/w = %d/%d, want unit-scaled", s.ReadBytes, s.WriteBytes)
	}
	if len(s.Warns) != 0 {
		t.Errorf("healthy drive warned: %v", s.Warns)
	}
}

func TestParseSmartAta(t *testing.T) {
	s, ok := ParseSmart(readFixture(t,
		filepath.Join("../../testdata/fixtures/commodoreplus4/2026-07-25-smart", "smart-sde.json")))
	if !ok || !s.Passed {
		t.Fatalf("ata parse = %+v ok=%v", s, ok)
	}
	if s.TempC != 45 || s.PowerOnH != 13446 {
		t.Errorf("temp/poh = %d/%d, want 45/13446", s.TempC, s.PowerOnH)
	}
	if s.WriteBytes != 102071584471*512 || s.ReadBytes != 2224360598667*512 {
		t.Errorf("lifetime r/w wrong: %d/%d", s.ReadBytes, s.WriteBytes)
	}
	if len(s.Warns) != 0 {
		t.Errorf("healthy Exos warned: %v", s.Warns)
	}
}

// The SuperMicro pair: GiB-named lifetime units, and sdd's genuine 52
// reported-uncorrect events — the tool's first real catch.
func TestParseSmartSuperMicro(t *testing.T) {
	dir := "../../testdata/fixtures/commodoreplus4/2026-07-25-smart"
	c, ok := ParseSmart(readFixture(t, filepath.Join(dir, "smart-sdc.json")))
	if !ok || len(c.Warns) != 0 {
		t.Fatalf("sdc = %+v", c)
	}
	if c.WriteBytes != 4325<<30 || c.ReadBytes != 699<<30 {
		t.Errorf("sdc lifetime = %d/%d, want GiB-scaled", c.ReadBytes, c.WriteBytes)
	}
	d, _ := ParseSmart(readFixture(t, filepath.Join(dir, "smart-sdd.json")))
	if len(d.Warns) != 1 || d.Warns[0] != "reported uncorrect 52" {
		t.Errorf("sdd warns = %v, want the 52 reported-uncorrect", d.Warns)
	}
	// the check ledger behind the warn: overall PASSED ok, ata187 warn 52
	byID := map[string]SmartCheck{}
	for _, c := range d.Checks {
		byID[c.ID] = c
	}
	if c := byID["overall"]; c.Value != "PASSED" || c.Sev != CheckOK {
		t.Errorf("sdd overall check = %+v", c)
	}
	if c := byID["ata187"]; c.Value != "52" || c.Sev != CheckWarn || c.Label != "reported uncorrect" {
		t.Errorf("sdd ata187 check = %+v", c)
	}
}

func TestSmartTempThresholds(t *testing.T) {
	// the nvme fixture states its own WCTEMP/CCTEMP
	s, ok := ParseSmart(readFixture(t,
		filepath.Join("../../testdata/fixtures/lemur/2026-07-25-smart", "smart-nvme0n1.json")))
	if !ok || s.TempHigh != 82 || s.TempCrit != 85 {
		t.Errorf("nvme thresholds = %d/%d, want 82/85", s.TempHigh, s.TempCrit)
	}
	// Exos shape: op limit == hard limit — sanity keeps high, drops crit
	exos := `{"temperature":{"current":61,"op_limit_max":60,"limit_max":60}}`
	if s, ok := ParseSmart(exos); !ok || s.TempHigh != 60 || s.TempCrit != -1 {
		t.Errorf("exos = high %d crit %d, want 60/-1", s.TempHigh, s.TempCrit)
	}
	// absent thresholds never validate — no invented limits
	if s, _ := ParseSmart(`{"temperature":{"current":40}}`); s.TempHigh != -1 || s.TempCrit != -1 {
		t.Errorf("bare = %d/%d, want -1/-1", s.TempHigh, s.TempCrit)
	}
}

func TestParseSmartVerdicts(t *testing.T) {
	pending := `{"smart_status":{"passed":true},"temperature":{"current":41},
		"ata_smart_attributes":{"table":[{"id":197,"raw":{"value":8}},{"id":5,"raw":{"value":0}}]}}`
	if s, ok := ParseSmart(pending); !ok || len(s.Warns) != 1 || s.Warns[0] != "pending sectors 8" {
		t.Errorf("pending case = %+v", s)
	} else {
		// ledger: overall + the two PRESENT attrs (5 ok, 197 warn) in
		// canonical order — absent attrs must not fabricate ok rows
		if len(s.Checks) != 3 || s.Checks[1].ID != "ata5" || s.Checks[1].Sev != CheckOK ||
			s.Checks[2].ID != "ata197" || s.Checks[2].Sev != CheckWarn {
			t.Errorf("pending checks = %+v", s.Checks)
		}
	}
	spare := `{"smart_status":{"passed":true},
		"nvme_smart_health_information_log":{"available_spare":80,"available_spare_threshold":10,
		"percentage_used":91,"data_units_read":1,"data_units_written":1}}`
	if s, _ := ParseSmart(spare); len(s.Warns) != 2 {
		t.Errorf("spare+life case = %+v", s.Warns)
	}
	failed := `{"smart_status":{"passed":false},"temperature":{"current":55}}`
	if s, _ := ParseSmart(failed); !s.HaveStatus || s.Passed {
		t.Error("failed drive parsed as passing")
	} else if len(s.Checks) != 1 || s.Checks[0].Value != "FAILED" || s.Checks[0].Sev != CheckFail {
		t.Errorf("failed checks = %+v", s.Checks)
	}
	standby := `{"smartctl":{"exit_status":2}}`
	if s, _ := ParseSmart(standby); !s.Standby {
		t.Error("standby not detected")
	}
	if _, ok := ParseSmart("not json"); ok {
		t.Error("garbage accepted")
	}
}

func TestSmartOdometers(t *testing.T) {
	// SN01-shaped Exos: attr 242 (and devstat, which mirrors it — same
	// firmware counter) carries a factory offset; FARM has field truth
	base := `{"smart_status":{"passed":true},"logical_block_size":512,
		"ata_smart_attributes":{"table":[
		{"id":241,"name":"Total_LBAs_Written","raw":{"value":1000000}},
		{"id":242,"name":"Total_LBAs_Read","raw":{"value":84886080}}]},
		"ata_device_statistics":{"pages":[{"table":[
		{"name":"Logical Sectors Written","value":1000123},
		{"name":"Logical Sectors Read","value":84886203},
		{"name":"Power-on Hours"}]}]}}`
	farm := `{"seagate_farm_log":{
		"page_1_drive_information":{"logical_sector_size":512},
		"page_2_workload_statistics":{"logical_sectors_written":1000000,"logical_sectors_read":1000000}}}`
	s, ok := ParseSmart(base + "\n" + farm)
	if !ok || s.FarmReadBytes != 512000000 || s.FarmWriteBytes != 512000000 {
		t.Fatalf("farm odometers = %d/%d", s.FarmReadBytes, s.FarmWriteBytes)
	}
	if s.DsReadBytes != 84886203*512 || s.DsWriteBytes != 1000123*512 {
		t.Fatalf("devstat odometers = %d/%d", s.DsReadBytes, s.DsWriteBytes)
	}
	// trust order tier 1: FARM wins where present
	if s.OdoRead() != 512000000 || s.OdoWrite() != 512000000 {
		t.Errorf("odo = %d/%d, want farm values", s.OdoRead(), s.OdoWrite())
	}
	// FARM informs display only — it must never mint a check
	for _, c := range s.Checks {
		if c.ID != "overall" {
			t.Errorf("unexpected check %+v", c)
		}
	}
	// tier 2: no farm document — devstat over the attribute pair
	if s, _ := ParseSmart(base); s.OdoRead() != 84886203*512 || s.OdoWrite() != 1000123*512 {
		t.Errorf("devstat tier = %d/%d", s.OdoRead(), s.OdoWrite())
	}
	// tier 3: attributes alone
	attrs := `{"logical_block_size":512,"ata_smart_attributes":{"table":[
		{"id":242,"name":"Total_LBAs_Read","raw":{"value":84886080}}]}}`
	if s, _ := ParseSmart(attrs); s.OdoRead() != 84886080*512 || s.OdoWrite() != -1 {
		t.Errorf("attr tier = %d/%d", s.OdoRead(), s.OdoWrite())
	}
}
