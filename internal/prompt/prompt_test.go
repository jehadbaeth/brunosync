package prompt

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPrompts(t *testing.T) {
	// Windows line endings, an invalid answer and defaults.
	in := "\r\nstaging\r\nmaybe\ny\n7\n2\na, b ,,c\n"
	var out bytes.Buffer
	p := New(strings.NewReader(in), &out)

	if s, _ := p.Ask("Name", "local"); s != "local" {
		t.Errorf("default not used: %q", s)
	}
	if s, _ := p.AskValid("Env", "", func(s string) (string, error) {
		if s == "" {
			return "", fmt.Errorf("required")
		}
		return "looks good", nil
	}); s != "staging" {
		t.Errorf("got %q", s)
	}
	if ok, _ := p.Confirm("Sure", false); !ok {
		t.Error("confirm after an invalid answer failed")
	}
	if i, _ := p.Choose("Pick", []string{"a", "b"}, -1); i != 1 {
		t.Errorf("choose = %d", i)
	}
	if l, _ := p.List("List", nil); strings.Join(l, "|") != "a|b|c" {
		t.Errorf("list = %v", l)
	}
	for _, want := range []string{"? Name [local]: ", "✓ looks good", "please answer y or n", "enter a number from 1 to 2"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output misses %q:\n%s", want, out.String())
		}
	}
	if _, err := p.Ask("More", "x"); !errors.Is(err, ErrAborted) {
		t.Errorf("end of input must abort, got %v", err)
	}
}

func TestLastLineWithoutNewline(t *testing.T) {
	p := New(strings.NewReader("yes"), &bytes.Buffer{})
	if ok, err := p.Confirm("Sure", false); err != nil || !ok {
		t.Errorf("got %v %v", ok, err)
	}
}

func TestBackToken(t *testing.T) {
	p := New(strings.NewReader("<\n<\n<\n"), &bytes.Buffer{})
	if _, err := p.Ask("Name", "x"); !errors.Is(err, ErrBack) {
		t.Errorf("ask: %v", err)
	}
	if _, err := p.Choose("Pick", []string{"a"}, 0); !errors.Is(err, ErrBack) {
		t.Errorf("choose: %v", err)
	}
	if _, err := p.Confirm("Sure", true); !errors.Is(err, ErrBack) {
		t.Errorf("confirm: %v", err)
	}
}
