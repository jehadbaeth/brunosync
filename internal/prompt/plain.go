package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Plain is the line based UI: one answer per line, numbers for menus, no
// colors. Tests and scripted input use it. An answer of "<" goes back, like
// Esc in the terminal UI.
type Plain struct {
	printer
	in *bufio.Reader
}

var _ UI = (*Plain)(nil)

// New returns a plain UI reading answers from in and writing to out.
func New(in io.Reader, out io.Writer) *Plain {
	return &Plain{printer: newPrinter(out, false), in: bufio.NewReader(in)}
}

func (p *Plain) readLine() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(p.out)
			return "", ErrAborted
		}
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// Ask implements UI.
func (p *Plain) Ask(label, def string) (string, error) { return p.AskValid(label, def, nil) }

// AskValid implements UI.
func (p *Plain) AskValid(label, def string, check Check) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(p.out, "  ? %s [%s]: ", label, def)
		} else {
			fmt.Fprintf(p.out, "  ? %s: ", label)
		}
		s, err := p.readLine()
		if err != nil {
			return "", err
		}
		if s == backToken {
			return "", ErrBack
		}
		if s == "" {
			s = def
		}
		if check == nil {
			return s, nil
		}
		msg, err := check(s)
		if err != nil {
			p.Fail("%v", err)
			continue
		}
		if msg != "" {
			p.OK("%s", msg)
		}
		return s, nil
	}
}

// Confirm implements UI.
func (p *Plain) Confirm(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "  ? %s [%s]: ", label, hint)
		s, err := p.readLine()
		if err != nil {
			return false, err
		}
		if s == backToken {
			return false, ErrBack
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		p.Fail("please answer y or n")
	}
}

// Choose implements UI.
func (p *Plain) Choose(label string, options []string, def int) (int, error) {
	fmt.Fprintf(p.out, "  ? %s\n", label)
	for i, o := range options {
		fmt.Fprintf(p.out, "    %d) %s\n", i+1, o)
	}
	d := ""
	if def >= 0 {
		d = strconv.Itoa(def + 1)
	}
	s, err := p.AskValid("Choose", d, func(s string) (string, error) {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > len(options) {
			return "", fmt.Errorf("enter a number from 1 to %d", len(options))
		}
		return "", nil
	})
	if err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(s)
	return n - 1, nil
}

// List implements UI.
func (p *Plain) List(label string, def []string) ([]string, error) {
	s, err := p.Ask(label, strings.Join(def, ", "))
	if err != nil {
		return nil, err
	}
	return splitList(s), nil
}

// Task implements UI.
func (p *Plain) Task(label string, fn func() (string, error)) (string, error) {
	msg, err := fn()
	if err != nil {
		p.line(p.failed(label, err.Error()))
		return "", err
	}
	p.line(p.done(label, "", msg))
	return msg, nil
}
