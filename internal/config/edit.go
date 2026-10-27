package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
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

func (l Layer) FileName() string {
	if l == Local {
		return LocalFileName
	}
	return FileName
}

// Set writes value at path in the layer's file and keeps the other keys in
// their order; the file is re-indented on write. If the result no longer
// loads, the file is restored, or removed if it did not exist, and the error
// is returned.
func Set(root string, layer Layer, path []string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return edit(root, layer, path, raw)
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
	members, err := parseObject(data)
	for i, key := range path {
		if err != nil {
			return false
		}
		j := slices.IndexFunc(members, func(m member) bool { return m.key == key })
		if j < 0 {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		members, err = parseObject(members[j].val)
	}
	return false
}

func edit(root string, layer Layer, path []string, value json.RawMessage) error {
	file := filepath.Join(root, layer.FileName())
	old, err := os.ReadFile(file)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return err
	}
	if missing && layer == Shared {
		return fmt.Errorf("%s does not exist yet; press W to write the default board", FileName)
	}
	if missing && value == nil {
		return nil
	}
	members, err := parseObject(old)
	if err != nil {
		return fmt.Errorf("%s: %w", layer.FileName(), err)
	}
	members, err = setMember(members, path, value)
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
		if missing {
			return errors.Join(err, os.Remove(file))
		}
		return errors.Join(err, writeFile(file, old))
	}
	return nil
}

func writeFile(path string, data []byte) error {
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
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
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
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

// formatFile writes the members the way the rest of drover writes JSON.
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
func setMember(members []member, path []string, value json.RawMessage) ([]member, error) {
	i := slices.IndexFunc(members, func(m member) bool { return m.key == path[0] })
	if len(path) == 1 {
		if value == nil {
			if i >= 0 {
				members = slices.Delete(members, i, i+1)
			}
			return members, nil
		}
		return upsert(members, i, path[0], value), nil
	}
	var child []member
	if i >= 0 {
		var err error
		if child, err = parseObject(members[i].val); err != nil {
			return nil, fmt.Errorf("%s: %w", path[0], err)
		}
	}
	child, err := setMember(child, path[1:], value)
	if err != nil {
		return nil, err
	}
	if value == nil && len(child) == 0 {
		if i >= 0 {
			members = slices.Delete(members, i, i+1)
		}
		return members, nil
	}
	obj, err := encodeObject(child)
	if err != nil {
		return nil, err
	}
	return upsert(members, i, path[0], obj), nil
}

func upsert(members []member, i int, key string, val json.RawMessage) []member {
	if i >= 0 {
		members[i].val = val
		return members
	}
	return append(members, member{key, val})
}
