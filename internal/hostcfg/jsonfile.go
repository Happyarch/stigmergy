// Package hostcfg writes stigmergy into the host agents' configuration files.
//
// These are the user's files, and they were there first. Every writer in this
// package merges: it adds what stigmergy needs, leaves everything else exactly
// as it found it, and can remove precisely what it added. A config writer that
// overwrites is a config writer that eventually eats someone's work.
package hostcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Object is a decoded JSON object, kept as a map so unknown keys survive a
// read-modify-write cycle untouched.
type Object map[string]any

// ReadJSON loads a JSON object, returning an empty one if the file is absent.
//
// A malformed file is an error, never something to overwrite: it is far more
// likely to be a config someone is midway through editing than junk to discard.
func ReadJSON(path string) (Object, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Object{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return Object{}, nil
	}
	var obj Object
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%w) — fix or move it, and re-run; stigmergy will not overwrite it", path, err)
	}
	return obj, nil
}

// WriteJSON writes an object back, creating parent directories as needed.
func WriteJSON(path string, obj Object) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o644)
}

// writeFileAtomic replaces a file in one step, so an interrupted write cannot
// leave a host with a half-written config it then refuses to start with.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".stigmergy-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// mapAt returns the nested object at key, creating it if absent.
func (o Object) mapAt(key string) Object {
	if existing, ok := o[key].(map[string]any); ok {
		return Object(existing)
	}
	child := Object{}
	o[key] = map[string]any(child)
	return child
}

// arrayAt returns the array at key.
func (o Object) arrayAt(key string) []any {
	if existing, ok := o[key].([]any); ok {
		return existing
	}
	return nil
}
