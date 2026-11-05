package merge

import (
	"testing"

	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

func req(name, url, body string, params ...*ocyaml.Map) *ocyaml.Map {
	h := ocyaml.M("method", "GET", "url", url)
	if len(params) > 0 {
		l := make([]any, len(params))
		for i, p := range params {
			l[i] = p
		}
		h.Set("params", l)
	}
	if body != "" {
		h.Set("body", ocyaml.M("type", "json", "data", body))
	}
	return ocyaml.M("info", ocyaml.M("name", name, "type", "http", "seq", int64(1)), "http", h)
}

func param(name, value string, disabled bool) *ocyaml.Map {
	p := ocyaml.M("name", name, "value", value, "type", "query")
	if disabled {
		p.Set("disabled", true)
	}
	return p
}

func TestPick(t *testing.T) {
	cases := []struct {
		b, c, n  any
		want     any
		conflict bool
	}{
		{"a", "a", "b", "b", false},     // spec changed
		{"a", "x", "a", "x", false},     // human changed
		{"a", "x", "x", "x", false},     // both to the same value
		{"a", "x", "y", "x", true},      // both differently
		{nil, nil, "new", "new", false}, // new field
		{"a", "a", nil, nil, false},     // removed by spec
	}
	for i, c := range cases {
		got, conflict := Pick(c.b, c.c, c.n)
		if !ocyaml.Equal(got, c.want) || conflict != c.conflict {
			t.Errorf("case %d: got %v/%v want %v/%v", i, got, conflict, c.want, c.conflict)
		}
	}
}

func TestUserOwnedFieldsSurvive(t *testing.T) {
	base := req("List", "{{b}}/items", "")
	cur := req("List", "{{b}}/items", "")
	cur.Set("runtime", ocyaml.M("scripts", []any{ocyaml.M("type", "tests", "code", "test()")}))
	cur.Set("settings", ocyaml.M("timeout", int64(5000)))
	cur.Map("info").Set("seq", int64(7))
	cur.Set("x-unknown", "keep me")
	inc := req("List all", "{{b}}/v2/items", "")

	res := Request(base, cur, inc)
	if len(res.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts %v", res.Conflicts)
	}
	d := res.Doc
	if d.Path("info.name") != "List all" || d.Path("http.url") != "{{b}}/v2/items" {
		t.Errorf("spec changes not applied: %v %v", d.Path("info.name"), d.Path("http.url"))
	}
	if d.Map("runtime") == nil || d.Path("settings.timeout") != int64(5000) || d.Path("info.seq") != int64(7) || d.Value("x-unknown") != "keep me" {
		t.Error("user owned fields were touched")
	}
}

func TestQueryStringFollowsParams(t *testing.T) {
	base := req("L", "{{b}}/items", "", param("status", "OPEN", true))
	// the user enabled the param in Bruno: the param and the url change
	cur := req("L", "{{b}}/items?status=OPEN", "", param("status", "OPEN", false))
	// the spec adds a required param and moves the path
	inc := req("L", "{{b}}/v2/items?limit=10", "", param("status", "OPEN", true), param("limit", "10", false))

	res := Request(base, cur, inc)
	if len(res.Conflicts) != 0 {
		t.Fatalf("enabling a query param must not conflict with a path change: %v", res.Conflicts)
	}
	if got := res.Doc.Path("http.url"); got != "{{b}}/v2/items?status=OPEN&limit=10" {
		t.Errorf("url = %v", got)
	}
}

func TestParamsMergeByName(t *testing.T) {
	base := req("L", "{{b}}/x", "", param("a", "", true), param("gone", "", true), param("edited", "1", true))
	cur := req("L", "{{b}}/x", "", param("a", "filled", true), param("gone", "", true), param("edited", "2", true), param("mine", "m", true))
	inc := req("L", "{{b}}/x", "", param("a", "", true), param("new", "n", true))

	res := Request(base, cur, inc)
	got := map[string]string{}
	for _, p := range res.Doc.Path("http.params").([]any) {
		m := p.(*ocyaml.Map)
		got[m.String("name")] = m.String("value")
	}
	want := map[string]string{"a": "filled", "new": "n", "mine": "m", "edited": "2"}
	if !ocyaml.Equal(toMap(got), toMap(want)) {
		t.Errorf("params = %v, want %v", got, want)
	}
	// "edited" was removed from the spec but changed locally: kept and reported
	if len(res.Conflicts) != 1 || res.Conflicts[0].Field != "http.params[edited]" {
		t.Errorf("conflicts = %+v", res.Conflicts)
	}
}

func toMap(m map[string]string) *ocyaml.Map {
	out := ocyaml.NewMap()
	for k, v := range m {
		out.Set(k, v)
	}
	return out
}

func TestBodyConflictKeepsCurrent(t *testing.T) {
	base := req("C", "{{b}}/x", `{"a": 1}`)
	cur := req("C", "{{b}}/x", `{"a": 2}`)
	inc := req("C", "{{b}}/x", `{"a": 1, "b": 1}`)
	res := Request(base, cur, inc)
	if res.Doc.Path("http.body.data") != `{"a": 2}` {
		t.Error("local body overwritten")
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0].Field != "http.body" {
		t.Errorf("conflicts = %+v", res.Conflicts)
	}
}

func TestUserExamplesKept(t *testing.T) {
	ex := func(name, data string) *ocyaml.Map {
		return ocyaml.M("name", name, "response", ocyaml.M("status", int64(200), "body", ocyaml.M("type", "json", "data", data)))
	}
	base := req("C", "{{b}}/x", "")
	base.Set("examples", []any{ex("200 Response", "{}")})
	cur := ocyaml.CloneMap(base)
	cur.Set("examples", []any{ex("200 Response", "{}"), ex("my saved response", `{"real": true}`)})
	inc := ocyaml.CloneMap(base)
	inc.Set("examples", []any{ex("200 Response", `{"new": 1}`)})

	res := Request(base, cur, inc)
	exs := res.Doc.List("examples")
	if len(exs) != 2 || exs[1].(*ocyaml.Map).String("name") != "my saved response" {
		t.Fatalf("user example lost: %v", ocyaml.MustMarshalString(exs))
	}
	if exs[0].(*ocyaml.Map).Path("response.body.data") != `{"new": 1}` {
		t.Error("generated example not updated")
	}
}

func TestHasUserWork(t *testing.T) {
	base := req("C", "{{b}}/x", "")
	if HasUserWork(base, ocyaml.CloneMap(base)) {
		t.Error("untouched request reported as having user work")
	}
	withTest := ocyaml.CloneMap(base)
	withTest.Set("runtime", ocyaml.M("scripts", []any{"x"}))
	if !HasUserWork(base, withTest) {
		t.Error("test script not detected")
	}
	if !HasUserWork(nil, base) {
		t.Error("missing base must count as user work (archive, never delete blindly)")
	}
}
