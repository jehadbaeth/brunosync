package spec

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pb33f/go-yaml"
	"github.com/pb33f/libopenapi/datamodel/high/base"

	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

// Direction controls which properties appear in a generated example.
type Direction int

const (
	// DirRequest examples skip readOnly properties.
	DirRequest Direction = iota
	// DirResponse examples skip writeOnly properties.
	DirResponse
)

const maxDepth = 12

// Example builds a deterministic example value for a schema.
//
// Precedence: schema example, first of schema examples, default, const,
// first enum value, then a placeholder derived from type and format. The
// output never contains random data, otherwise every sync would produce a diff.
func Example(sp *base.SchemaProxy, dir Direction) any {
	g := &exampleGen{dir: dir, stack: map[string]int{}}
	return g.proxy(sp, 0)
}

type exampleGen struct {
	dir   Direction
	stack map[string]int // $ref -> times on the current path, for cycle breaking
}

func (g *exampleGen) proxy(sp *base.SchemaProxy, depth int) any {
	if sp == nil || depth > maxDepth {
		return nil
	}
	if ref := refKey(sp); ref != "" {
		if g.stack[ref] > 0 {
			return cycleValue(sp)
		}
		g.stack[ref]++
		defer func() { g.stack[ref]-- }()
	}
	s := sp.Schema()
	if s == nil {
		return nil
	}
	return g.schema(s, depth)
}

// cycleValue returns an empty container for a recursive reference, so a tree
// node example shows "children": [] instead of recursing forever.
func cycleValue(sp *base.SchemaProxy) any {
	s := sp.Schema()
	if s != nil && hasType(s, "array") {
		return []any{}
	}
	return ocyaml.NewMap()
}

func (g *exampleGen) schema(s *base.Schema, depth int) any {
	if v, ok := explicitExample(s); ok {
		return v
	}

	if len(s.AllOf) > 0 {
		return g.allOf(s, depth)
	}
	if len(s.OneOf) > 0 {
		return g.proxy(s.OneOf[0], depth+1)
	}
	if len(s.AnyOf) > 0 {
		return g.proxy(s.AnyOf[0], depth+1)
	}

	switch primaryType(s) {
	case "object":
		return g.object(s, depth)
	case "array":
		if s.Items != nil && s.Items.IsA() {
			if k := refKey(s.Items.A); k != "" && g.stack[k] > 0 {
				return []any{}
			}
			item := g.proxy(s.Items.A, depth+1)
			if item == nil {
				return []any{}
			}
			return []any{item}
		}
		return []any{}
	case "string":
		return stringExample(s)
	case "integer":
		if s.Minimum != nil {
			return int64(*s.Minimum)
		}
		return int64(0)
	case "number":
		if s.Minimum != nil {
			return *s.Minimum
		}
		return float64(0)
	case "boolean":
		return true
	case "null":
		return nil
	}
	return nil
}

func (g *exampleGen) object(s *base.Schema, depth int) any {
	out := ocyaml.NewMap()
	if s.Properties != nil {
		for name, prop := range s.Properties.FromOldest() {
			ps := prop.Schema()
			if ps != nil {
				if g.dir == DirRequest && ps.ReadOnly != nil && *ps.ReadOnly {
					continue
				}
				if g.dir == DirResponse && ps.WriteOnly != nil && *ps.WriteOnly {
					continue
				}
			}
			out.Set(name, g.proxy(prop, depth+1))
		}
	}
	if out.Len() == 0 && s.AdditionalProperties != nil && s.AdditionalProperties.IsA() {
		out.Set("additionalProp1", g.proxy(s.AdditionalProperties.A, depth+1))
	}
	return out
}

// allOf merges the object parts in order. Non object parts win as a whole.
func (g *exampleGen) allOf(s *base.Schema, depth int) any {
	out := ocyaml.NewMap()
	var scalar any
	for _, part := range s.AllOf {
		v := g.proxy(part, depth+1)
		if m, ok := v.(*ocyaml.Map); ok {
			for _, k := range m.Keys() {
				out.Set(k, m.Value(k))
			}
		} else if v != nil {
			scalar = v
		}
	}
	// Properties declared next to allOf also count.
	if s.Properties != nil && s.Properties.Len() > 0 {
		if m, ok := g.object(s, depth).(*ocyaml.Map); ok {
			for _, k := range m.Keys() {
				out.Set(k, m.Value(k))
			}
		}
	}
	if out.Len() == 0 && scalar != nil {
		return scalar
	}
	return out
}

func explicitExample(s *base.Schema) (any, bool) {
	if s.Example != nil {
		return NodeValue(s.Example), true
	}
	if len(s.Examples) > 0 && s.Examples[0] != nil {
		return NodeValue(s.Examples[0]), true
	}
	if s.Default != nil {
		return NodeValue(s.Default), true
	}
	if s.Const != nil {
		return NodeValue(s.Const), true
	}
	for _, e := range s.Enum {
		if e != nil && e.Tag != "!!null" {
			return NodeValue(e), true
		}
	}
	return nil, false
}

func primaryType(s *base.Schema) string {
	for _, t := range s.Type {
		if t != "null" {
			return t
		}
	}
	if len(s.Type) == 1 {
		return "null"
	}
	switch {
	case s.Properties != nil && s.Properties.Len() > 0, s.AdditionalProperties != nil:
		return "object"
	case s.Items != nil:
		return "array"
	case s.Format != "":
		return "string"
	}
	return ""
}

func hasType(s *base.Schema, t string) bool {
	for _, x := range s.Type {
		if x == t {
			return true
		}
	}
	return t == "array" && len(s.Type) == 0 && s.Items != nil
}

// stringExample uses the same placeholders as Swagger UI where one exists, so
// the examples look familiar.
func stringExample(s *base.Schema) string {
	switch strings.ToLower(s.Format) {
	case "date-time":
		return "2024-01-01T00:00:00Z"
	case "date":
		return "2024-01-01"
	case "time":
		return "12:00:00"
	case "duration":
		return "PT1H"
	case "uuid":
		return "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	case "email":
		return "user@example.com"
	case "uri", "url", "iri":
		return "https://example.com"
	case "hostname":
		return "example.com"
	case "ipv4":
		return "192.168.0.1"
	case "ipv6":
		return "::1"
	case "byte":
		return "U3dhZ2dlciByb2Nrcw=="
	case "binary":
		return ""
	case "password":
		return "secret"
	}
	return "string"
}

// NodeValue converts a YAML node from the spec into a generic ordered value.
func NodeValue(n *yaml.Node) any {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) > 0 {
			return NodeValue(n.Content[0])
		}
		return nil
	case yaml.AliasNode:
		return NodeValue(n.Alias)
	case yaml.MappingNode:
		m := ocyaml.NewMap()
		for i := 0; i+1 < len(n.Content); i += 2 {
			m.Set(n.Content[i].Value, NodeValue(n.Content[i+1]))
		}
		return m
	case yaml.SequenceNode:
		l := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			l = append(l, NodeValue(c))
		}
		return l
	case yaml.ScalarNode:
		return ocyaml.ScalarFromTag(n.ShortTag(), n.Value)
	}
	return nil
}

// TextValue renders an example value as the text a user would type into a
// parameter or form field.
func TextValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case *ocyaml.Map, []any:
		return ocyaml.JSON(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ocyaml.Scalar(t)
	}
}

// refKey identifies the schema a reference points to. The same schema can be
// reached as "../common/openapi.yml#/components/schemas/Node" from one file
// and "#/components/schemas/Node" from inside common, so the reference string
// alone does not detect the cycle. The resolved YAML node does.
func refKey(sp *base.SchemaProxy) string {
	if !sp.IsReference() {
		return ""
	}
	if n := sp.GetValueNode(); n != nil {
		return fmt.Sprintf("node:%p", n)
	}
	return sp.GetReference()
}
