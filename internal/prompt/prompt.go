// Package prompt asks questions on a line based terminal. It reads plain
// lines so it works in every terminal (Windows included) and can be driven by
// a script in tests.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrAborted is returned when the input ends before a question is answered.
var ErrAborted = errors.New("aborted")

// Prompter asks questions.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

// New returns a prompter reading answers from in and writing to out.
func New(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

// Printf writes text to the output.
func (p *Prompter) Printf(format string, a ...any) { fmt.Fprintf(p.out, format, a...) }

func (p *Prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(p.out)
			return "", ErrAborted
		}
		return "", err
	}
	return strings.TrimSpace(strings.TrimRight(s, "\r\n")), nil
}

// Ask asks for a string. An empty answer returns def.
func (p *Prompter) Ask(label, def string) (string, error) {
	return p.AskValid(label, def, nil)
}

// AskValid asks until check accepts the answer. An empty answer returns def
// (also checked). check may be nil.
func (p *Prompter) AskValid(label, def string, check func(string) error) (string, error) {
	for {
		if def != "" {
			fmt.Fprintf(p.out, "%s [%s]: ", label, def)
		} else {
			fmt.Fprintf(p.out, "%s: ", label)
		}
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s == "" {
			s = def
		}
		if check != nil {
			if err := check(s); err != nil {
				fmt.Fprintf(p.out, "  %v\n", err)
				continue
			}
		}
		return s, nil
	}
}

// Confirm asks a yes or no question.
func (p *Prompter) Confirm(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", label, hint)
		s, err := p.line()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(p.out, "  please answer y or n")
	}
}

// Choose shows a numbered list and returns the index picked. def is the
// index used for an empty answer, or -1 for no default.
func (p *Prompter) Choose(label string, options []string, def int) (int, error) {
	fmt.Fprintln(p.out, label)
	for i, o := range options {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, o)
	}
	d := ""
	if def >= 0 {
		d = strconv.Itoa(def + 1)
	}
	s, err := p.AskValid("Choose", d, func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > len(options) {
			return fmt.Errorf("enter a number from 1 to %d", len(options))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(s)
	return n - 1, nil
}

// List asks for a comma separated list. An empty answer returns def.
func (p *Prompter) List(label string, def []string) ([]string, error) {
	s, err := p.Ask(label, strings.Join(def, ", "))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out, nil
}
