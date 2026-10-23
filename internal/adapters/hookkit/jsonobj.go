// Package hookkit holds what the Claude Code and Codex adapters share:
// editing Claude-style hook configs (both agents use the same JSON shape),
// writing settings files safely, diffs for `--dry-run`, shell quoting and
// the one-line summaries shown to people.
package hookkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// member is one key of a JSON object. Values stay raw so everything we do
// not own round-trips byte for byte (modulo re-indentation).
type member struct {
	key string
	val json.RawMessage
}

// object is a JSON object that remembers key order: users diff their
// settings files, and reordering keys would make every install noisy.
type object []member

func parseObject(raw []byte) (object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	var obj object
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("malformed object key")
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		obj = append(obj, member{key, val})
	}
	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the top-level object")
	}
	return obj, nil
}

func parseArray(raw []byte) ([]json.RawMessage, error) {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, err
	}
	return arr, nil
}

func (o object) index(key string) int {
	for i, m := range o {
		if m.key == key {
			return i
		}
	}
	return -1
}

func (o object) get(key string) (json.RawMessage, bool) {
	if i := o.index(key); i >= 0 {
		return o[i].val, true
	}
	return nil, false
}

// set replaces key in place, or appends it.
func (o object) set(key string, val json.RawMessage) object {
	if i := o.index(key); i >= 0 {
		o[i].val = val
		return o
	}
	return append(o, member{key, val})
}

func (o object) without(key string) object {
	i := o.index(key)
	if i < 0 {
		return o
	}
	return append(o[:i:i], o[i+1:]...)
}

func (o object) marshal() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(marshalString(m.key))
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func marshalArray(items []json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// marshalString encodes s without HTML escaping: settings files are read
// by people, and ">" in a command path helps nobody.
func marshalString(s string) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // strings always encode
	return bytes.TrimRight(b.Bytes(), "\n")
}

func marshalInt(n int) json.RawMessage { return json.RawMessage(fmt.Sprint(n)) }

// format pretty-prints doc with the indentation the original file used,
// so a settings file Claude Code wrote with two spaces keeps two spaces.
func format(doc json.RawMessage, original []byte) []byte {
	var b bytes.Buffer
	if err := json.Indent(&b, doc, "", detectIndent(original)); err != nil {
		return append([]byte(nil), doc...)
	}
	b.WriteByte('\n')
	return b.Bytes()
}

func detectIndent(src []byte) string {
	for _, line := range bytes.Split(src, []byte("\n"))[1:] {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 || len(trimmed) == len(line) {
			continue
		}
		return string(line[:len(line)-len(trimmed)])
	}
	return "  "
}
