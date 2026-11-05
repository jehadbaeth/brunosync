// Package merge implements the three way merge between the last generated
// request (base), the request file as it is now (current) and the request
// generated from the latest spec (incoming). See DESIGN.md 4.2.
//
// The rule for every generator owned field:
//
//	current == base      -> take incoming (spec changed, nobody edited)
//	incoming == base     -> keep current (human edit)
//	current == incoming  -> keep current (both arrived at the same value)
//	otherwise            -> keep current and report a conflict
//
// Anything not listed as generator owned (runtime scripts, tests, assertions,
// variables, settings, info.seq, unknown keys) is never touched.
package merge

import (
	"fmt"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/gen"
	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

// Conflict is a field where both the spec and a human changed the value.
type Conflict struct {
	Field    string
	Current  any
	Incoming any
}

// Result of merging one request.
type Result struct {
	Doc       *ocyaml.Map
	Changed   bool
	Conflicts []Conflict
}

// scalarFields are generator owned fields merged as a whole.
var scalarFields = []string{
	"info.name",
	"info.tags",
	"info.description",
	"http.method",
	"http.body",
	"http.auth",
	"docs",
}

// keyedList describes a generator owned list merged entry by entry.
type keyedList struct {
	path     string
	key      func(*ocyaml.Map) string
	perField bool // merge individual entry fields instead of whole entries
}

var keyedLists = []keyedList{
	{path: "http.headers", key: func(m *ocyaml.Map) string { return strings.ToLower(m.String("name")) }, perField: true},
	{path: "http.params", key: func(m *ocyaml.Map) string { return m.String("type") + ":" + m.String("name") }, perField: true},
	{path: "examples", key: func(m *ocyaml.Map) string { return m.String("name") }},
}

// Pick applies the three way rule to one value.
func Pick(base, current, incoming any) (value any, conflict bool) {
	switch {
	case ocyaml.Equal(current, base):
		return incoming, false
	case ocyaml.Equal(incoming, base), ocyaml.Equal(current, incoming):
		return current, false
	default:
		return current, true
	}
}

// Request merges one request document. None of the inputs are modified.
func Request(base, current, incoming *ocyaml.Map) Result {
	out := ocyaml.CloneMap(current)
	var conflicts []Conflict

	for _, f := range scalarFields {
		v, c := Pick(base.Path(f), current.Path(f), incoming.Path(f))
		if c {
			conflicts = append(conflicts, Conflict{Field: f, Current: current.Path(f), Incoming: incoming.Path(f)})
		}
		setOwned(out, f, ocyaml.Clone(v), incoming)
	}

	for _, kl := range keyedLists {
		merged, cs := mergeList(kl, asList(base.Path(kl.path)), asList(current.Path(kl.path)), asList(incoming.Path(kl.path)))
		conflicts = append(conflicts, cs...)
		if len(merged) == 0 {
			merged = nil
		}
		var v any
		if merged != nil {
			v = merged
		}
		setOwned(out, kl.path, v, incoming)
	}

	// The URL path is merged on its own. The query string is rebuilt from the
	// merged params, like Bruno does, so enabling a query param in Bruno is not
	// mistaken for a URL edit.
	pathVal, conflict := Pick(urlPath(base), urlPath(current), urlPath(incoming))
	if conflict {
		conflicts = append(conflicts, Conflict{Field: "http.url", Current: urlPath(current), Incoming: urlPath(incoming)})
	}
	if p, ok := pathVal.(string); ok {
		setOwned(out, "http.url", p+gen.QueryString(asList(out.Path("http.params"))), incoming)
	}

	return Result{Doc: out, Changed: !ocyaml.Equal(out, current), Conflicts: conflicts}
}

// setOwned sets a field, placing a key that is new to the document where it
// sits in the incoming document, so files stay in Bruno's block order.
func setOwned(doc *ocyaml.Map, path string, v any, incoming *ocyaml.Map) {
	parts := strings.Split(path, ".")
	parent := doc
	incParent := incoming
	for _, p := range parts[:len(parts)-1] {
		if v == nil && parent.Map(p) == nil {
			return
		}
		if parent.Map(p) == nil {
			prev := previousKey(incParent, p)
			parent.SetAfter(prev, p, ocyaml.NewMap())
		}
		parent = parent.Map(p)
		incParent = incParent.Map(p)
	}
	leaf := parts[len(parts)-1]
	if v == nil {
		parent.Delete(leaf)
		return
	}
	if _, exists := parent.Get(leaf); exists {
		parent.Set(leaf, v)
		return
	}
	parent.SetAfter(previousKey(incParent, leaf), leaf, v)
}

// previousKey returns the key that comes before `key` in m, or "".
func previousKey(m *ocyaml.Map, key string) string {
	prev := ""
	for _, k := range m.Keys() {
		if k == key {
			return prev
		}
		prev = k
	}
	return ""
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

type indexed struct {
	order []string
	byKey map[string]*ocyaml.Map
}

func index(l []any, key func(*ocyaml.Map) string) indexed {
	ix := indexed{byKey: map[string]*ocyaml.Map{}}
	for _, e := range l {
		m, ok := e.(*ocyaml.Map)
		if !ok {
			continue
		}
		k := key(m)
		if _, dup := ix.byKey[k]; dup {
			continue
		}
		ix.order = append(ix.order, k)
		ix.byKey[k] = m
	}
	return ix
}

func mergeList(kl keyedList, base, current, incoming []any) ([]any, []Conflict) {
	b, c, n := index(base, kl.key), index(current, kl.key), index(incoming, kl.key)
	var out []any
	var conflicts []Conflict
	label := func(k string) string {
		// show the entry name as written (headers are keyed case insensitively)
		for _, ix := range []indexed{n, c, b} {
			if e, ok := ix.byKey[k]; ok && e.String("name") != "" {
				return fmt.Sprintf("%s[%s]", kl.path, e.String("name"))
			}
		}
		return fmt.Sprintf("%s[%s]", kl.path, k)
	}

	for _, k := range n.order {
		ne := n.byKey[k]
		be, inB := b.byKey[k]
		ce, inC := c.byKey[k]
		switch {
		case !inC && !inB:
			out = append(out, ocyaml.Clone(ne))
		case !inC && inB:
			// A human deleted a generated entry. Respect that unless the
			// spec changed the entry since, which is worth surfacing.
			if !ocyaml.Equal(be, ne) {
				out = append(out, ocyaml.Clone(ne))
			}
		case inC && !inB:
			// The human added an entry the spec now also has. Keep theirs.
			out = append(out, ocyaml.Clone(ce))
		default:
			if !kl.perField {
				v, conflict := Pick(be, ce, ne)
				if conflict {
					conflicts = append(conflicts, Conflict{Field: label(k), Current: ce, Incoming: ne})
				}
				out = append(out, ocyaml.Clone(v))
				continue
			}
			merged, cs := mergeEntry(be, ce, ne)
			for _, cf := range cs {
				cf.Field = label(k) + "." + cf.Field
				conflicts = append(conflicts, cf)
			}
			out = append(out, merged)
		}
	}

	for _, k := range c.order {
		if _, inN := n.byKey[k]; inN {
			continue
		}
		ce := c.byKey[k]
		be, inB := b.byKey[k]
		if !inB {
			out = append(out, ocyaml.Clone(ce)) // added by a human
			continue
		}
		if ocyaml.Equal(ce, be) {
			continue // removed from the spec, untouched locally
		}
		conflicts = append(conflicts, Conflict{Field: label(k), Current: ce, Incoming: nil})
		out = append(out, ocyaml.Clone(ce))
	}
	return out, conflicts
}

func mergeEntry(b, c, n *ocyaml.Map) (*ocyaml.Map, []Conflict) {
	out := ocyaml.CloneMap(c)
	var conflicts []Conflict
	keys := append(c.Keys(), n.Keys()...)
	keys = append(keys, b.Keys()...)
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		v, conflict := Pick(b.Value(k), c.Value(k), n.Value(k))
		if conflict {
			conflicts = append(conflicts, Conflict{Field: k, Current: c.Value(k), Incoming: n.Value(k)})
		}
		if v == nil {
			out.Delete(k)
		} else {
			out.Set(k, ocyaml.Clone(v))
		}
	}
	return out, conflicts
}

// HasUserWork reports whether a request contains anything a human added or
// changed compared to its base, used to decide between archiving and deleting.
func HasUserWork(base, current *ocyaml.Map) bool {
	if base == nil {
		return true
	}
	if !ocyaml.Equal(base.Path("http.url"), current.Path("http.url")) {
		return true
	}
	if rt := current.Map("runtime"); rt != nil && rt.Len() > 0 {
		return true
	}
	for _, f := range scalarFields {
		if !ocyaml.Equal(base.Path(f), current.Path(f)) {
			return true
		}
	}
	for _, kl := range keyedLists {
		if !ocyaml.Equal(base.Path(kl.path), current.Path(kl.path)) {
			return true
		}
	}
	return false
}

func splitURL(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	p, _, _ := strings.Cut(s, "?")
	return p, true
}

func urlPath(doc *ocyaml.Map) any {
	if p, ok := splitURL(doc.Path("http.url")); ok {
		return p
	}
	return nil
}
