package prompt

import (
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

// TTY is the terminal UI. Every question is a small inline Bubble Tea
// program. When it finishes it leaves one line behind (✓ question  answer),
// so the screen reads like a transcript of the setup.
type TTY struct {
	printer
	in io.Reader
}

var _ UI = (*TTY)(nil)

// NewTTY returns the terminal UI. in should be the terminal.
func NewTTY(in io.Reader, out io.Writer) *TTY {
	// Old Windows consoles need this before any colored output.
	_, _ = termenv.EnableVirtualTerminalProcessing(termenv.NewOutput(out))
	// Without this, adaptive colors (used inside bubbles) ask the terminal
	// for its background color and the reply, or the user's first keys, get
	// lost. Our palette reads well on both backgrounds.
	lipgloss.SetHasDarkBackground(true)
	p := newPrinter(out, true)
	p.width = termWidth(out)
	return &TTY{printer: p, in: in}
}

// termWidth returns the terminal width, or 80 when it is unknown.
func termWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok {
		if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
			return w
		}
	}
	return 80
}

// Claude Code's spinner.
var claudeSpinner = spinner.Spinner{
	Frames: []string{"·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"},
	FPS:    time.Second / 8,
}

func (t *TTY) run(m tea.Model) (tea.Model, error) {
	final, err := tea.NewProgram(m, tea.WithInput(t.in), tea.WithOutput(t.out)).Run()
	if err != nil {
		return nil, err
	}
	if e, ok := final.(interface{ exit() (aborted, back bool) }); ok {
		switch aborted, back := e.exit(); {
		case aborted:
			return nil, ErrAborted
		case back:
			return nil, ErrBack
		}
	}
	return final, nil
}

// Ask implements UI.
func (t *TTY) Ask(label, def string) (string, error) { return t.AskValid(label, def, nil) }

// AskValid implements UI.
func (t *TTY) AskValid(label, def string, check Check) (string, error) {
	final, err := t.run(newInputModel(t.printer, label, def, check))
	if err != nil {
		return "", err
	}
	return final.(inputModel).value, nil
}

// Confirm implements UI.
func (t *TTY) Confirm(label string, def bool) (bool, error) {
	d := 1
	if def {
		d = 0
	}
	i, err := t.Choose(label, []string{"Yes", "No"}, d)
	return i == 0, err
}

// Choose implements UI.
func (t *TTY) Choose(label string, options []string, def int) (int, error) {
	final, err := t.run(newSelectModel(t.printer, label, options, def))
	if err != nil {
		return 0, err
	}
	return final.(selectModel).cursor, nil
}

// List implements UI.
func (t *TTY) List(label string, def []string) ([]string, error) {
	s, err := t.Ask(label, strings.Join(def, ", "))
	return splitList(s), err
}

// Task implements UI.
func (t *TTY) Task(label string, fn func() (string, error)) (string, error) {
	final, err := t.run(newTaskModel(t.printer, label, fn))
	if err != nil {
		return "", err
	}
	m := final.(taskModel)
	return m.msg, m.err
}

// ---- text input

type checkResult struct {
	msg string
	err error
}

type inputModel struct {
	p            printer
	label        string
	def          string
	check        Check
	ti           textinput.Model
	sp           spinner.Model
	width        int
	checking     bool
	checkStarted time.Time
	errMsg       string
	okMsg        string
	value        string
	done         bool
	aborted      bool
	back         bool
	frame        int
}

func newInputModel(p printer, label, def string, check Check) inputModel {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.PromptStyle = p.st.dim
	ti.Placeholder = def
	ti.PlaceholderStyle = p.st.dim
	ti.Focus()
	sp := spinner.New(spinner.WithSpinner(claudeSpinner), spinner.WithStyle(p.st.accent))
	m := inputModel{p: p, label: label, def: def, check: check, ti: ti, sp: sp}
	m.resize(p.width)
	return m
}

// resize fits the input box into the terminal, leaving room for the indent.
func (m *inputModel) resize(termWidth int) {
	if termWidth <= 0 {
		return
	}
	m.width = min(max(termWidth-6, 30), 90)
	m.ti.Width = m.width - 6
}

func (m inputModel) exit() (bool, bool) { return m.aborted, m.back }

func (m inputModel) Init() tea.Cmd { return textinput.Blink }

func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.resize(msg.Width)
		return m, nil
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.aborted = true
			return m, tea.Quit
		}
		if msg.Type == tea.KeyEsc {
			// Also while a slow check runs; its result is dropped.
			m.back = true
			return m, tea.Quit
		}
		if m.checking {
			return m, nil
		}
		if msg.Type == tea.KeyEnter {
			v := strings.TrimSpace(m.ti.Value())
			if v == "" {
				v = m.def
			}
			m.value = v
			if m.check == nil {
				m.done = true
				return m, tea.Quit
			}
			m.checking, m.errMsg, m.checkStarted = true, "", time.Now()
			check := m.check
			return m, tea.Batch(m.sp.Tick, func() tea.Msg {
				ok, err := check(v)
				return checkResult{ok, err}
			})
		}
		if msg.Type == tea.KeyTab && m.ti.Value() == "" && m.def != "" {
			m.ti.SetValue(m.def) // tab takes the default for editing
			m.ti.CursorEnd()
			return m, nil
		}
		m.errMsg = ""
	case checkResult:
		m.checking = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.okMsg, m.done = msg.msg, true
		return m, tea.Quit
	case spinner.TickMsg:
		if !m.checking {
			return m, nil
		}
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		m.frame++
		return m, cmd
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	return m, cmd
}

func (m inputModel) View() string {
	st := m.p.st
	if m.done {
		return m.p.done(m.label, or(m.value, "(none)"), m.okMsg) + "\n"
	}
	if m.aborted {
		return "  " + st.err.Render("✗") + " " + m.label + "\n"
	}
	if m.back {
		return ""
	}
	var b strings.Builder
	b.WriteString("  " + st.accent.Render("?") + " " + st.bold.Render(m.label) + "\n")
	box := st.border.BorderForeground(colDim).Width(m.width)
	b.WriteString(indent(box.Render(m.ti.View()), "  ") + "\n")
	switch {
	case m.checking && time.Since(m.checkStarted) > 250*time.Millisecond:
		// Quick checks never show the spinner, slow ones (git) do.
		b.WriteString("    " + m.sp.View() + " " + shimmer(st, "Checking "+m.value+"…", m.frame) + "\n")
	case m.errMsg != "":
		b.WriteString("    " + st.err.Render("✗ "+m.errMsg) + "\n")
	default:
		hint := "enter to confirm · esc to go back · ctrl+c to quit"
		if m.def != "" {
			hint = "enter keeps the grey value · tab edits it · esc to go back"
		}
		b.WriteString("    " + st.dim.Render(hint) + "\n")
	}
	return b.String()
}

// ---- menu

// menuRows is how many options a menu shows at once.
const menuRows = 10

type selectModel struct {
	p       printer
	label   string
	options []string
	cursor  int
	done    bool
	aborted bool
	back    bool
}

func newSelectModel(p printer, label string, options []string, def int) selectModel {
	if def < 0 || def >= len(options) {
		def = 0
	}
	return selectModel{p: p, label: label, options: options, cursor: def}
}

func (m selectModel) exit() (bool, bool) { return m.aborted, m.back }

func (m selectModel) Init() tea.Cmd { return nil }

func (m selectModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.String() {
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "esc":
		m.back = true
		return m, tea.Quit
	case "up", "k", "shift+tab":
		m.cursor = (m.cursor + len(m.options) - 1) % len(m.options)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.options)
	case "enter", " ":
		m.done = true
		return m, tea.Quit
	case "pgup", "home":
		m.cursor = max(m.cursor-menuRows, 0)
	case "pgdown", "end":
		m.cursor = min(m.cursor+menuRows, len(m.options)-1)
	default:
		// Digits pick directly when every option has a single digit number.
		if n, err := strconv.Atoi(k.String()); err == nil && len(m.options) <= 9 && n >= 1 && n <= len(m.options) {
			m.cursor, m.done = n-1, true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectModel) View() string {
	st := m.p.st
	if m.done {
		return m.p.done(m.label, m.options[m.cursor], "") + "\n"
	}
	if m.aborted {
		return "  " + st.err.Render("✗") + " " + m.label + "\n"
	}
	if m.back {
		return ""
	}
	var b strings.Builder
	b.WriteString("  " + st.accent.Render("?") + " " + st.bold.Render(m.label) + "\n")
	// Long lists show a window around the cursor so the question stays on
	// screen.
	start, end := 0, len(m.options)
	if len(m.options) > menuRows {
		start = min(max(m.cursor-menuRows/2, 0), len(m.options)-menuRows)
		end = start + menuRows
	}
	if start > 0 {
		b.WriteString("    " + st.dim.Render("↑ "+strconv.Itoa(start)+" more") + "\n")
	}
	for i := start; i < end; i++ {
		o := m.options[i]
		num := strconv.Itoa(i+1) + ". "
		if i == m.cursor {
			b.WriteString("  " + styled(st.sel, "❯ "+num+o) + "\n")
		} else {
			b.WriteString("    " + st.dim.Render(num) + o + "\n")
		}
	}
	if end < len(m.options) {
		b.WriteString("    " + st.dim.Render("↓ "+strconv.Itoa(len(m.options)-end)+" more") + "\n")
	}
	b.WriteString("    " + st.dim.Render("↑/↓ to move · enter to select · esc to go back · ctrl+c to quit") + "\n")
	return b.String()
}

// ---- spinner task

type taskDone struct {
	msg string
	err error
}

type taskModel struct {
	p       printer
	label   string
	fn      func() (string, error)
	sp      spinner.Model
	started time.Time
	msg     string
	err     error
	done    bool
	aborted bool
	frame   int
}

func newTaskModel(p printer, label string, fn func() (string, error)) taskModel {
	sp := spinner.New(spinner.WithSpinner(claudeSpinner), spinner.WithStyle(p.st.accent))
	return taskModel{p: p, label: label, fn: fn, sp: sp, started: time.Now()}
}

func (m taskModel) exit() (bool, bool) { return m.aborted, false }

func (m taskModel) Init() tea.Cmd {
	fn := m.fn
	return tea.Batch(m.sp.Tick, func() tea.Msg {
		msg, err := fn()
		return taskDone{msg, err}
	})
}

func (m taskModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.aborted = true
			return m, tea.Quit
		}
	case taskDone:
		m.msg, m.err, m.done = msg.msg, msg.err, true
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		m.frame++
		return m, cmd
	}
	return m, nil
}

func (m taskModel) View() string {
	switch {
	case m.done && m.err != nil:
		return m.p.failed(m.label, firstLines(m.err.Error())) + "\n"
	case m.done:
		return m.p.done(m.label, "", m.msg) + "\n"
	case m.aborted:
		return "  " + m.p.st.err.Render("✗") + " " + m.label + "\n"
	}
	secs := int(time.Since(m.started).Seconds())
	return "  " + m.sp.View() + " " + shimmer(m.p.st, m.label+"…", m.frame) + " " + m.p.st.dim.Render("("+strconv.Itoa(secs)+"s · ctrl+c to quit)") + "\n"
}

// shimmer renders busy text the way Claude Code does: accent colored, with a
// lighter highlight sweeping across it, one step per spinner frame.
func shimmer(st styles, text string, frame int) string {
	runes := []rune(text)
	const halo = 2 // highlighted runes on each side of the center
	span := len(runes) + 2*halo + 4
	center := (frame*2)%span - halo
	var b strings.Builder
	for i, r := range runes {
		if i >= center-halo && i <= center+halo {
			b.WriteString(st.shine.Render(string(r)))
		} else {
			b.WriteString(st.accent.Render(string(r)))
		}
	}
	return b.String()
}

func indent(s, pre string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pre + lines[i]
	}
	return strings.Join(lines, "\n")
}

func firstLines(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:197] + "..."
	}
	return s
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
