// Package omap is a JSON object that keeps its keys in insertion order, so command output reads
// the same way every time (film simulation first, metadata where people expect it).
package omap

import (
	"bytes"
	"encoding/json"
)

// Map is an ordered JSON object. The zero value is ready to use.
type Map struct {
	keys []string
	vals map[string]any
}

// New returns an empty map.
func New() *Map { return &Map{vals: map[string]any{}} }

// Set adds or replaces a key; a new key goes last.
func (m *Map) Set(key string, value any) *Map {
	if m.vals == nil {
		m.vals = map[string]any{}
	}
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = value
	return m
}

// Get returns a value and whether it is present.
func (m *Map) Get(key string) (any, bool) {
	if m == nil || m.vals == nil {
		return nil, false
	}
	v, ok := m.vals[key]
	return v, ok
}

// Delete removes a key.
func (m *Map) Delete(key string) {
	if m == nil || m.vals == nil {
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

// Keys returns the keys in order.
func (m *Map) Keys() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.keys...)
}

// Len is the number of keys.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.keys)
}

// MarshalJSON writes the object with keys in insertion order.
func (m *Map) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	if m != nil {
		for i, k := range m.keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			buf.Write(kb)
			buf.WriteByte(':')
			vb, err := marshal(m.vals[k])
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// ToPlain converts the map (recursively) to map[string]any, losing order.
func (m *Map) ToPlain() map[string]any {
	out := map[string]any{}
	if m == nil {
		return out
	}
	for _, k := range m.keys {
		out[k] = plain(m.vals[k])
	}
	return out
}

func plain(v any) any {
	switch x := v.(type) {
	case *Map:
		return x.ToPlain()
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = plain(e)
		}
		return out
	default:
		return v
	}
}

// Indent renders any value as indented JSON (two spaces) without HTML escaping.
func Indent(v any) ([]byte, error) {
	raw, err := marshal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
