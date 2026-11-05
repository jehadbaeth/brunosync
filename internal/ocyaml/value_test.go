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
	out, err := Marshal(M("a", "10", "b", "true", "c", "2023-02-23", "d", "null", "e", "{{x}}/y"))
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		if _, ok := back.Value(k).(string); !ok {
			t.Errorf("%s did not survive as a string:\n%s", k, out)
		}
	}
}
