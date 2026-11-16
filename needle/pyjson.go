package needle

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// This file reproduces the reference serializer byte for byte. The Python
// reference hands the engine json.dumps(schemas) - insertion-ordered keys,
// ", " and ": " separators, and ensure_ascii escaping - and the engine
// renders those exact bytes into the model's prompt. A schema serialized
// any other way (Go's json.Marshal sorts keys alphabetically and strips
// the spaces) changes the prompt the tuned model sees, which measurably
// degrades its tool selection. pyDumps exists so the prompt is identical.

// Key orders mirror how the reference's build_schema inserts them:
// tool name, description, parameters, then whatever the decorator appends
// (triggers); a parameters object is type, properties, required, then
// $defs/definitions; a property is its type, a Literal enum, the
// docstring description, and then the Field constraints in Field.apply
// order. Maps that arrive without an order of their own (raw schema maps
// from callers) are emitted with these same canonical orders so their
// bytes match what an equivalent reference declaration would produce;
// pass a JSON string (WithToolsJSON) for byte-level control.
var (
	canonicalToolKeys     = []string{"name", "description", "parameters", "triggers"}
	canonicalParamKeys    = []string{"type", "properties", "required", "$defs", "definitions"}
	canonicalPropertyKeys = []string{
		"type", "enum", "description",
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		"minLength", "maxLength", "pattern", "format", "minItems", "maxItems",
		"uniqueItems", "default", "const", "items",
	}
)

// pyDumps serializes v the way the reference's json.dumps does: ordered
// objects keep their insertion order, plain maps fall back to the canonical
// schema key orders above (remaining keys sorted), items are joined with
// ", " and keys are followed by ": ", and strings are escaped ASCII-only
// the way ensure_ascii=True does.
func pyDumps(v any) (string, error) {
	var b strings.Builder
	if err := pyEncode(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

func pyEncode(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case *object:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			pyWriteString(b, k)
			b.WriteString(": ")
			if err := pyEncode(b, t.vals[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case map[string]any:
		return pyEncodeMap(b, t)
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := pyEncode(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case []*object:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := pyEncode(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case string:
		pyWriteString(b, t)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case int:
		b.WriteString(strconv.FormatInt(int64(t), 10))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case float64:
		b.WriteString(pyFormatFloat(t))
	default:
		return pyEncodeReflect(b, v)
	}
	return nil
}

// pyEncodeReflect covers the remaining JSON-shaped values schemas can carry
// (typed slices such as []string, other integer widths, nested maps) by
// walking them with reflection.
func pyEncodeReflect(b *strings.Builder, v any) error {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		b.WriteByte('[')
		for i := 0; i < rv.Len(); i++ {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := pyEncode(b, pyValue(rv.Index(i))); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case reflect.Map:
		m := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key, ok := iter.Key().Interface().(string)
			if !ok {
				return fmt.Errorf("needle: cannot serialize tool schema map key of type %T", iter.Key().Interface())
			}
			m[key] = pyValue(iter.Value())
		}
		return pyEncodeMap(b, m)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(rv.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(rv.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		b.WriteString(pyFormatFloat(rv.Float()))
	case reflect.String:
		pyWriteString(b, rv.String())
	case reflect.Bool:
		if rv.Bool() {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			b.WriteString("null")
			return nil
		}
		return pyEncode(b, rv.Elem().Interface())
	default:
		return fmt.Errorf("needle: cannot serialize tool schema value of type %T", v)
	}
	return nil
}

// pyValue unwraps a reflect.Value into a plain any.
func pyValue(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}
	return rv.Interface()
}

// pyEncodeMap emits a plain map in canonical schema order. Which table
// applies is decided by the map's own shape, the same way the reference's
// build_schema decides what to insert first.
func pyEncodeMap(b *strings.Builder, m map[string]any) error {
	var order []string
	switch {
	case m["name"] != nil && m["parameters"] != nil:
		order = canonicalToolKeys
	case m["properties"] != nil || m["required"] != nil:
		order = canonicalParamKeys
	default:
		order = canonicalPropertyKeys
	}
	emitted := map[string]bool{}
	b.WriteByte('{')
	first := true
	emit := func(k string) error {
		v, ok := m[k]
		if !ok {
			return nil
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		emitted[k] = true
		pyWriteString(b, k)
		b.WriteString(": ")
		return pyEncode(b, v)
	}
	for _, k := range order {
		if err := emit(k); err != nil {
			return err
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !emitted[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if err := emit(k); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

// pyWriteString escapes a string the way json.dumps(ensure_ascii=True)
// does: the short escapes for the two mandatory characters and the five
// C escapes, \u00xx for the remaining control characters, and \uXXXX for
// everything non-ASCII (surrogate pairs above the BMP). Unlike Go's
// encoding/json, "<", ">" and "&" are not escaped.
func pyWriteString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				if c < 0x20 {
					fmt.Fprintf(b, `\u%04x`, c)
				} else {
					b.WriteByte(c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(b, `\u%04x`, 0xFFFD)
			i++
			continue
		}
		if r > 0xFFFF {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(b, `\u%04x\u%04x`, hi, lo)
		} else {
			fmt.Fprintf(b, `\u%04x`, r)
		}
		i += size
	}
	b.WriteByte('"')
}

// pyFormatFloat renders a float the way Python's repr does for the values
// that appear in schemas (bounds, defaults): shortest round-trip form,
// without a trailing ".0" for integral values, which json.dumps prints
// as plain integers.
func pyFormatFloat(f float64) string {
	if f == float64(int64(f)) && f > -1e16 && f < 1e16 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
