// Package prompt talks to the person running the wizard. There are two
// implementations of UI: a terminal UI (arrow key menus, spinners, colors,
// styled after Claude Code) and a plain line based one used for tests and
// scripted input. Both validate answers right away and show the result with a
// check mark or a cross.
package prompt

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// ErrAborted is returned when the user quits (Ctrl+C) or the input ends.
var ErrAborted = errors.New("aborted")

// ErrBack is returned when the user presses Esc (or answers "<" in the plain
// UI): leave this question and go back one step, changing nothing.
var ErrBack = errors.New("back")

// backToken is the answer that means Esc in the plain UI.
const backToken = "<"

// Check validates an answer. On success it may return a short message that
// is shown next to a green check mark, for example "found 3 specs".
type Check func(answer string) (string, error)

// UI asks questions and prints results.
type UI interface {
	// Ask asks for a string. An empty answer returns def.
	Ask(label, def string) (string, error)
	// AskValid asks until check accepts the answer. check may be slow (it
	// runs behind a spinner) and may be nil.
	AskValid(label, def string, check Check) (string, error)
	Confirm(label string, def bool) (bool, error)
	// Choose returns the index picked. def is preselected, -1 for none.
	Choose(label string, options []string, def int) (int, error)
	// List asks for a comma separated list.
	List(label string, def []string) ([]string, error)
	// Task runs fn behind a spinner and reports its outcome.
	Task(label string, fn func() (string, error)) (string, error)

	Banner(title string, lines ...string)
	Section(title string)
	OK(format string, a ...any)
	Warn(format string, a ...any)
	Fail(format string, a ...any)
	Info(format string, a ...any)
	Item(format string, a ...any) // indented detail under the previous line
	Field(key, value string)      // one "key   value" line of a summary
	// Badge colors a short status word for use inside labels and menu
	// options. Put it at the end of the text.
	Badge(kind Kind, text string) string
}

// Kind picks the color of a badge.
type Kind int

const (
	KindOK Kind = iota
	KindWarn
	KindErr
	KindDim
)

// Claude Code's palette.
var (
	colAccent = lipgloss.Color("#D77757")
	colOK     = lipgloss.Color("#4EBA65")
	colErr    = lipgloss.Color("#FF6B80")
	colWarn   = lipgloss.Color("#FFC107")
	colSelect = lipgloss.Color("#B1B9F9")
	colDim    = lipgloss.Color("#999999")
	colShine  = lipgloss.Color("#EB9F7F") // the moving highlight on busy text
)

type styles struct {
	accent, ok, err, warn, sel, dim, bold, border, shine lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	return styles{
		accent: r.NewStyle().Foreground(colAccent),
		ok:     r.NewStyle().Foreground(colOK),
		err:    r.NewStyle().Foreground(colErr),
		warn:   r.NewStyle().Foreground(colWarn),
		sel:    r.NewStyle().Foreground(colSelect),
		dim:    r.NewStyle().Foreground(colDim),
		bold:   r.NewStyle().Bold(true),
		border: r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 1),
		shine:  r.NewStyle().Foreground(colShine).Bold(true),
	}
}

// printer holds what both implementations share: writing styled lines.
type printer struct {
	out   io.Writer
	st    styles
	width int // terminal width, 0 when unknown (plain output)
}

func newPrinter(out io.Writer, color bool) printer {
	r := lipgloss.NewRenderer(out)
	// Never ask the terminal for its background color: the query reads from
	// stdin and eats the first keys typed (or hangs where nobody answers).
	r.SetHasDarkBackground(true)
	if !color {
		r.SetColorProfile(termenv.Ascii)
	}
	return printer{out: out, st: newStyles(r)}
}

func (p printer) line(s string) { fmt.Fprintln(p.out, s) }

func (p printer) Banner(title string, lines ...string) {
	body := p.st.accent.Render("✻") + " " + p.st.bold.Render(title)
	if len(lines) > 0 {
		body += "\n\n" + p.st.dim.Render(strings.Join(lines, "\n"))
	}
	box := p.st.border
	if p.width > 0 {
		box = box.Width(min(p.width-2, 100)) // wraps long paths instead of breaking the frame
	}
	p.line(box.Render(body))
}

func (p printer) Section(title string) {
	p.line("\n" + p.st.accent.Render("⏺") + " " + p.st.bold.Render(title))
}

func (p printer) OK(format string, a ...any) {
	p.line("  " + p.st.ok.Render("✓") + " " + fmt.Sprintf(format, a...))
}

func (p printer) Warn(format string, a ...any) {
	p.line("  " + p.st.warn.Render("⚠") + " " + fmt.Sprintf(format, a...))
}

func (p printer) Fail(format string, a ...any) {
	p.line("  " + p.st.err.Render("✗") + " " + fmt.Sprintf(format, a...))
}

func (p printer) Info(format string, a ...any) {
	p.line("  " + p.st.dim.Render(fmt.Sprintf(format, a...)))
}

func (p printer) Item(format string, a ...any) {
	p.line("    " + p.st.dim.Render("⎿") + " " + fmt.Sprintf(format, a...))
}

func (p printer) Field(key, value string) {
	p.line("  " + p.st.dim.Render(fmt.Sprintf("%-14s", key)) + value)
}

// styled applies st to s without losing it after a badge inside s: every
// reset a badge ends with would otherwise end st too.
func styled(st lipgloss.Style, s string) string {
	parts := strings.Split(s, "\x1b[0m")
	for i, part := range parts {
		if part != "" {
			parts[i] = st.Render(part)
		}
	}
	return strings.Join(parts, "")
}

func (p printer) Badge(kind Kind, text string) string {
	switch kind {
	case KindOK:
		return p.st.ok.Render(text)
	case KindWarn:
		return p.st.warn.Render(text)
	case KindErr:
		return p.st.err.Render(text)
	}
	return p.st.dim.Render(text)
}

// done renders the line an answered question leaves behind.
func (p printer) done(label, answer, msg string) string {
	s := "  " + p.st.ok.Render("✓") + " " + label
	if answer != "" {
		s += "  " + styled(p.st.dim, answer)
	}
	if msg != "" {
		s += "\n    " + p.st.dim.Render("⎿") + " " + p.st.ok.Render(msg)
	}
	return s
}

func (p printer) failed(label, msg string) string {
	return "  " + p.st.err.Render("✗") + " " + label + "\n    " + p.st.dim.Render("⎿") + " " + p.st.err.Render(msg)
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}
