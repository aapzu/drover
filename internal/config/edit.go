package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// Layer is one of the two files a setting can live in.
type Layer int

const (
	// Shared is drover.json, which is committed with the repo.
	Shared Layer = iota
	// Local is drover.local.json, for personal overrides.
	Local
)

// ErrNoSharedFile means drover.json has not been written yet, so there is
// nothing for a Shared edit to change.
var ErrNoSharedFile = errors.New(FileName + " does not exist yet")

func (l Layer) FileName() string {
	if l == Local {
		return LocalFileName
	}
	return FileName
}

const columnsKey = "columns"

// ColumnPath addresses a setting of the column with the given id, for Set,
// Unset and Sets.
func ColumnPath(id string, key ...string) []string {
	return append([]string{columnsKey, id}, key...)
}

// Set writes value at path in the layer's file and keeps the other keys in
// their order; the file is re-indented on write. If the result no longer
// loads, the file is restored, or removed if it did not exist, and the error
// is returned.
func Set(root string, layer Layer, path []string, value any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return err
	}
	return edit(root, layer, path, bytes.TrimSpace(buf.Bytes()))
}

// Unset removes path from the layer's file, so the value falls back to the
// other layer or the default.
func Unset(root string, layer Layer, path []string) error {
	return edit(root, layer, path, nil)
}

// Sets reports whether the layer's file sets path itself.
func Sets(root string, layer Layer, path []string) bool {
	data, err := os.ReadFile(filepath.Join(root, layer.FileName()))
	if err != nil {
		return false
	}
	return sets(data, path)
}

func sets(data []byte, path []string) bool {
	members, err := parseObject(data)
	if err != nil || len(path) == 0 {
		return false
	}
	i := findMember(members, path[0])
	switch {
	case i < 0:
		return false
	case len(path) == 1:
		return true
	case path[0] == columnsKey && isArray(members[i].val):
		return setsInColumns(members[i].val, path[1:])
	}
	return sets(members[i].val, path[1:])
}

func setsInColumns(data []byte, path []string) bool {
	var elems []json.RawMessage
	if err := json.Unmarshal(data, &elems); err != nil {
		return false
	}
	j := findElement(elems, path[0])
	return j >= 0 && (len(path) == 1 || sets(elems[j], path[1:]))
}

func edit(root string, layer Layer, path []string, value json.RawMessage) error {
	if layer != Shared && layer != Local {
		return fmt.Errorf("unknown layer %d", layer)
	}
	if len(path) == 0 {
		return errors.New("empty path")
	}
	file := filepath.Join(root, layer.FileName())
	old, err := os.ReadFile(file)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if missing && layer == Shared {
		return ErrNoSharedFile
	}
	if missing && value == nil {
		return nil
	}
	members, err := parseObject(old)
	if err != nil {
		return fmt.Errorf("%s: %w", layer.FileName(), err)
	}
	members, err = setMember(members, path, value, layer == Local)
	if err != nil {
		return fmt.Errorf("%s: %w", layer.FileName(), err)
	}
	data, err := formatFile(members)
	if err != nil {
		return err
	}
	if err := writeFile(file, data); err != nil {
		return err
	}
	if _, _, err := Load(root); err != nil {
		var undo error
		if missing {
			undo = os.Remove(file)
		} else {
			undo = writeFile(file, old)
		}
		if undo != nil {
			return fmt.Errorf("%w (and restoring %s failed: %v)", err, layer.FileName(), undo)
		}
		return err
	}
	return nil
}

func writeFile(path string, data []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// member is one key of a JSON object with its value left as raw bytes.
type member struct {
	key string
	val json.RawMessage
}

// parseObject splits a JSON object into its members. Empty input is an empty object.
func parseObject(data []byte) ([]member, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var members []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("object key is not a string")
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		members = append(members, member{key, val})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return members, nil
}

func encodeObject(members []member) (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(m.val)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// formatFile writes the members with a two-space indent and a trailing
// newline, like Board.Save.
func formatFile(members []member) ([]byte, error) {
	obj, err := encodeObject(members)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, obj, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// setMember sets the value at path, creating parent objects as needed. A nil
// value removes the key, and a parent left empty by that goes too.
func setMember(members []member, path []string, value json.RawMessage, local bool) ([]member, error) {
	i := findMember(members, path[0])
	if len(path) == 1 {
		if value == nil {
			return removeMember(members, i), nil
		}
		return upsert(members, i, path[0], value), nil
	}
	if path[0] == columnsKey && (i < 0 || isArray(members[i].val)) {
		var raw json.RawMessage
		if i >= 0 {
			raw = members[i].val
		}
		elems, err := setColumn(raw, path[1:], value, local)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path[0], err)
		}
		if elems == nil {
			return removeMember(members, i), nil
		}
		return upsert(members, i, path[0], elems), nil
	}
	var child []member
	if i >= 0 {
		var err error
		if child, err = parseObject(members[i].val); err != nil {
			return nil, fmt.Errorf("%s: %w", path[0], err)
		}
	}
	child, err := setMember(child, path[1:], value, local)
	if err != nil {
		return nil, err
	}
	if value == nil && len(child) == 0 {
		return removeMember(members, i), nil
	}
	obj, err := encodeObject(child)
	if err != nil {
		return nil, err
	}
	return upsert(members, i, path[0], obj), nil
}

// setColumn sets the value at path inside the columns element whose id is
// path[0]. drover.local.json elements are overrides, so they are added and
// dropped as keys come and go; a drover.json column must already exist and is
// never dropped.
func setColumn(raw json.RawMessage, path []string, value json.RawMessage, local bool) (json.RawMessage, error) {
	if len(path) < 2 {
		return nil, errors.New("address a setting of the column, not the whole column")
	}
	var elems []json.RawMessage
	if raw != nil {
		if err := json.Unmarshal(raw, &elems); err != nil {
			return nil, err
		}
	}
	j := -1
	for i, e := range elems {
		var el struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(e, &el); err != nil {
			return nil, fmt.Errorf("column %d is not an object with a string id", i)
		}
		if j < 0 && el.ID == path[0] {
			j = i
		}
	}
	var child []member
	switch {
	case j >= 0:
		var err error
		if child, err = parseObject(elems[j]); err != nil {
			return nil, fmt.Errorf("%s: %w", path[0], err)
		}
	case value == nil:
		return raw, nil
	case !local:
		return nil, fmt.Errorf("no column %q", path[0])
	default:
		id, err := json.Marshal(path[0])
		if err != nil {
			return nil, err
		}
		child = []member{{"id", id}}
	}
	child, err := setMember(child, path[1:], value, local)
	if err != nil {
		return nil, err
	}
	if local && value == nil && len(child) <= 1 {
		elems = slices.Delete(elems, j, j+1)
	} else {
		obj, err := encodeObject(child)
		if err != nil {
			return nil, err
		}
		if j >= 0 {
			elems[j] = obj
		} else {
			elems = append(elems, obj)
		}
	}
	if len(elems) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, e := range elems {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(e)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

func isArray(val json.RawMessage) bool {
	return bytes.HasPrefix(bytes.TrimSpace(val), []byte("["))
}

// findElement is the index of the element whose "id" is id, or -1.
func findElement(elems []json.RawMessage, id string) int {
	return slices.IndexFunc(elems, func(e json.RawMessage) bool {
		var el struct {
			ID string `json:"id"`
		}
		return json.Unmarshal(e, &el) == nil && el.ID == id
	})
}

func findMember(members []member, key string) int {
	return slices.IndexFunc(members, func(m member) bool { return m.key == key })
}

func removeMember(members []member, i int) []member {
	if i < 0 {
		return members
	}
	return slices.Delete(members, i, i+1)
}

func upsert(members []member, i int, key string, val json.RawMessage) []member {
	if i >= 0 {
		members[i].val = val
		return members
	}
	return append(members, member{key, val})
}
