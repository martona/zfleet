package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martona/zfs-explorer/internal/collect"
	"github.com/martona/zfs-explorer/internal/ui"
	"github.com/martona/zfs-explorer/internal/zfs"
)

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	var replays, hostFlags multiFlag
	flag.Var(&replays, "replay", "fixture `dir` (or name=dir, repeatable) instead of live collection")
	flag.Var(&hostFlags, "host", "ssh `destination` of a remote host (repeatable; adds to the hosts file)")
	noDedupe := flag.Bool("no-dedupe", false, "keep entries matching the local hostname as ssh hosts (testing)")
	dump := flag.Bool("dump", false, "render one frame to stdout and exit (no TTY needed)")
	width := flag.Int("width", 110, "frame width for --dump")
	height := flag.Int("height", 30, "frame height for --dump")
	sel := flag.String("select", "", "row to select for --dump: overview, [host:]pool, [host:]dataset, or a host name")
	snapsFlag := flag.String("snaps", "", "comma-separated [host:]datasets whose snapshots to unfold into the tree for --dump (the t toggle); ds@label also unfolds that family")
	cursor := flag.String("cursor", "", "visible row to move the --dump cursor to: @snap, @family label, dataset, pool, or host")
	expand := flag.String("expand", "", "comma-separated [host:]pools/datasets to unfold for --dump tree views")
	var markFlags multiFlag
	flag.Var(&markFlags, "mark", "selection to mark for --dump: [host:]ds@s1,s2 or a whole [host:]ds (repeatable)")
	filterFlag := flag.String("filter", "", "filter pattern for --dump: ds[@snap], substring or glob; sweeps the fleet like the live / key")
	vdrives := flag.Bool("vdrives", false, "show every drive's check ledger in --dump (the live v toggle)")
	ackFile := flag.String("ack-file", "", "acknowledgement ledger `path` (default ~/.config/zfse/ack.conf)")
	ackPopup := flag.Bool("ack-popup", false, "open the acknowledge popup in --dump")
	flag.Parse()

	specs, multi := resolveHosts(replays, hostFlags, *noDedupe)
	m := ui.New(specs, multi)
	m.SetAckFile(*ackFile)

	// hostFor resolves an optional "host:" prefix on dump arguments,
	// defaulting to the first host. Pool and dataset names cannot contain
	// a colon, so the split is unambiguous; a bare host name means the
	// host itself.
	hostFor := func(arg string) (string, string) {
		for _, s := range specs {
			if s.Name == arg {
				return s.Name, ""
			}
		}
		if i := strings.IndexByte(arg, ':'); i > 0 {
			name := arg[:i]
			for _, s := range specs {
				if s.Name == name {
					return name, arg[i+1:]
				}
			}
		}
		return specs[0].Name, arg
	}
	srcOf := func(host string) collect.Source {
		for _, s := range specs {
			if s.Name == host {
				return s.Src
			}
		}
		return specs[0].Src
	}

	if *dump {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		poolsOf := map[string][]string{}
		for _, s := range specs {
			status, list, err := s.Src.PoolTexts(ctx)
			if err != nil {
				if !multi {
					fail("collect pools: " + err.Error())
				}
				continue // a dead fleet member still renders as a row
			}
			pools := zfs.ParseZpoolStatus(status)
			zfs.AttachListNumbers(pools, list)
			for _, p := range pools {
				poolsOf[s.Name] = append(poolsOf[s.Name], p.Name)
			}
			m.ApplyPoolData(s.Name, pools)
			roots, _ := s.Src.RootTexts(ctx)
			poolProps, _ := s.Src.PoolProps(ctx)
			m.ApplyAuxPools(s.Name, roots, poolProps)
			if arc, objsets, err := s.Src.StatTexts(ctx); err == nil {
				m.ApplyStatData(s.Name, arc, objsets)
			}
			for _, b := range iostatBlocks(s.Src, 1, 6*time.Second) {
				m.ApplyIostat(s.Name, b)
			}
			al, sysBlock, lsblk, hwmonDev := s.Src.DiskTexts(ctx)
			m.ApplyDisks(s.Name, al, sysBlock, lsblk, hwmonDev)
			var nodes []string
			for _, d := range zfs.ParseLsblkDisks(lsblk) {
				nodes = append(nodes, d.Node)
			}
			smart, sudoOK := s.Src.SmartTexts(ctx, nodes)
			m.ApplySmart(s.Name, smart, sudoOK)
			up, load, stat, hwmon := s.Src.HostTexts(ctx)
			m.ApplyHostVitals(s.Name, up, load, stat, hwmon)
			ver, kernel, osrel := s.Src.InfoTexts(ctx)
			m.ApplyHostInfo(s.Name, ver, kernel, osrel)
			m.MarkHostLive(s.Name)
		}
		if !strings.HasPrefix(specs[0].Src.Name(), "replay") {
			// second sample so rate-based readouts are real, not blank
			time.Sleep(1200 * time.Millisecond)
			for _, s := range specs {
				if arc, objsets, err := s.Src.StatTexts(ctx); err == nil {
					m.ApplyStatData(s.Name, arc, objsets)
					up, load, stat, hwmon := s.Src.HostTexts(ctx)
					m.ApplyHostVitals(s.Name, up, load, stat, hwmon)
				}
			}
		}
		if *expand != "" {
			fetched := map[string]bool{}
			for _, item := range strings.Split(*expand, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				host, path := hostFor(item)
				pool := strings.SplitN(path, "/", 2)[0]
				key := host + "\x00" + pool
				if !fetched[key] {
					if dsText, err := srcOf(host).DatasetTexts(ctx, pool); err == nil {
						m.ApplyDatasets(host, pool, dsText)
					}
					fetched[key] = true
				}
				m.ExpandFor(host, path)
			}
		}
		if *snapsFlag != "" {
			for _, item := range strings.Split(*snapsFlag, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				host, path := hostFor(item)
				ds := strings.SplitN(path, "@", 2)[0]
				src := srcOf(host)
				pool := strings.SplitN(ds, "/", 2)[0]
				dsText, err := src.DatasetTexts(ctx, pool)
				if err != nil {
					fail("collect datasets: " + err.Error())
				}
				m.ApplyDatasets(host, pool, dsText)
				if !m.ShowSnaps(host, path) {
					fail("host not found: " + item)
				}
				if snapText, err := src.SnapshotTexts(ctx, ds); err == nil {
					m.ApplySnaps(host, ds, snapText)
				}
				if propText, err := src.PropTexts(ctx, ds); err == nil {
					m.ApplyProps(host, ds, propText)
				}
			}
		}
		for _, mk := range markFlags {
			host, path := hostFor(mk)
			src := srcOf(host)
			ds := strings.SplitN(path, "@", 2)[0]
			pool := strings.SplitN(ds, "/", 2)[0]
			if dsText, err := src.DatasetTexts(ctx, pool); err == nil {
				m.ApplyDatasets(host, pool, dsText)
			}
			if i := strings.IndexByte(path, '@'); i >= 0 {
				if snapText, err := src.SnapshotTexts(ctx, ds); err == nil {
					m.ApplySnaps(host, ds, snapText)
				}
				m.MarkSnaps(host, ds, strings.Split(path[i+1:], ","))
			} else {
				m.MarkDataset(host, ds)
			}
		}
		for _, ht := range m.MarkTargets() {
			text, err := srcOf(ht[0]).DestroyDryRun(ctx, ht[1])
			m.ApplyDryRun(ht[0], ht[1], text, err)
		}
		if *filterFlag != "" {
			// the live sweep, run synchronously: every pool's dataset tree,
			// plus pool-recursive snapshots when the pattern hunts them
			for _, s := range specs {
				for _, pool := range poolsOf[s.Name] {
					if dsText, err := s.Src.DatasetTexts(ctx, pool); err == nil {
						m.ApplyDatasets(s.Name, pool, dsText)
					}
					if strings.Contains(*filterFlag, "@") {
						if txt, err := s.Src.PoolSnapshotTexts(ctx, pool); err == nil {
							m.ApplySweep(s.Name, pool, txt)
						}
					}
				}
			}
			m.SetFilter(*filterFlag)
		}
		if *sel != "" {
			host, name := hostFor(*sel)
			m.SetSelected(host, name)
			if strings.Contains(name, "/") {
				if snapText, err := srcOf(host).SnapshotTexts(ctx, name); err == nil {
					m.ApplySnaps(host, name, snapText)
				}
				if propText, err := srcOf(host).PropTexts(ctx, name); err == nil {
					m.ApplyProps(host, name, propText)
				}
			}
		}
		if *cursor != "" {
			if !m.SetCursorRow(*cursor) {
				fail("row not visible: " + *cursor)
			}
			if host, target := m.SelectedFamTarget(); target != "" {
				text, err := srcOf(host).DestroyDryRun(ctx, target)
				m.ApplyDryRun(host, target, text, err)
			}
		}
		// a selected pool row renders the live-engine blocks — feed them
		// the same double-sampled perf surfaces the 2s tick would deliver
		// (vdev latency already arrived via the per-host iostat block)
		if host, pool, ok := m.SelectedPoolTarget(); ok {
			src := srcOf(host)
			txgs, dmuTx, zil, params, err := src.PerfTexts(ctx, pool)
			m.ApplyPerf(host, pool, txgs, dmuTx, zil, params, err)
			if !strings.HasPrefix(src.Name(), "replay") {
				time.Sleep(1200 * time.Millisecond)
				txgs, dmuTx, zil, params, err = src.PerfTexts(ctx, pool)
				m.ApplyPerf(host, pool, txgs, dmuTx, zil, params, err)
			}
		}
		m.SetVerboseDrives(*vdrives)
		if *ackPopup {
			m.OpenAckPopup()
		}
		m.SetSize(*width, *height)
		fmt.Println(m.View())
		return
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fail(err.Error())
	}
}

// resolveHosts turns flags and the hosts file into the fleet: which hosts,
// in what order, served by which collector. The rules, as designed:
//   - no remotes registered → exactly the single-host tool of old
//   - any remote → host rows appear; the local host (when it has zfs)
//     joins the fleet
//   - a registered name whose first label matches the local hostname is
//     served locally instead of over ssh (dedupe — the hosts file is
//     yadm-shared and must not grow ghosts), unless --no-dedupe
func resolveHosts(replays, hostFlags multiFlag, noDedupe bool) ([]ui.HostSpec, bool) {
	var bare []string
	var named []ui.HostSpec
	for _, r := range replays {
		if i := strings.IndexByte(r, '='); i > 0 {
			named = append(named, ui.HostSpec{Name: r[:i], Dest: "replay",
				Src: collect.Replay{Dir: r[i+1:]}})
		} else {
			bare = append(bare, r)
		}
	}
	// a bare --replay dir is the classic single-host fixture mode, exclusive;
	// name=dir hosts are virtual fleet members and may ride along with live
	// ones — puppets whose every surface is a text file, re-read each tick
	if len(bare) > 0 {
		if len(bare) > 1 || len(named) > 0 || len(hostFlags) > 0 {
			fail("--replay: a bare dir is single-host; use name=dir entries to build or join a fleet")
		}
		return []ui.HostSpec{{Name: "replay", Src: collect.Replay{Dir: bare[0]}}}, false
	}

	entries := append(readHostsFile(), hostFlags...)
	localOK := runtime.GOOS == "linux" && haveZpool()
	localName := shortHostname()

	if len(entries) == 0 && len(named) > 0 && !localOK {
		// pure virtual fleet — fine anywhere
		return uniquifyNames(named), true
	}
	if len(entries) == 0 && len(named) == 0 {
		if runtime.GOOS != "linux" {
			fail("live mode needs a Linux host with ZFS; use --replay <fixture-dir> or register remote hosts")
		}
		if !haveZpool() {
			fail("zpool not found in PATH; use --replay <fixture-dir>, register remote hosts, or run on a ZFS host")
		}
		name := localName
		if name == "" {
			name = "local"
		}
		return []ui.HostSpec{{Name: name, Src: collect.Exec{}}}, false
	}

	var specs []ui.HostSpec
	localUsed := false
	for _, e := range entries {
		label := firstLabel(e)
		if !noDedupe && localOK && !localUsed && localName != "" && strings.EqualFold(label, localName) {
			specs = append(specs, ui.HostSpec{Name: label, Src: collect.Exec{}})
			localUsed = true
			continue
		}
		specs = append(specs, ui.HostSpec{Name: label, Dest: e, Src: collect.Ssh{Dest: e, Label: label}})
	}
	if !localUsed && localOK {
		name := localName
		if name == "" {
			name = "local"
		}
		specs = append([]ui.HostSpec{{Name: name, Src: collect.Exec{}}}, specs...)
	}
	specs = append(specs, named...)
	if len(specs) == 0 {
		fail("no usable hosts: local has no zfs and no remotes resolved")
	}
	return uniquifyNames(specs), true
}

// readHostsFile loads ~/.config/zfse/hosts: one ssh destination per line,
// #-comments, blank lines ignored. Absent file = no entries.
func readHostsFile() []string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, "zfse", "hosts"))
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// firstLabel reduces an ssh destination to its display name: strip user@,
// keep the first hostname label.
func firstLabel(dest string) string {
	host := dest
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		host = host[i+1:]
	}
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return host
}

func shortHostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return firstLabel(name)
}

func haveZpool() bool {
	_, err := exec.LookPath("zpool")
	return err == nil
}

// uniquifyNames disambiguates colliding display names — deliberate with
// --no-dedupe self-registration, accidental otherwise; either way the tree
// needs distinct ids.
func uniquifyNames(specs []ui.HostSpec) []ui.HostSpec {
	seen := map[string]int{}
	for i := range specs {
		seen[specs[i].Name]++
		if n := seen[specs[i].Name]; n > 1 {
			specs[i].Name = fmt.Sprintf("%s(%d)", specs[i].Name, n)
		}
	}
	return specs
}

// iostatBlocks reads up to n sample blocks from a source's iostat stream
// for the one-shot dump path, giving up at the deadline. Replay streams EOF
// after their fixture block; a live stream's first kernel-timed sample
// lands in ~2s.
func iostatBlocks(src collect.Source, n int, wait time.Duration) []string {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	rc, err := src.IostatStream(ctx)
	if err != nil {
		return nil
	}
	defer rc.Close()
	var out []string
	collect.ScanIostatBlocks(rc, func(block string) bool {
		out = append(out, block)
		return len(out) < n
	})
	return out
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "zfse: "+msg)
	os.Exit(1)
}
