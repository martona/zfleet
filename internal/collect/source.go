// Package collect produces the raw command output the UI parses. Sources are
// deliberately dumb — they return text, never parsed structures — so the same
// parsers serve live collection, fixture replay, and (future) remote hosts
// over ssh.
package collect

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/martona/zfs-explorer/internal/zfs"
)

type Source interface {
	// PoolTexts returns `zpool status` and `zpool list -Hpv` output.
	PoolTexts(ctx context.Context) (status, list string, err error)
	// StatTexts returns arcstats kstat content and the concatenated
	// per-dataset objset kstats ("" when unavailable). It doubles as the
	// 2s connection heartbeat; iostat samples arrive via IostatStream.
	StatTexts(ctx context.Context) (arcstats, objsets string, err error)
	// IostatStream returns the raw output of a long-lived
	// `zpool iostat -Hpvly -T u 2` covering all pools with per-vdev
	// latency columns. Blocks are framed by `-T u` timestamp lines
	// (bare epoch integers); consecutive timestamps occur (-y suppresses
	// the boot sample but not its timestamp). The kernel times the
	// intervals, so every sample is a true 2s window — no forked
	// one-shots, no unobserved gaps between them. Close terminates the
	// underlying process.
	IostatStream(ctx context.Context) (io.ReadCloser, error)
	// DatasetTexts returns wide `zfs list` output covering at least the
	// given pool; parsers filter, so a superset is fine.
	DatasetTexts(ctx context.Context, pool string) (string, error)
	// SnapshotTexts returns snapshot list output covering at least the
	// given dataset's own snapshots.
	SnapshotTexts(ctx context.Context, ds string) (string, error)
	// PoolSnapshotTexts returns snapshot list output covering at least the
	// whole pool, recursively — one command per pool is what makes the
	// fleet-wide snapshot sweep affordable.
	PoolSnapshotTexts(ctx context.Context, pool string) (string, error)
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
	// Destroy runs `zfs destroy` FOR REAL — the most destructive command
	// in the tool. recursive adds -r (whole-dataset marks mean the
	// subtree); sudo prefixes `sudo -n` for hosts where the probe granted
	// it. The UI shows the operator this exact command before it runs.
	Destroy(ctx context.Context, target string, recursive, sudo bool) (string, error)
	// PoolClear runs `zpool clear pool [vdev]` — the second write command,
	// reachable only through the warnings popup, which shows the operator
	// the verbatim line. Resets error counters; the ereports stay in the
	// journal and the clear itself lands in zpool history.
	PoolClear(ctx context.Context, pool, vdev string, sudo bool) (string, error)
	// PerfTexts returns the kstat surfaces for one pool's perf blocks:
	// txgs ring, dmu_tx counters, zil counters, module params. All
	// instant file reads; vdev latency comes from IostatStream.
	PerfTexts(ctx context.Context, pool string) (txgs, dmuTx, zil, params string, err error)
	// PoolProps returns `zpool get -Hp ashift,bcloneused,bclonesaved`
	// output. Parsers pick their lines by property name, so fixtures
	// captured before a property joined the list simply lack it.
	PoolProps(ctx context.Context) (string, error)
	// HostTexts returns per-tick host vitals: /proc/uptime, /proc/loadavg,
	// /proc/stat, and hwmon temp files in `grep -H` path:value form. All
	// best-effort — parsers tolerate empty strings.
	HostTexts(ctx context.Context) (uptime, loadavg, stat, hwmon string)
	// InfoTexts returns once-per-connect host identity: `zfs version`
	// output, the kernel release, and /etc/os-release. Best-effort.
	InfoTexts(ctx context.Context) (zfsVer, kernel, osRelease string)
	// DiskTexts returns the disk-resolution surfaces: the /dev/disk
	// symlink universe, the /sys/class/block map, the lsblk inventory,
	// and hwmon chip→device links. Best-effort.
	DiskTexts(ctx context.Context) (aliases, sysBlock, lsblk, hwmonDev string)
	// SmartTexts probes for passwordless sudo and, when granted, returns
	// `smartctl -j -x -n standby` output per disk node. sudoOK reports the
	// probe — false means the degraded no-root experience, not an error.
	SmartTexts(ctx context.Context, nodes []string) (texts map[string]string, sudoOK bool)
	Name() string
}

// Exec runs zpool on the local host.
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
	var objsets strings.Builder
	if paths, _ := filepath.Glob("/proc/spl/kstat/zfs/*/objset-*"); paths != nil {
		for _, p := range paths {
			if b, err := os.ReadFile(p); err == nil { // files vanish on unmount
				objsets.Write(b)
			}
		}
	}
	return string(arc), objsets.String(), nil
}

func (Exec) IostatStream(ctx context.Context) (io.ReadCloser, error) {
	return streamCmd(exec.CommandContext(ctx, "zpool", "iostat", "-Hpvly", "-T", "u", "2"))
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

func (Exec) PoolSnapshotTexts(ctx context.Context, pool string) (string, error) {
	out, err := exec.CommandContext(ctx, "zfs", "list", "-Hp", "-r",
		"-t", "snapshot", "-s", "creation", "-o", zfs.SnapshotFields, pool).Output()
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
	out, err := exec.CommandContext(ctx, "zpool", "get", "-Hp", "ashift,bcloneused,bclonesaved").Output()
	return string(out), err
}

func (Exec) HostTexts(ctx context.Context) (string, string, string, string) {
	readFile := func(p string) string {
		b, _ := os.ReadFile(p)
		return string(b)
	}
	// hwmon files render as path:value lines — the same shape `grep -H .`
	// produces over ssh, so one parser serves both collectors
	var hw strings.Builder
	for _, pat := range []string{"/sys/class/hwmon/hwmon*/name",
		"/sys/class/hwmon/hwmon*/temp*_input", "/sys/class/hwmon/hwmon*/temp*_label",
		"/sys/class/hwmon/hwmon*/temp*_max", "/sys/class/hwmon/hwmon*/temp*_crit"} {
		paths, _ := filepath.Glob(pat)
		for _, p := range paths {
			if b, err := os.ReadFile(p); err == nil {
				hw.WriteString(p + ":" + strings.TrimSpace(string(b)) + "\n")
			}
		}
	}
	return readFile("/proc/uptime"), readFile("/proc/loadavg"),
		readFile("/proc/stat"), hw.String()
}

func (Exec) InfoTexts(ctx context.Context) (string, string, string) {
	ver, _ := exec.CommandContext(ctx, "zfs", "version").Output()
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	osrel, _ := os.ReadFile("/etc/os-release")
	return string(ver), strings.TrimSpace(string(kernel)), string(osrel)
}

func (Exec) DiskTexts(ctx context.Context) (string, string, string, string) {
	var a strings.Builder
	filepath.WalkDir("/dev/disk", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if target, err := os.Readlink(path); err == nil {
			a.WriteString(path + " " + target + "\n")
		}
		return nil
	})
	var sb strings.Builder
	if ents, err := os.ReadDir("/sys/class/block"); err == nil {
		for _, e := range ents {
			if t, err := os.Readlink("/sys/class/block/" + e.Name()); err == nil {
				sb.WriteString(e.Name() + " " + t + "\n")
			}
		}
	}
	lsblk, _ := exec.CommandContext(ctx, "lsblk", "-bdP", "-o", "NAME,SIZE,ROTA,MODEL,SERIAL").Output()
	var hd strings.Builder
	if hs, _ := filepath.Glob("/sys/class/hwmon/hwmon*"); hs != nil {
		for _, h := range hs {
			if dev, err := filepath.EvalSymlinks(h + "/device"); err == nil {
				hd.WriteString(h + " " + dev + "\n")
			}
		}
	}
	return a.String(), sb.String(), string(lsblk), hd.String()
}

func (Exec) SmartTexts(ctx context.Context, nodes []string) (map[string]string, bool) {
	if exec.CommandContext(ctx, "sudo", "-n", "true").Run() != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, n := range nodes {
		// smartctl exits nonzero for logged errors while still emitting
		// full JSON — keep whatever stdout arrived
		// -x (not -a) so ATA drives report their SCT temperature limits —
		// the device-stated thresholds the temp tinting refuses to invent
		b, _ := exec.CommandContext(ctx, "sudo", "-n", "smartctl",
			"-j", "-x", "-n", "standby", "/dev/"+n).Output()
		if len(b) > 0 {
			out[n] = string(b)
		}
	}
	return out, true
}

func (Exec) DestroyDryRun(ctx context.Context, target string) (string, error) {
	// args are passed as a vector (no shell), and -n is pinned here. -p
	// makes the reclaim figure machine-readable for the Σ math.
	out, err := exec.CommandContext(ctx, "zfs", "destroy", "-n", "-v", "-p", target).CombinedOutput()
	return string(out), err
}

func (Exec) Destroy(ctx context.Context, target string, recursive, sudo bool) (string, error) {
	// no -f: a busy mount fails loudly instead of being force-unmounted
	args := []string{"zfs", "destroy"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, target)
	if sudo {
		args = append([]string{"sudo", "-n"}, args...)
	}
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	return string(out), err
}

func (Exec) PoolClear(ctx context.Context, pool, vdev string, sudo bool) (string, error) {
	args := []string{"zpool", "clear", pool}
	if vdev != "" {
		args = append(args, vdev)
	}
	if sudo {
		args = append([]string{"sudo", "-n"}, args...)
	}
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	return string(out), err
}

func (Exec) PerfTexts(ctx context.Context, pool string) (string, string, string, string, error) {
	readFile := func(p string) string {
		b, _ := os.ReadFile(p)
		return string(b)
	}
	txgs := readFile("/proc/spl/kstat/zfs/" + pool + "/txgs")
	dmuTx := readFile("/proc/spl/kstat/zfs/dmu_tx")
	// newer kmods have per-pool zil kstats; fall back to the global one
	zil := readFile("/proc/spl/kstat/zfs/" + pool + "/zil")
	if strings.TrimSpace(zil) == "" {
		zil = readFile("/proc/spl/kstat/zfs/zil")
	}
	var params strings.Builder
	for _, p := range []string{"zfs_dirty_data_max", "zfs_delay_min_dirty_percent", "zfs_dirty_data_sync_percent"} {
		full := "/sys/module/zfs/parameters/" + p
		if b, err := os.ReadFile(full); err == nil {
			params.WriteString(full + ":" + strings.TrimSpace(string(b)) + "\n")
		}
	}
	return txgs, dmuTx, zil, params.String(), nil
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
	arc, err := r.read("arcstats.out")
	objsets, _ := r.read("objset-all.out") // absent in pre-v2 fixture dirs
	return arc, objsets, err
}

func (r Replay) IostatStream(context.Context) (io.ReadCloser, error) {
	// one synthetic block: the latency capture first (only it has the
	// ≥17-column rows the latency parser sees), then the all-pools sample
	// whose 7-column rows win the pool-io parse by coming last
	hpvly, _ := r.read("zpool-iostat-Hpvly.out")
	hpv, err := r.read("zpool-iostat-Hpv.out")
	if err != nil && hpvly == "" {
		return nil, err
	}
	return io.NopCloser(strings.NewReader(hpvly + "\n" + hpv)), nil
}

// Replay fixture files cover all pools/datasets at once; parsers filter.

func (r Replay) DatasetTexts(context.Context, string) (string, error) {
	return r.read("zfs-list-wide.out")
}

func (r Replay) SnapshotTexts(context.Context, string) (string, error) {
	return r.read("zfs-list-snapshots.out")
}

func (r Replay) PoolSnapshotTexts(context.Context, string) (string, error) {
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

func (r Replay) HostTexts(context.Context) (string, string, string, string) {
	// all absent in pre-multi-host fixture dirs; vitals simply don't render
	up, _ := r.read("uptime.out")
	load, _ := r.read("loadavg.out")
	stat, _ := r.read("proc-stat.out")
	hwmon, _ := r.read("hwmon.out")
	return up, load, stat, hwmon
}

func (r Replay) InfoTexts(context.Context) (string, string, string) {
	ver, _ := r.read("zfs-version.out")
	kernel, _ := r.read("kernel.out")
	osrel, _ := r.read("os-release.out")
	return ver, strings.TrimSpace(kernel), osrel
}

func (r Replay) DiskTexts(context.Context) (string, string, string, string) {
	aliases, _ := r.read("disk-aliases.out")
	sysBlock, _ := r.read("sys-block.out")
	lsblk, _ := r.read("lsblk-disks.out")
	hwmonDev, _ := r.read("hwmon-dev.out")
	return aliases, sysBlock, lsblk, hwmonDev
}

func (r Replay) SmartTexts(_ context.Context, nodes []string) (map[string]string, bool) {
	if _, err := r.read("sudo-ok"); err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, n := range nodes {
		if s, err := r.read("smart-" + n + ".json"); err == nil {
			out[n] = s
		}
	}
	return out, true
}

func (Replay) DestroyDryRun(context.Context, string) (string, error) {
	return "", errors.New("dry-run needs a live system")
}

// Destroy simulates success so the sick puppet can exercise the whole F8
// surface; fixtures re-read on the next tick, so the "destroyed" data
// calmly returns.
func (Replay) Destroy(context.Context, string, bool, bool) (string, error) {
	return "", nil
}

// PoolClear simulates success; the fixture's counters calmly return on
// the next tick, which is exactly what a sick puppet is for.
func (Replay) PoolClear(context.Context, string, string, bool) (string, error) {
	return "", nil
}

func (r Replay) PerfTexts(_ context.Context, pool string) (string, string, string, string, error) {
	txgs, err := r.read("txgs-" + pool + ".out")
	if err != nil {
		return "", "", "", "", err
	}
	dmuTx, _ := r.read("dmu-tx.out")
	zil, _ := r.read("zil.out")
	params, _ := r.read("zfs-params.out")
	return txgs, dmuTx, zil, params, nil
}
