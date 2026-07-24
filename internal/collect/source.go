// Package collect produces the raw command output the UI parses. Sources are
// deliberately dumb — they return text, never parsed structures — so the same
// parsers serve live collection, fixture replay, and (future) remote hosts
// over ssh.
package collect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/martona/zfs-explorer/internal/zfs"
)

type Source interface {
	// PoolTexts returns `zpool status` and `zpool list -Hpv` output.
	PoolTexts(ctx context.Context) (status, list string, err error)
	// StatTexts returns arcstats kstat content, one pool-level
	// `zpool iostat` sample, and the concatenated per-dataset objset
	// kstats ("" when unavailable).
	StatTexts(ctx context.Context) (arcstats, iostat, objsets string, err error)
	// DatasetTexts returns wide `zfs list` output covering at least the
	// given pool; parsers filter, so a superset is fine.
	DatasetTexts(ctx context.Context, pool string) (string, error)
	// SnapshotTexts returns snapshot list output covering at least the
	// given dataset's own snapshots.
	SnapshotTexts(ctx context.Context, ds string) (string, error)
	// PropTexts returns `zfs get -Hp all` output for the dataset (or a
	// superset; "" when unavailable).
	PropTexts(ctx context.Context, ds string) (string, error)
	// RootTexts returns `zfs list -Hp -d 0 -o name,used,logicalused` —
	// per-pool charged vs logical totals.
	RootTexts(ctx context.Context) (string, error)
	// DestroyDryRun runs `zfs destroy -n -v` on a snapshot list
	// ("ds@a,b,c") and returns its output. The -n flag is hardcoded; this
	// never destroys anything.
	DestroyDryRun(ctx context.Context, target string) (string, error)
	// PoolProps returns `zpool get -Hp ashift` output.
	PoolProps(ctx context.Context) (string, error)
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

func (Exec) StatTexts(ctx context.Context) (string, string, string, error) {
	arc, err := os.ReadFile("/proc/spl/kstat/zfs/arcstats")
	if err != nil {
		arc = nil // non-Linux or odd setup: ARC segment will show as unknown
	}
	var objsets strings.Builder
	if paths, _ := filepath.Glob("/proc/spl/kstat/zfs/*/objset-*"); paths != nil {
		for _, p := range paths {
			if b, err := os.ReadFile(p); err == nil { // files vanish on unmount
				objsets.Write(b)
			}
		}
	}
	iostat, err := exec.CommandContext(ctx, "zpool", "iostat", "-Hpy", "1", "1").Output()
	if err != nil {
		return string(arc), "", objsets.String(), err
	}
	return string(arc), string(iostat), objsets.String(), nil
}

func (Exec) DatasetTexts(ctx context.Context, pool string) (string, error) {
	out, err := exec.CommandContext(ctx, "zfs", "list", "-Hp", "-r",
		"-t", "filesystem,volume", "-o", zfs.DatasetFields, pool).Output()
	return string(out), err
}

func (Exec) SnapshotTexts(ctx context.Context, ds string) (string, error) {
	out, err := exec.CommandContext(ctx, "zfs", "list", "-Hp", "-d", "1",
		"-t", "snapshot", "-s", "creation", "-o", zfs.SnapshotFields, ds).Output()
	return string(out), err
}

func (Exec) PropTexts(ctx context.Context, ds string) (string, error) {
	out, err := exec.CommandContext(ctx, "zfs", "get", "-Hp", "all", ds).Output()
	return string(out), err
}

func (Exec) RootTexts(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "zfs", "list", "-Hp", "-d", "0",
		"-o", "name,used,logicalused").Output()
	return string(out), err
}

func (Exec) PoolProps(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "zpool", "get", "-Hp", "ashift").Output()
	return string(out), err
}

func (Exec) DestroyDryRun(ctx context.Context, target string) (string, error) {
	// args are passed as a vector (no shell), and -n is pinned here
	out, err := exec.CommandContext(ctx, "zfs", "destroy", "-n", "-v", target).CombinedOutput()
	return string(out), err
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

func (r Replay) StatTexts(context.Context) (string, string, string, error) {
	arc, _ := r.read("arcstats.out")
	objsets, _ := r.read("objset-all.out") // absent in pre-v2 fixture dirs
	iostat, err := r.read("zpool-iostat-Hpv.out")
	if err != nil {
		return arc, "", objsets, err
	}
	return arc, iostat, objsets, nil
}

// Replay fixture files cover all pools/datasets at once; parsers filter.

func (r Replay) DatasetTexts(context.Context, string) (string, error) {
	return r.read("zfs-list-wide.out")
}

func (r Replay) SnapshotTexts(context.Context, string) (string, error) {
	return r.read("zfs-list-snapshots.out")
}

func (r Replay) PropTexts(context.Context, string) (string, error) {
	// concatenate whatever get-all captures exist; ParseProps filters by name
	var b strings.Builder
	for _, f := range []string{"zfs-get-all-rust.out", "zfs-get-all-fs.out", "zfs-get-all-zvol.out"} {
		if s, err := r.read(f); err == nil {
			b.WriteString(s)
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

func (r Replay) RootTexts(context.Context) (string, error) {
	// derive name/used/logicalused for pool roots from the wide capture;
	// pre-v2 fixtures lack the logical columns and yield "-"
	wide, err := r.read("zfs-list-wide.out")
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, line := range strings.Split(wide, "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 27 || strings.ContainsRune(f[0], '/') {
			continue
		}
		logical := "-"
		if len(f) >= 29 {
			logical = f[27]
		}
		b.WriteString(f[0] + "\t" + f[2] + "\t" + logical + "\n")
	}
	return b.String(), nil
}

func (r Replay) PoolProps(context.Context) (string, error) {
	return r.read("zpool-ashift.out") // absent in pre-v2 fixture dirs
}

func (Replay) DestroyDryRun(context.Context, string) (string, error) {
	return "", errors.New("dry-run needs a live system")
}
