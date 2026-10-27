// Package rclone provides a small, shell-free adapter to the rclone binary.
package rclone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Adapter struct{ Binary string }
type Entry struct {
	Path    string
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

func New(binary string) *Adapter {
	if binary == "" {
		binary = "rclone"
	}
	return &Adapter{Binary: binary}
}
func (a *Adapter) command(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, a.Binary, args...)
}
func (a *Adapter) Version(ctx context.Context) (string, error) {
	b, e := a.command(ctx, "version").Output()
	return strings.TrimSpace(strings.Split(string(b), "\n")[0]), e
}
func (a *Adapter) Remotes(ctx context.Context) ([]string, error) {
	b, e := a.command(ctx, "listremotes").Output()
	if e != nil {
		return nil, explain(e)
	}
	lines := strings.Fields(string(b))
	sort.Strings(lines)
	return lines, nil
}
func (a *Adapter) List(ctx context.Context, location string) ([]Entry, error) {
	b, e := a.command(ctx, "lsjson", "--dirs-only=false", location).Output()
	if e != nil {
		return nil, fmt.Errorf("list %q: %w", location, explain(e))
	}
	var raw []struct {
		Path, Name string
		Size       int64
		ModTime    time.Time
		IsDir      bool
	}
	if e = json.Unmarshal(b, &raw); e != nil {
		return nil, e
	}
	out := make([]Entry, len(raw))
	for i, x := range raw {
		out[i] = Entry(x)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// explain swaps a bare "exit status 1" for the last thing rclone printed,
// which is usually the actual reason (bad token, missing directory, ...).
func explain(e error) error {
	var x *exec.ExitError
	if !errors.As(e, &x) {
		return e
	}
	lines := strings.Split(strings.TrimSpace(string(x.Stderr)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if i := strings.Index(last, "last error was: "); i >= 0 {
		last = last[i+len("last error was: "):]
	}
	if last == "" {
		return e
	}
	return errors.New(last)
}
func Join(base, name string) string {
	if strings.HasSuffix(base, ":") {
		return base + name
	}
	if strings.Contains(base, ":") {
		return strings.TrimSuffix(base, "/") + "/" + name
	}
	return filepath.Join(base, name)
}
func Parent(path string) string {
	if strings.HasSuffix(path, ":") {
		return path
	}
	if i := strings.Index(path, ":"); i >= 0 {
		p := strings.TrimSuffix(path, "/")
		if j := strings.LastIndex(p, "/"); j > i {
			return p[:j]
		}
		return p[:i+1]
	}
	return filepath.Dir(path)
}
func (a *Adapter) Run(ctx context.Context, args []string, output func(string)) error {
	c := a.command(ctx, args...)
	pipe, e := c.StderrPipe()
	if e != nil {
		return e
	}
	if e = c.Start(); e != nil {
		return e
	}
	buf := make([]byte, 4096)
	for {
		n, er := pipe.Read(buf)
		if n > 0 {
			output(string(buf[:n]))
		}
		if er != nil {
			break
		}
	}
	return c.Wait()
}
