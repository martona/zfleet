package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ANSI palette colors only, so the user's terminal theme decides the actual
// look.
var (
	styGood    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styBad     = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	styDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styBold    = lipgloss.NewStyle().Bold(true)
	styBar     = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styBarWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styBarBad  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
)

func healthStyle(state string) lipgloss.Style {
	switch state {
	case "ONLINE":
		return styGood
	case "DEGRADED":
		return styWarn
	case "FAULTED", "UNAVAIL", "SUSPENDED":
		return styBad
	default:
		return styDim
	}
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

// truncate shortens a plain string to w cells, keeping head and tail with a
// middle ellipsis — device names carry their identity at both ends.
func truncate(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w <= 1 {
		return string(r[:w])
	}
	head := (w - 1) / 2
	tail := w - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}
