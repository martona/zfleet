// Package collect produces the raw command output the UI parses. Sources are
// deliberately dumb — they return text, never parsed structures — so the same
// parsers serve live collection, fixture replay, and (future) remote hosts
// over ssh.
package collect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

type Source interface {
	// PoolTexts returns `zpool status` and `zpool list -Hpv` output.
	PoolTexts(ctx context.Context) (status, list string, err error)
	// StatTexts returns arcstats kstat content and one pool-level
	// `zpool iostat` sample.
	StatTexts(ctx context.Context) (arcstats, iostat string, err error)
	Name() string
}

// Exec runs zpool on the local host. One-shot `iostat -Hpy 1 1` is used
// instead of a long-lived stream: -H mode has no sample framing, and a
// self-contained true-rate sample (~1s) per poll needs none.
type Exec struct{}

func (Exec) Name() string { return "live" }

func (Exec) PoolTexts(ctx context.Context) (string, string, error) {
	status, err := exec.CommandContext(ctx, "zpool", "status").Output()
	if err != nil {
		return "", "", err
	}
	list, err := exec.CommandContext(ctx, "zpool", "list", "-Hpv").Output()
	if err != nil {
		return "", "", err
	}
	return string(status), string(list), nil
}

func (Exec) StatTexts(ctx context.Context) (string, string, error) {
	arc, err := os.ReadFile("/proc/spl/kstat/zfs/arcstats")
	if err != nil {
		arc = nil // non-Linux or odd setup: ARC segment will show as unknown
	}
	iostat, err := exec.CommandContext(ctx, "zpool", "iostat", "-Hpy", "1", "1").Output()
	if err != nil {
		return string(arc), "", err
	}
	return string(arc), string(iostat), nil
}

// Replay serves recorded fixture files from a directory, so the TUI runs
// anywhere — no ZFS, no root, no host.
type Replay struct{ Dir string }

func (r Replay) Name() string { return "replay:" + filepath.Base(r.Dir) }

func (r Replay) read(name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(r.Dir, name))
	return string(b), err
}

func (r Replay) PoolTexts(context.Context) (string, string, error) {
	status, err := r.read("zpool-status.out")
	if err != nil {
		return "", "", err
	}
	list, err := r.read("zpool-list-Hpv.out")
	if err != nil {
		return "", "", err
	}
	return status, list, nil
}

func (r Replay) StatTexts(context.Context) (string, string, error) {
	arc, _ := r.read("arcstats.out")
	iostat, err := r.read("zpool-iostat-Hpv.out")
	if err != nil {
		return arc, "", err
	}
	return arc, iostat, nil
}
