package collect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/martona/zfs-explorer/internal/zfs"
)

// Ssh runs the same read-only commands as Exec on a remote host, through the
// system ssh client so the user's keys, aliases and known_hosts apply
// verbatim. BatchMode keeps it from ever prompting; a ControlMaster socket
// (where the platform supports one) makes per-command cost a few
// milliseconds after the first connect.
type Ssh struct {
	Dest  string // ssh destination: "host", "user@host", or a config alias
	Label string // display name — the first hostname label
}

func (s Ssh) Name() string { return "ssh:" + s.Label }

func sshArgs(dest string) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5"}
	if runtime.GOOS != "windows" {
		// Windows OpenSSH lacks multiplexing; elsewhere the shared master
		// turns every follow-up command into a cheap channel open
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath=~/.ssh/zfse-%C",
			"-o", "ControlPersist=60s")
	}
	return append(args, "--", dest)
}

// run executes one remote command line and returns its stdout, folding the
// first stderr line into the error so failures read as themselves.
func (s Ssh) run(ctx context.Context, cmdline string) (string, error) {
	out, err := exec.CommandContext(ctx, "ssh", append(sshArgs(s.Dest), cmdline)...).Output()
	return string(out), s.decorate(err)
}

// runCombined mirrors Exec's CombinedOutput call sites (the dry-run, whose
// interesting output can land on stderr).
func (s Ssh) runCombined(ctx context.Context, cmdline string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", append(sshArgs(s.Dest), cmdline)...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), s.decorate(err)
}

// sshError carries a readable message while keeping the original error in
// the chain — transportDown/cmdMissing need the exit code to survive the
// prettification, or every failure with stderr reads as an outage.
type sshError struct {
	msg string
	err error
}

func (e *sshError) Error() string { return e.msg }
func (e *sshError) Unwrap() error { return e.err }

func (s Ssh) decorate(err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if msg := firstLine(string(ee.Stderr)); msg != "" {
			return &sshError{fmt.Sprintf("%s: %s", s.Dest, msg), err}
		}
	}
	return fmt.Errorf("%s: %w", s.Dest, err)
}

// transportDown reports whether an error is ssh failing to reach the host at
// all (exit 255) rather than a remote command failing — the signal to stop
// burning connect timeouts on the remaining commands of a batch.
func transportDown(err error) bool {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode() == 255
	}
	return err != nil // exec-level failure (ssh missing, ctx timeout)
}

// cmdMissing reports the remote shell's "command not found" (exit 127) —
// the mark of a basic host with no zfs installed. Such hosts are legal
// fleet members: reachable, vitals-bearing, zero pools.
func cmdMissing(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 127
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// quote wraps a string for the remote shell. Command lines here are fixed
// strings plus pool/dataset names, all quoted through this.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (s Ssh) PoolTexts(ctx context.Context) (string, string, error) {
	status, err := s.run(ctx, "zpool status")
	if err != nil {
		if cmdMissing(err) {
			return "", "", nil // a basic host: reachable, no zfs, no pools
		}
		return "", "", err
	}
	list, err := s.run(ctx, "zpool list -Hpv")
	if err != nil {
		return "", "", err
	}
	return status, list, nil
}

func (s Ssh) StatTexts(ctx context.Context) (string, string, string, error) {
	arc, err := s.run(ctx, "cat /proc/spl/kstat/zfs/arcstats")
	if err != nil && transportDown(err) {
		return "", "", "", err
	}
	objsets, _ := s.run(ctx, "cat /proc/spl/kstat/zfs/*/objset-* 2>/dev/null")
	iostat, err := s.run(ctx, "zpool iostat -Hpy 1 1")
	if err != nil {
		if transportDown(err) {
			return arc, "", objsets, err
		}
		// any command-level failure — zpool missing, module not loaded —
		// is not an outage: the stats heartbeat answers for the HOST
		return arc, "", objsets, nil
	}
	return arc, iostat, objsets, nil
}

func (s Ssh) DatasetTexts(ctx context.Context, pool string) (string, error) {
	return s.run(ctx, "zfs list -Hp -r -t filesystem,volume -o "+zfs.DatasetFields+" "+quote(pool))
}

func (s Ssh) SnapshotTexts(ctx context.Context, ds string) (string, error) {
	return s.run(ctx, "zfs list -Hp -d 1 -t snapshot -s creation -o "+zfs.SnapshotFields+" "+quote(ds))
}

func (s Ssh) PoolSnapshotTexts(ctx context.Context, pool string) (string, error) {
	return s.run(ctx, "zfs list -Hp -r -t snapshot -s creation -o "+zfs.SnapshotFields+" "+quote(pool))
}

func (s Ssh) PropTexts(ctx context.Context, ds string) (string, error) {
	return s.run(ctx, "zfs get -Hp all "+quote(ds))
}

func (s Ssh) RootTexts(ctx context.Context) (string, error) {
	return s.run(ctx, "zfs list -Hp -d 0 -o name,used,logicalused")
}

func (s Ssh) PoolProps(ctx context.Context) (string, error) {
	return s.run(ctx, "zpool get -Hp ashift")
}

func (s Ssh) DestroyDryRun(ctx context.Context, target string) (string, error) {
	// -n is pinned in this string; this never destroys anything. -p makes
	// the reclaim figure machine-readable for the Σ math.
	return s.runCombined(ctx, "zfs destroy -n -v -p "+quote(target))
}

func (s Ssh) PerfTexts(ctx context.Context, pool string) (string, string, string, string, string, error) {
	txgs, err := s.run(ctx, "cat "+quote("/proc/spl/kstat/zfs/"+pool+"/txgs"))
	if err != nil && transportDown(err) {
		return "", "", "", "", "", err
	}
	dmuTx, _ := s.run(ctx, "cat /proc/spl/kstat/zfs/dmu_tx")
	// newer kmods have per-pool zil kstats; fall back to the global one
	zil, _ := s.run(ctx, "cat "+quote("/proc/spl/kstat/zfs/"+pool+"/zil")+" 2>/dev/null || cat /proc/spl/kstat/zfs/zil")
	params, _ := s.run(ctx, "grep -H . /sys/module/zfs/parameters/zfs_dirty_data_max /sys/module/zfs/parameters/zfs_delay_min_dirty_percent /sys/module/zfs/parameters/zfs_dirty_data_sync_percent")
	iostat, err := s.run(ctx, "zpool iostat -Hpvly "+quote(pool)+" 1 1")
	return txgs, dmuTx, zil, params, iostat, err
}

func (s Ssh) HostTexts(ctx context.Context) (string, string, string, string) {
	up, err := s.run(ctx, "cat /proc/uptime")
	if err != nil && transportDown(err) {
		return "", "", "", ""
	}
	load, _ := s.run(ctx, "cat /proc/loadavg")
	stat, _ := s.run(ctx, "cat /proc/stat")
	hwmon, _ := s.run(ctx, "grep -H . /sys/class/hwmon/hwmon*/name /sys/class/hwmon/hwmon*/temp*_input /sys/class/hwmon/hwmon*/temp*_label 2>/dev/null")
	return up, load, stat, hwmon
}

func (s Ssh) DiskTexts(ctx context.Context) (string, string, string, string) {
	aliases, err := s.run(ctx, `find /dev/disk -type l -printf "%p %l\n" 2>/dev/null`)
	if err != nil && transportDown(err) {
		return "", "", "", ""
	}
	sysBlock, _ := s.run(ctx, `find /sys/class/block -maxdepth 1 -type l -printf "%f %l\n" 2>/dev/null`)
	lsblk, _ := s.run(ctx, "lsblk -bdP -o NAME,SIZE,ROTA,MODEL,SERIAL 2>/dev/null")
	hwmonDev, _ := s.run(ctx, `for h in /sys/class/hwmon/hwmon*; do [ -e "$h/device" ] && echo "$h $(readlink -f "$h/device")"; done; true`)
	return aliases, sysBlock, lsblk, hwmonDev
}

func (s Ssh) SmartTexts(ctx context.Context, nodes []string) (map[string]string, bool) {
	if _, err := s.run(ctx, "sudo -n true"); err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, n := range nodes {
		// keep stdout even on nonzero exits — smartctl flags logged errors
		// in its exit code while the JSON is complete
		text, _ := s.run(ctx, "sudo -n smartctl -j -a -n standby /dev/"+quote(n))
		if len(text) > 0 {
			out[n] = text
		}
	}
	return out, true
}

func (s Ssh) InfoTexts(ctx context.Context) (string, string, string) {
	ver, err := s.run(ctx, "zfs version")
	if err != nil && transportDown(err) {
		return "", "", ""
	}
	kernel, _ := s.run(ctx, "cat /proc/sys/kernel/osrelease")
	osrel, _ := s.run(ctx, "cat /etc/os-release")
	return ver, strings.TrimSpace(kernel), osrel
}
