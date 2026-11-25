package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	legacySharedFile = "drover.json"
	legacyLocalFile  = "drover.local.json"
)

// MigrateRepoFiles imports the settings of a repo that still has drover.json
// or drover.local.json into the project config file, when that file doesn't
// exist yet. drover.json is copied as is and drover.local.json is applied on
// top of it. The repo files are never touched. It returns the project path
// when it wrote it. The result is not validated here: a file that doesn't
// load is reported by the next Load.
func MigrateRepoFiles(root string) (string, error) {
	dest := ProjectPath(root)
	if _, err := os.Stat(dest); err == nil {
		return "", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	shared, err := readOptional(filepath.Join(root, legacySharedFile))
	if err != nil {
		return "", err
	}
	local, err := readOptional(filepath.Join(root, legacyLocalFile))
	if err != nil {
		return "", err
	}
	if shared == nil && local == nil {
		return "", nil
	}
	members, err := parseObject(shared)
	if err != nil {
		return "", fmt.Errorf("migrate %s: %w", legacySharedFile, err)
	}
	localMembers, err := parseObject(local)
	if err != nil {
		return "", fmt.Errorf("migrate %s: %w", legacyLocalFile, err)
	}
	if members, err = applyLegacyLocal(root, members, localMembers); err != nil {
		return "", fmt.Errorf("migrate %s: %w", legacyLocalFile, err)
	}
	data, err := formatFile(members)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := writeFile(dest, data); err != nil {
		return "", err
	}
	return dest, nil
}

// applyLegacyLocal puts the keys of the old drover.local.json into members.
// Column overrides go on the columns the members define, or on the default
// board when they define none; an override of a column that isn't there is
// dropped, as are empty values, as before.
func applyLegacyLocal(root string, members, local []member) ([]member, error) {
	for _, m := range local {
		var err error
		switch m.key {
		case columnsKey:
			members, err = applyLegacyColumns(root, members, m.val)
		case "notifications":
			var notifications map[string]json.RawMessage
			if err = json.Unmarshal(m.val, &notifications); err == nil {
				for key, val := range notifications {
					if members, err = setMember(members, []string{m.key, key}, val); err != nil {
						break
					}
				}
			}
		default:
			if string(m.val) == `""` {
				continue
			}
			members, err = setMember(members, []string{m.key}, m.val)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.key, err)
		}
	}
	return members, nil
}

func applyLegacyColumns(root string, members []member, raw json.RawMessage) ([]member, error) {
	var columns []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &columns); err != nil {
		return nil, err
	}
	for _, col := range columns {
		var id string
		if err := json.Unmarshal(col["id"], &id); err != nil {
			return nil, errors.New("a column needs a string id")
		}
		if !hasColumn(members, id) {
			continue
		}
		var err error
		if findMember(members, columnsKey) < 0 {
			if members, err = seedDefaultBoard(root, members); err != nil {
				return nil, err
			}
		}
		for key, val := range col {
			if key == "id" || string(val) == `""` {
				continue
			}
			if members, err = setMember(members, ColumnPath(id, key), val); err != nil {
				return nil, err
			}
		}
	}
	return members, nil
}

// hasColumn reports whether the board the members define has a column with the
// id; with no columns key that is the default board.
func hasColumn(members []member, id string) bool {
	i := findMember(members, columnsKey)
	if i < 0 {
		_, _, ok := Default().Column(id)
		return ok
	}
	var elems []json.RawMessage
	return json.Unmarshal(members[i].val, &elems) == nil && findElement(elems, id) >= 0
}

func readOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return data, err
}
