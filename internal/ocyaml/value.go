// Package ocyaml holds an ordered, generic representation of YAML documents.
//
// Bruno collection files are YAML. brunosync has to read files that users
// edited in Bruno, change a few fields and write them back without reordering
// or dropping anything it does not understand. A plain map[string]any loses
// key order, so this package uses its own ordered Map.
//
// Values are one of: nil, string, bool, int64, float64, []any, *Map.
package ocyaml

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// Map is an insertion ordered string keyed map.
type Map struct {
	keys []string
	vals map[string]any
}

// NewMap returns an empty Map.
func NewMap() *Map { return &Map{vals: map[string]any{}} }

// M builds a Map from alternating key, value arguments. Nil values are skipped,
// which keeps optional fields out of the generated files.
func M(kv ...any) *Map {
	m := NewMap()
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == nil {
			continue
		}
		m.Set(kv[i].(string), kv[i+1])
	}
	return m
}

// Keys returns the keys in order.
func (m *Map) Keys() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.keys...)
}

// Len returns the number of entries.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// Get returns the value for key.
func (m *Map) Get(key string) (any, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m.vals[key]
	return v, ok
}

// Value returns the value for key or nil.
func (m *Map) Value(key string) any {
	v, _ := m.Get(key)
	return v
}

// Set inserts or replaces key. A new key goes to the end.
func (m *Map) Set(key string, v any) {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = v
}

// SetAfter inserts key directly after the key `after` if it is new. Existing
// keys keep their position. If `after` does not exist the key is appended.
func (m *Map) SetAfter(after, key string, v any) {
	if _, ok := m.vals[key]; ok || m.vals == nil {
		m.Set(key, v)
		return
	}
	idx := -1
	for i, k := range m.keys {
		if k == after {
			idx = i
			break
		}
	}
	if idx < 0 {
		m.Set(key, v)
		return
	}
	m.keys = append(m.keys[:idx+1], append([]string{key}, m.keys[idx+1:]...)...)
	m.vals[key] = v
}

// Delete removes key.
func (m *Map) Delete(key string) {
	if m == nil {
		return
	}
	if _, ok := m.vals[key]; !ok {
		return
	}
	delete(m.vals, key)
	for i, k := range m.keys {
		if k == key {
			m.keys = append(m.keys[:i], m.keys[i+1:]...)
			break
		}
	}
}

// Map returns the child map at key, or nil.
func (m *Map) Map(key string) *Map {
	c, _ := m.Value(key).(*Map)
	return c
}

// EnsureMap returns the child map at key, creating it if needed.
func (m *Map) EnsureMap(key string) *Map {
	if c := m.Map(key); c != nil {
		return c
	}
	c := NewMap()
	m.Set(key, c)
	return c
}

// List returns the list at key, or nil.
func (m *Map) List(key string) []any {
	l, _ := m.Value(key).([]any)
	return l
}

// String returns the string at key, or "".
func (m *Map) String(key string) string {
	switch v := m.Value(key).(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return Scalar(v)
	}
}

// Path returns the value at a dotted path such as "http.url".
func (m *Map) Path(path string) any {
	parts := strings.Split(path, ".")
	cur := m
	for i, p := range parts {
		if cur == nil {
			return nil
		}
		v, ok := cur.Get(p)
		if !ok {
			return nil
		}
		if i == len(parts)-1 {
			return v
		}
		cur, _ = v.(*Map)
	}
	return nil
}

// SetPath sets the value at a dotted path, creating intermediate maps. A nil
// value deletes the leaf.
func (m *Map) SetPath(path string, v any) {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		if v == nil && cur.Map(p) == nil {
			return
		}
		cur = cur.EnsureMap(p)
	}
	leaf := parts[len(parts)-1]
	if v == nil {
		cur.Delete(leaf)
		return
	}
	cur.Set(leaf, v)
}

// Clone deep copies a value.
func Clone(v any) any {
	switch t := v.(type) {
	case *Map:
		if t == nil {
			return (*Map)(nil)
		}
		c := NewMap()
		for _, k := range t.keys {
			c.Set(k, Clone(t.vals[k]))
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, e := range t {
			c[i] = Clone(e)
		}
		return c
	default:
		return t
	}
}

// CloneMap deep copies a map.
func CloneMap(m *Map) *Map {
	if m == nil {
		return nil
	}
	return Clone(m).(*Map)
}

// Equal compares two values structurally. Map key order is ignored, list order
// is not. Numbers compare by value, so int64(1) equals float64(1).
func Equal(a, b any) bool {
	a, b = normalizeEmpty(a), normalizeEmpty(b)
	switch x := a.(type) {
	case nil:
		return b == nil
	case *Map:
		y, ok := b.(*Map)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for _, k := range x.keys {
			yv, ok := y.Get(k)
			if !ok || !Equal(x.vals[k], yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case int64, float64:
		fx, okx := toFloat(x)
		fy, oky := toFloat(b)
		return okx && oky && fx == fy
	default:
		return a == b
	}
}

// normalizeEmpty treats empty maps and lists like absent values, because Bruno
// drops empty blocks when it saves a file.
func normalizeEmpty(v any) any {
	switch t := v.(type) {
	case *Map:
		if t == nil || t.Len() == 0 {
			return nil
		}
	case []any:
		if len(t) == 0 {
			return nil
		}
	}
	return v
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// Scalar renders a scalar value as the string a user would type.
func Scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

// ---------------------------------------------------------------------------
// YAML conversion

// Parse decodes a YAML document into a Map. An empty document gives an empty Map.
func Parse(data []byte) (*Map, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return NewMap(), nil
	}
	v, err := FromNode(doc.Content[0])
	if err != nil {
		return nil, err
	}
	m, ok := v.(*Map)
	if !ok {
		return nil, fmt.Errorf("top level YAML value is not a mapping")
	}
	return m, nil
}

// FromNode converts a yaml.v3 node into a generic value.
func FromNode(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return FromNode(n.Content[0])
	case yaml.AliasNode:
		return FromNode(n.Alias)
	case yaml.MappingNode:
		m := NewMap()
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode && k.Tag == "!!merge" {
				continue
			}
			v, err := FromNode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m.Set(k.Value, v)
		}
		return m, nil
	case yaml.SequenceNode:
		l := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := FromNode(c)
			if err != nil {
				return nil, err
			}
			l = append(l, v)
		}
		return l, nil
	case yaml.ScalarNode:
		return ScalarFromTag(n.ShortTag(), n.Value), nil
	}
	return nil, fmt.Errorf("unsupported YAML node kind %v", n.Kind)
}

// ScalarFromTag converts a scalar with a resolved YAML tag to a Go value.
func ScalarFromTag(tag, value string) any {
	switch tag {
	case "!!null":
		return nil
	case "!!bool":
		b, err := strconv.ParseBool(strings.ToLower(value))
		if err != nil {
			return value
		}
		return b
	case "!!int":
		if i, err := strconv.ParseInt(value, 0, 64); err == nil {
			return i
		}
		return value
	case "!!float":
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
		return value
	default:
		return value
	}
}

// ToNode converts a generic value into a yaml.v3 node.
func ToNode(v any) *yaml.Node {
	switch t := v.(type) {
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case *Map:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if t == nil {
			return n
		}
		for _, k := range t.keys {
			n.Content = append(n.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
				ToNode(t.vals[k]))
		}
		return n
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range t {
			n.Content = append(n.Content, ToNode(e))
		}
		return n
	case string:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}
		if strings.Contains(t, "\n") {
			n.Style = yaml.LiteralStyle
		}
		return n
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(t)}
	case int64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(t, 10)}
	case int:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(t)}
	case float64:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(t, 'g', -1, 64)}
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(t)}
	}
}

// Marshal renders a Map as YAML with two space indentation, the way Bruno
// writes its files. Top level blocks are separated by a blank line, which
// matches Bruno's layout and keeps diffs readable.
func Marshal(m *Map) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(ToNode(m)); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return addBlockSpacing(buf.Bytes()), nil
}

func addBlockSpacing(b []byte) []byte {
	lines := strings.Split(string(b), "\n")
	var out []string
	for i, l := range lines {
		isTopKey := i > 0 && len(l) > 0 && l[0] != ' ' && l[0] != '-' && l[0] != '#'
		if isTopKey && len(out) > 0 && out[len(out)-1] != "" {
			// only separate top level keys whose previous block spans more than one line
			// or is itself a block, which mirrors Bruno's output closely enough
			prev := out[len(out)-1]
			if strings.HasPrefix(prev, " ") || strings.HasPrefix(prev, "-") {
				out = append(out, "")
			}
		}
		out = append(out, l)
	}
	return []byte(strings.Join(out, "\n"))
}

// MustMarshalString is a helper for comparisons and reports.
func MustMarshalString(v any) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(ToNode(v)); err != nil {
		return fmt.Sprint(v)
	}
	_ = enc.Close()
	return strings.TrimSpace(buf.String())
}
