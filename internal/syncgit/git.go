// Package syncgit runs the git commands used by the manual sync transport.
//
// It must never be imported by the hook path: a git command may wait for a
// credential prompt or network I/O, neither of which belongs in an edit hook.
package syncgit

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes git with the settings that make a sync working copy inert:
// no repository hooks and a deliberately non-user commit identity.
type Runner struct{ Dir string }

var fixed = []string{"-c", "core.hooksPath=/dev/null", "-c", "user.name=stigmergy", "-c", "user.email=stigmergy@localhost"}

func (r Runner) Run(args ...string) error {
	_, err := r.output(args...)
	return err
}

func (r Runner) Output(args ...string) (string, error) { return r.output(args...) }

func (r Runner) output(args ...string) (string, error) {
	argv := append(append([]string{}, fixed...), "-C", r.Dir)
	argv = append(argv, args...)
	cmd := exec.Command("git", argv...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (r Runner) Clone(remote, dir string) error {
	argv := append(append([]string{}, fixed...), "clone", remote, dir)
	cmd := exec.Command("git", argv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (r Runner) Init() error                           { return r.Run("init") }
func (r Runner) RemoteURL(name string) (string, error) { return r.Output("remote", "get-url", name) }
func (r Runner) AddAll() error                         { return r.Run("add", "-A") }
func (r Runner) Commit(message string) error           { return r.Run("commit", "--allow-empty", "-m", message) }
func (r Runner) Fetch() error                          { return r.Run("fetch", "origin") }
func (r Runner) Push() error                           { return r.Run("push", "origin", "HEAD:main") }
