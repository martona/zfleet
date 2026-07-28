#!/bin/sh
# capture.sh — record every text surface zfse parses into a dated fixture
# dir (default ~/claude/fixtures/<shorthost>/<date>). Runs ON the target
# host. Read-only apart from the fixture files themselves; the dir layout
# matches what collect.Replay expects.
set -u
base="${1:-$HOME/claude/fixtures}"
d="$base/$(hostname -s)/$(date +%F)"
mkdir -p "$d"
m="$d/manifest.txt"
: >"$m"

run() { # run <outfile> <cmd...>
	out="$1"
	shift
	"$@" >"$d/$out" 2>>"$m"
	echo "exit=$? $out <= $*" >>"$m"
}
sh_run() { # sh_run <outfile> <shell-line> — for globs and pipelines
	out="$1"
	shift
	sh -c "$*" >"$d/$out" 2>>"$m"
	echo "exit=$? $out <= sh -c: $*" >>"$m"
}

# keep in sync with zfs.DatasetFields / zfs.SnapshotFields
DSFIELDS="name,type,used,avail,refer,usedbysnapshots,usedbydataset,usedbychildren,usedbyrefreservation,mountpoint,mounted,canmount,quota,refquota,reservation,refreservation,recordsize,volsize,volblocksize,compression,compressratio,origin,creation,atime,sync,encryption,keystatus,logicalused,logicalreferenced"
SNAPFIELDS="name,used,refer,creation,written"

run zpool-status.out zpool status
run zpool-list-Hpv.out zpool list -Hpv
run zpool-ashift.out zpool get -Hp ashift,bcloneused,bclonesaved
run zpool-iostat-Hpv.out zpool iostat -Hpy 1 1
run arcstats.out cat /proc/spl/kstat/zfs/arcstats
run zfs-list-wide.out zfs list -Hp -r -t filesystem,volume -o "$DSFIELDS"
run zfs-list-snapshots.out zfs list -Hp -t snapshot -s creation -o "$SNAPFIELDS"
run dmu-tx.out cat /proc/spl/kstat/zfs/dmu_tx
sh_run zil.out 'cat /proc/spl/kstat/zfs/zil 2>/dev/null || true'
sh_run objset-all.out 'cat /proc/spl/kstat/zfs/*/objset-* 2>/dev/null'
sh_run zfs-params.out 'grep -H . /sys/module/zfs/parameters/zfs_dirty_data_max /sys/module/zfs/parameters/zfs_delay_min_dirty_percent /sys/module/zfs/parameters/zfs_dirty_data_sync_percent'

first=""
for p in $(zpool list -H -o name); do
	[ -z "$first" ] && first="$p"
	run "txgs-$p.out" cat "/proc/spl/kstat/zfs/$p/txgs"
done
[ -n "$first" ] && run zpool-iostat-Hpvly.out zpool iostat -Hpvly "$first" 1 1

# property samples: every pool root (one file — Replay concatenates and
# parsers filter by name), one mounted fs, one volume
sh_run zfs-get-all-rust.out 'for p in $(zpool list -H -o name); do zfs get -Hp all "$p"; done'
fs="$(zfs list -H -t filesystem -o name,mounted | awk '$2 == "yes" { print $1; exit }')"
[ -n "$fs" ] && run zfs-get-all-fs.out zfs get -Hp all "$fs"
vol="$(zfs list -H -t volume -o name | head -1)"
[ -n "$vol" ] && run zfs-get-all-zvol.out zfs get -Hp all "$vol"

# disk surfaces (drive-health round): the alias universe, the kernel
# block-device map, the disk inventory, and hwmon chip→device links
sh_run disk-aliases.out 'find /dev/disk -type l -printf "%p %l\n" 2>/dev/null'
sh_run sys-block.out 'find /sys/class/block -maxdepth 1 -type l -printf "%f %l\n" 2>/dev/null'
sh_run lsblk-disks.out 'lsblk -bdP -o NAME,SIZE,ROTA,MODEL,SERIAL 2>/dev/null'
sh_run hwmon-dev.out 'for h in /sys/class/hwmon/hwmon*; do [ -e "$h/device" ] && echo "$h $(readlink -f "$h/device")"; done; true'

# smart surfaces (phase 2): passwordless sudo only; -n standby politely
# skips sleeping drives rather than waking them for a checkup
if sudo -n true 2>/dev/null; then
	echo ok >"$d/sudo-ok"
	for disk in $(lsblk -bdno NAME 2>/dev/null | grep -Ev "^(loop|zd|sr|nbd|dm-)"); do
		sudo -n smartctl -j -x -n standby "/dev/$disk" >"$d/smart-$disk.json" 2>>"$m"
		echo "exit=$? smart-$disk.json <= smartctl -j -x -n standby /dev/$disk" >>"$m"
	done
fi

# host surfaces (multi-host round): vitals + identity
run uptime.out cat /proc/uptime
run loadavg.out cat /proc/loadavg
run proc-stat.out cat /proc/stat
run kernel.out cat /proc/sys/kernel/osrelease
run os-release.out cat /etc/os-release
run zfs-version.out zfs version
sh_run hwmon.out 'grep -H . /sys/class/hwmon/hwmon*/name /sys/class/hwmon/hwmon*/temp*_input /sys/class/hwmon/hwmon*/temp*_label /sys/class/hwmon/hwmon*/temp*_max /sys/class/hwmon/hwmon*/temp*_crit 2>/dev/null'

echo "$d"
