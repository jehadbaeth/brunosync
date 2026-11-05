package ocyaml

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// JSON renders a generic value as pretty printed JSON with two space indent,
// keeping map key order. Used for generated request and response bodies.
func JSON(v any) string {
	var b strings.Builder
	writeJSON(&b, v, 0)
	return b.String()
}

func writeJSON(b *strings.Builder, v any, depth int) {
	indent := strings.Repeat("  ", depth)
	inner := strings.Repeat("  ", depth+1)
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case *Map:
		if t.Len() == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range t.keys {
			b.WriteString(inner)
			b.WriteString(jsonString(k))
			b.WriteString(": ")
			writeJSON(b, t.vals[k], depth+1)
			if i < len(t.keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent)
		b.WriteString("}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range t {
			b.WriteString(inner)
			writeJSON(b, e, depth+1)
			if i < len(t)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString(indent)
		b.WriteString("]")
	case string:
		b.WriteString(jsonString(t))
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case int:
		b.WriteString(strconv.Itoa(t))
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			b.WriteString("0")
			return
		}
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			b.WriteString(strconv.FormatInt(int64(t), 10))
			return
		}
		b.WriteString(strconv.FormatFloat(t, 'g', -1, 64))
	default:
		b.WriteString(jsonString(Scalar(t)))
	}
}

func jsonString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}
