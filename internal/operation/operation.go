package operation

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Kind string

const (
	Copy Kind = "copy"
	Move Kind = "move"
	Sync Kind = "sync"
)

type Spec struct {
	Kind                Kind
	Source, Destination string
	DryRun              bool
	BackupDir           string
	MaxDelete           int
}

func (s Spec) Command() []string {
	a := []string{string(s.Kind), s.Source, s.Destination, "--stats=1s", "--stats-one-line", "--stats-log-level=NOTICE"}
	if s.DryRun {
		a = append(a, "--dry-run")
	}
	if s.BackupDir != "" {
		a = append(a, "--backup-dir", s.BackupDir)
	}
	if s.MaxDelete > 0 {
		a = append(a, "--max-delete", fmt.Sprint(s.MaxDelete))
	}
	return a
}
func (s Spec) Validate() error {
	if s.Kind != Copy && s.Kind != Move && s.Kind != Sync {
		return fmt.Errorf("unsupported operation %q", s.Kind)
	}
	if strings.TrimSpace(s.Source) == "" || strings.TrimSpace(s.Destination) == "" {
		return fmt.Errorf("source and destination are required")
	}
	if equal(s.Source, s.Destination) {
		return fmt.Errorf("source and destination are identical")
	}
	if s.Kind == Sync && s.MaxDelete < 0 {
		return fmt.Errorf("max delete cannot be negative")
	}
	// Local nesting is always dangerous. Remote nesting under same remote follows identical rules.
	if nested(s.Source, s.Destination) || nested(s.Destination, s.Source) {
		return fmt.Errorf("source and destination are nested; operation refused")
	}
	return nil
}
func equal(a, b string) bool { return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") }
func nested(a, b string) bool {
	a = strings.TrimSuffix(a, "/")
	b = strings.TrimSuffix(b, "/")
	if strings.Contains(a, ":") || strings.Contains(b, ":") {
		if strings.HasSuffix(a, ":") {
			return strings.HasPrefix(b, a) && b != a
		}
		return strings.HasPrefix(b, a+"/")
	}
	ra, e := filepath.Abs(a)
	if e != nil {
		return false
	}
	rb, e := filepath.Abs(b)
	if e != nil {
		return false
	}
	return strings.HasPrefix(rb, ra+string(filepath.Separator))
}
func (s Spec) Warning() string {
	if s.Kind == Sync {
		return "Sync deletes destination-only files. Review the plan carefully."
	}
	if s.Kind == Move {
		return "Move removes source files after successful transfer."
	}
	return "Copy preserves source files."
}
