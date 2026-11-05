package ocyaml

import "testing"

// Bruno writes date-like strings unquoted when it saves a file. They must
// come back as the same string, or a save in Bruno would look like an edit.
func TestUnquotedScalarsStayStrings(t *testing.T) {
	m, err := Parse([]byte("d: 2023-02-23\nts: 2023-01-17T00:00:00Z\nq: \"10\"\nn: 10\nb: true\nv: 1.5\n"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Value("d") != "2023-02-23" || m.Value("ts") != "2023-01-17T00:00:00Z" {
		t.Errorf("dates changed: %#v %#v", m.Value("d"), m.Value("ts"))
	}
	if m.Value("q") != "10" || m.Value("n") != int64(10) || m.Value("b") != true || m.Value("v") != 1.5 {
		t.Errorf("scalars: %#v %#v %#v %#v", m.Value("q"), m.Value("n"), m.Value("b"), m.Value("v"))
	}
	if !Equal(m.Value("d"), "2023-02-23") {
		t.Error("Equal on dates")
	}
}

func TestMarshalQuotesAmbiguousStrings(t *testing.T) {
	out, err := Marshal(M("a", "10", "b", "true", "c", "2023-02-23", "d", "null", "e", "{{x}}/y",
		"f", "0_0001", "g", "1_3008", "h", "yes", "i", "off", "j", "1:20", "k", "0x1F", "l", "~", "m", ""))
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m"} {
		if _, ok := back.Value(k).(string); !ok {
			t.Errorf("%s did not survive as a string:\n%s", k, out)
		}
	}
}

func TestYAML11AmbiguousStringsAreQuoted(t *testing.T) {
	for _, s := range []string{"0_0001", "1_3008", "yes", "No", "on", "OFF", "1:20", "0x1F", "0b101", "~", "1e5", ".5", "-12", ".inf", ".NaN"} {
		if !yaml11Ambiguous(s) {
			t.Errorf("%q should be quoted", s)
		}
	}
	for _, s := range []string{"okapi", "abc_1", "1a", "v1.2.3", "{{x}}", "a:b", "2024-01-01T00:00:00Z extra"} {
		if yaml11Ambiguous(s) {
			t.Errorf("%q should stay plain", s)
		}
	}
}

// Bruno writes these unquoted and reads them back as strings.
func TestYAML11FormsReadAsStrings(t *testing.T) {
	m, err := Parse([]byte("a: 0_0001\nb: 1_3008\nc: 1:20\nd: yes\ne: 0b101\nf: 12\ng: 0x1F\nh: 1.5e3\ni: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]any{"a": "0_0001", "b": "1_3008", "c": "1:20", "d": "yes", "e": "0b101", "f": int64(12), "g": int64(31), "h": 1500.0, "i": false} {
		if got := m.Value(k); !Equal(got, want) {
			t.Errorf("%s = %#v, want %#v", k, got, want)
		}
	}
}
