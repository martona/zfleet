package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/martona/zfleet/internal/zfs"
)

// ANSI palette colors only, so the user's terminal theme decides the actual
// look — with one deliberate exception: sparkline riser ink is 256-color,
// because the 16-palette offers nothing between shout and mute. Old
// terminals degrade to the nearest ANSI color.
// WARN is BRIGHT yellow (11), not plain (3): many palettes render 3 as an
// olive gold whose warn-ness lives entirely in its red component — for
// protan vision that collapses into green. Bright yellow differs from
// green in luminance, which survives every kind of color vision.
var (
	styGood    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styWarnInv = lipgloss.NewStyle().Reverse(true).Foreground(lipgloss.Color("11")) // the scream band
	styBad     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	styDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styBold    = lipgloss.NewStyle().Bold(true)
	styInv     = lipgloss.NewStyle().Reverse(true) // the mc cursor bar
	styBar     = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styBarWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styBarBad  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	// the snapshot sigil: bright violet, unclaimed by the rest of the
	// palette (steel=reads, gold=writes, 11=WARN) and distinct under
	// protan vision; bold carries the mark even where color can't. A
	// direct 256-color index (#d787ff), NOT ANSI 13 — user palettes render
	// 13 anywhere from neon to mud, and this mark must not be mud
	stySnapAt = lipgloss.NewStyle().Foreground(lipgloss.Color("177")).Bold(true)
)

// sparkFam is riser ink by intensity — trickle, mid, burst — so big moves
// glow hotter (btop's gradient, quantized to our three riser levels).
// Reads wear steel, writes wear gold; the dead family renders frozen
// history on unreachable hosts. Baseline dots are always muted.
type sparkFam [3]lipgloss.Style

var (
	sparkGold = sparkFam{
		lipgloss.NewStyle().Foreground(lipgloss.Color("136")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("178")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	}
	sparkSteel = sparkFam{
		lipgloss.NewStyle().Foreground(lipgloss.Color("67")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("110")),
		lipgloss.NewStyle().Foreground(lipgloss.Color("153")),
	}
	sparkDead = sparkFam{styDim, styDim, styDim}
)

// Severity: two formal alarm tiers — WARN is yellow, ERROR is red — and
// they escalate through the chain: counters warn a pool, a pool's tier
// colors its name, the worst pool colors its host, and an unreachable
// host is an ERROR outright. No state is neutral: anything not ONLINE is
// at least a WARN.
const (
	sevOK = iota
	sevWarn
	sevErr
)

func stateSev(state string) int {
	switch state {
	case "ONLINE", "AVAIL": // AVAIL: an idle hot spare is fine, not news
		return sevOK
	case "FAULTED", "UNAVAIL", "SUSPENDED", "REMOVED":
		return sevErr
	default: // DEGRADED, OFFLINE, INUSE, and whatever zfs invents next
		return sevWarn
	}
}

func sevStyle(sev int) lipgloss.Style {
	switch sev {
	case sevErr:
		return styBad
	case sevWarn:
		return styWarn
	}
	return styGood
}

func healthStyle(state string) lipgloss.Style { return sevStyle(stateSev(state)) }

// poolSev is a pool's alarm tier: its state's tier, floored at WARN when
// any error counter anywhere in its topology is nonzero.
func poolSev(p *zfs.Pool) int {
	s := stateSev(p.State)
	if s == sevOK {
		if r, w, c := p.ErrSums(); r+w+c > 0 {
			s = sevWarn
		}
	}
	return s
}

// bar renders a capacity bar; the fill colors by fullness thresholds
// (>=90 red, >=80 yellow) because that is when ZFS starts to hurt.
func bar(pct int64, width int) string {
	if pct < 0 {
		return strings.Repeat("·", width)
	}
	if pct > 100 {
		pct = 100
	}
	fill := int((pct*int64(width) + 50) / 100)
	sty := styBar
	switch {
	case pct >= 90:
		sty = styBarBad
	case pct >= 80:
		sty = styBarWarn
	}
	return sty.Render(strings.Repeat("▓", fill)) + styDim.Render(strings.Repeat("░", width-fill))
}

// elastic right-aligns a readout in a grow-but-not-shrink field: the field
// remembers the widest value it has shown (per key) so live numbers flapping
// between "0B/s" and "1.32M/s" don't shove the rest of the line around.
func elastic(widths map[string]int, key, s string) string {
	if n := lipgloss.Width(s); n > widths[key] {
		widths[key] = n
	}
	return padL(s, widths[key])
}

// Sparklines are btop-style braille: each cell is a 2×4 dot matrix, so one
// character carries two samples at four levels — double the density of
// block glyphs at a fraction of the ink. An idle sample keeps a single
// muted baseline dot (calm, not absence — absence stays blank), and any
// nonzero sample rises at least one dot above the floor. History shorter
// than the window left-pads with blanks so the newest sample stays pinned
// at the right edge.
var (
	brailleL = [5]rune{0, 0x40, 0x44, 0x46, 0x47} // left column, bottom-up fill
	brailleR = [5]rune{0, 0x80, 0xA0, 0xB0, 0xB8} // right column
)

func sparklineFam(fam sparkFam, vals []int64, width int) string {
	if n := 2 * width; len(vals) > n {
		vals = vals[len(vals)-n:]
	}
	var max int64
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	lvl := func(v int64) int {
		if v <= 0 {
			return 1
		}
		l := 2 + int(v*3/max)
		if l > 4 {
			l = 4
		}
		return l
	}
	// a cell's ink follows its hotter sample; -1 = baseline-only, muted
	shade := func(l, r int) int {
		if r > l {
			l = r
		}
		return l - 2
	}
	var out strings.Builder
	out.WriteString(rep(" ", width-(len(vals)+1)/2))
	var run strings.Builder
	runShade := -2
	flush := func() {
		if run.Len() == 0 {
			return
		}
		if runShade < 0 {
			out.WriteString(styDim.Render(run.String()))
		} else {
			out.WriteString(fam[runShade].Render(run.String()))
		}
		run.Reset()
	}
	cell := func(bits rune, sh int) {
		if sh != runShade {
			flush()
			runShade = sh
		}
		run.WriteRune(0x2800 | bits)
	}
	i := 0
	if len(vals)%2 == 1 {
		// odd history: the oldest cell carries one sample, right column,
		// so the newest stays glued to the right edge
		l := lvl(vals[0])
		cell(brailleR[l], shade(0, l))
		i = 1
	}
	for ; i < len(vals); i += 2 {
		l, r := lvl(vals[i]), lvl(vals[i+1])
		cell(brailleL[l]|brailleR[r], shade(l, r))
	}
	flush()
	return out.String()
}

// sparklineTall stacks `rows` braille lines into one taller chart, scaled to
// an explicit ceiling instead of the window max. Callers pass a remembered
// high-water mark: a self-scaled ring deflates as its biggest samples age
// out, so a pool that once moved 1G/s would read as slammed at 100K/s.
// Zero keeps the muted baseline dot; any nonzero sample rises at least one
// dot. Returns the rows top-first.
func sparklineTall(fam sparkFam, vals []int64, width, rows int, ceil int64) []string {
	if n := 2 * width; len(vals) > n {
		vals = vals[len(vals)-n:]
	}
	for _, v := range vals {
		if v > ceil {
			ceil = v
		}
	}
	total := 4 * rows
	lvl := func(v int64) int {
		if v <= 0 {
			return 1
		}
		l := 2 + int(v*int64(total-2)/ceil)
		if l > total {
			l = total
		}
		return l
	}
	clamp4 := func(x int) rune {
		if x < 0 {
			x = 0
		}
		if x > 4 {
			x = 4
		}
		return rune(x)
	}
	pad := rep(" ", width-(len(vals)+1)/2)
	line := make([]strings.Builder, rows)
	run := make([]strings.Builder, rows)
	runShade := make([]int, rows)
	for r := range line {
		line[r].WriteString(pad)
		runShade[r] = -2
	}
	flush := func(r int) {
		if run[r].Len() == 0 {
			return
		}
		if runShade[r] < 0 {
			line[r].WriteString(styDim.Render(run[r].String()))
		} else {
			line[r].WriteString(fam[runShade[r]].Render(run[r].String()))
		}
		run[r].Reset()
	}
	// column ink intensity follows the hotter sample's overall level, and
	// the whole column wears it so a burst reads as one object
	cell := func(lL, lR int) {
		hot := lL
		if lR > hot {
			hot = lR
		}
		sh := -1
		if hot > 1 {
			sh = (hot - 2) * 3 / (total - 1)
		}
		for r := 0; r < rows; r++ {
			floor := 4 * (rows - 1 - r)
			bits := brailleL[clamp4(lL-floor)] | brailleR[clamp4(lR-floor)]
			rsh := sh
			if bits == 0 {
				rsh = -1 // blank air above the column: cheap muted run
			}
			if rsh != runShade[r] {
				flush(r)
				runShade[r] = rsh
			}
			run[r].WriteRune(0x2800 | bits)
		}
	}
	i := 0
	if len(vals)%2 == 1 {
		cell(0, lvl(vals[0]))
		i = 1
	}
	for ; i < len(vals); i += 2 {
		cell(lvl(vals[i]), lvl(vals[i+1]))
	}
	out := make([]string, rows)
	for r := range out {
		flush(r)
		out[r] = line[r].String()
	}
	return out
}

// stripSGR removes ANSI SGR sequences so a fully composed line can be
// re-styled wholesale — the idle perf screen dims its entire body this way
// (silence should look like silence).
func stripSGR(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j
				continue
			}
		}
		out.WriteByte(s[i])
	}
	return out.String()
}

// errBadge compresses nonzero error counters into a row-width verdict:
// "R 12 C 3", tightening to "R12C3" and finally "errors" as space runs out.
func errBadge(r, w, c int64, maxW int) string {
	var parts []string
	if r > 0 {
		parts = append(parts, "R "+zfs.NiceCount(r))
	}
	if w > 0 {
		parts = append(parts, "W "+zfs.NiceCount(w))
	}
	if c > 0 {
		parts = append(parts, "C "+zfs.NiceCount(c))
	}
	if len(parts) == 0 {
		return ""
	}
	s := strings.Join(parts, " ")
	if len(s) <= maxW {
		return s
	}
	if s = strings.ReplaceAll(s, " ", ""); len(s) <= maxW {
		return s
	}
	return truncate("errors", maxW)
}

// dimLabels brightens digit runs and mutes everything else — words, units,
// separators — so a phrase like "3d 22h" reads by its numbers.
func dimLabels(s string) string {
	isNum := func(b byte) bool { return b >= '0' && b <= '9' || b == '.' }
	var out strings.Builder
	for i := 0; i < len(s); {
		j := i
		for j < len(s) && isNum(s[j]) == isNum(s[i]) {
			j++
		}
		if isNum(s[i]) {
			out.WriteString(s[i:j])
		} else {
			out.WriteString(styDim.Render(s[i:j]))
		}
		i = j
	}
	return out.String()
}

// tempTint brightens a temperature readout that exceeds its DEVICE-stated
// thresholds — warn yellow at the operating limit, bad red at critical.
// No stated thresholds, no tint: readings are never judged against
// invented limits. Display-tier only — nothing here enters the severity
// chain (temps oscillate; they are conditions, not facts to ack).
func tempTint(c, high, crit int, cell string) string {
	switch {
	case crit > 0 && c >= crit:
		return styBad.Render(cell)
	case high > 0 && c >= high:
		return styWarn.Render(cell)
	}
	return dimUnit(cell)
}

// dimUnit dims the trailing unit of a readout ("1.88G", "43%") so the
// digits carry the line and adjacent numeric columns separate visually.
// Safe on right-padded input — the suffix scan stops at the digits.
func dimUnit(s string) string {
	i := len(s)
	for i > 0 && (s[i-1] < '0' || s[i-1] > '9') {
		i--
	}
	if i == len(s) {
		return s
	}
	return s[:i] + styDim.Render(s[i:])
}

// padR pads or truncates a *plain* (unstyled) string to exactly w cells.
func padR(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d < 0 {
		return truncate(s, w)
	}
	return s + strings.Repeat(" ", d)
}

// padL right-aligns a plain string in w cells.
func padL(s string, w int) string {
	d := w - lipgloss.Width(s)
	if d < 0 {
		return truncate(s, w)
	}
	return strings.Repeat(" ", d) + s
}

// wrap word-wraps a plain string into lines of at most w cells.
func wrap(s string, w int) []string {
	if w < 8 {
		return []string{truncate(s, w)}
	}
	var out []string
	cur := ""
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case lipgloss.Width(cur)+1+lipgloss.Width(word) <= w:
			cur += " " + word
		default:
			out = append(out, cur)
			cur = word
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// commonPrefixLen returns the length of the longest shared prefix — used to
// strip the model/serial boilerplate sibling disks share so their rows show
// the part that differs.
func commonPrefixLen(names []string) int {
	if len(names) == 0 {
		return 0
	}
	p := names[0]
	for _, n := range names[1:] {
		limit := len(p)
		if len(n) < limit {
			limit = len(n)
		}
		i := 0
		for i < limit && p[i] == n[i] {
			i++
		}
		p = p[:i]
	}
	return len(p)
}

// truncate shortens a plain string to w cells, keeping head and tail with a
// middle ellipsis — device names carry their identity at both ends. Total
// over all widths: deep tree indents in a narrow pane produce negative
// budgets, and a renderer must degrade to nothing, never panic.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return string(r[:1])
	}
	head := (w - 1) / 2
	tail := w - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
