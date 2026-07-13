// Package xdg resolves the XDG base directories stigmergy stores state in.
package xdg

import (
	"os"
	"path/filepath"
)

// DataHome returns $XDG_DATA_HOME, defaulting to ~/.local/share.
func DataHome() (string, error) {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// StateHome returns $XDG_STATE_HOME, defaulting to ~/.local/state.
func StateHome() (string, error) {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// DataDir returns the stigmergy data directory, creating it 0700 if absent.
func DataDir() (string, error) {
	base, err := DataHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "stigmergy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// GlobalDBPath returns the global database path, creating its directory.
func GlobalDBPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "global.sqlite3"), nil
}

// StateDir returns the stigmergy state directory (probe logs and the like),
// creating it 0700 if absent.
func StateDir() (string, error) {
	base, err := StateHome()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "stigmergy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
