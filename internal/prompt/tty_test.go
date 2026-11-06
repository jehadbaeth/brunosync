package prompt

import (
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func testPrinter() printer { return newPrinter(io.Discard, false) }

func TestSelectModel(t *testing.T) {
	var m tea.Model = newSelectModel(testPrinter(), "Pick", []string{"a", "b", "c"}, 2)
	m, _ = m.Update(key("down")) // wraps to the top
	m, _ = m.Update(key("down"))
	if got := m.(selectModel).cursor; got != 1 {
		t.Fatalf("cursor = %d", got)
	}
	m, cmd := m.Update(key("enter"))
	if !m.(selectModel).done || cmd == nil {
		t.Fatal("enter should finish")
	}
	if v := m.View(); !strings.Contains(v, "✓ Pick") || !strings.Contains(v, "b") {
		t.Errorf("final view = %q", v)
	}

	m = newSelectModel(testPrinter(), "Pick", []string{"a", "b", "c"}, 0)
	m, _ = m.Update(key("3"))
	if s := m.(selectModel); !s.done || s.cursor != 2 {
		t.Errorf("digit shortcut: %+v", s)
	}
}

func TestSelectModelScrolls(t *testing.T) {
	opts := make([]string, 30)
	for i := range opts {
		opts[i] = "option" + string(rune('A'+i))
	}
	var m tea.Model = newSelectModel(testPrinter(), "Pick", opts, 15)
	v := m.View()
	if strings.Count(v, "option") != menuRows || !strings.Contains(v, "↑ 10 more") || !strings.Contains(v, "↓ 10 more") {
		t.Errorf("window wrong:\n%s", v)
	}
	if !strings.Contains(v, "❯ 16. optionP") {
		t.Errorf("cursor not visible:\n%s", v)
	}
	m, _ = m.Update(key("1")) // no digit shortcuts with more than 9 options
	if m.(selectModel).done {
		t.Error("digit picked from a long list")
	}
}

func TestInputModelValidates(t *testing.T) {
	check := func(s string) (string, error) {
		if s != "good" {
			return "", errors.New("not good")
		}
		return "fine", nil
	}
	var m tea.Model = newInputModel(testPrinter(), "Value", "", check)
	m, _ = m.Update(key("bad"))
	m, cmd := m.Update(key("enter"))
	if !m.(inputModel).checking || cmd == nil {
		t.Fatal("enter should start the check")
	}
	m, _ = m.Update(key("x")) // typing is ignored while checking
	m, _ = m.Update(checkResult{err: errors.New("not good")})
	if im := m.(inputModel); im.done || im.errMsg != "not good" || !strings.Contains(m.View(), "✗ not good") {
		t.Fatalf("error not shown: %+v", im)
	}
	for range len("bad") {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	m, _ = m.Update(key("good"))
	m, _ = m.Update(key("enter"))
	m, _ = m.Update(checkResult{msg: "fine"})
	im := m.(inputModel)
	if !im.done || im.value != "good" || !strings.Contains(m.View(), "fine") {
		t.Errorf("not accepted: %+v\n%s", im, m.View())
	}
}

func TestInputModelDefaultAndTab(t *testing.T) {
	var m tea.Model = newInputModel(testPrinter(), "Host", "localhost", nil)
	m, _ = m.Update(key("tab"))
	m, _ = m.Update(key(":8080"))
	m, _ = m.Update(key("enter"))
	if v := m.(inputModel).value; v != "localhost:8080" {
		t.Errorf("tab should take the default for editing, got %q", v)
	}
	m = newInputModel(testPrinter(), "Host", "localhost", nil)
	m, _ = m.Update(key("enter"))
	if v := m.(inputModel).value; v != "localhost" {
		t.Errorf("enter should keep the default, got %q", v)
	}
	m = newInputModel(testPrinter(), "Host", "", nil)
	m, _ = m.Update(key("ctrl+c"))
	if !m.(inputModel).aborted {
		t.Error("ctrl+c should abort")
	}
}
