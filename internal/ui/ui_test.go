package ui

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/itsbryanman/RCLIFT/internal/jobs"
	"github.com/itsbryanman/RCLIFT/internal/operation"
	"github.com/itsbryanman/RCLIFT/internal/rclone"
)

type fakeBrowser map[string][]rclone.Entry

func (f fakeBrowser) Remotes(context.Context) ([]string, error) { return []string{"gd:", "nas:"}, nil }
func (f fakeBrowser) List(_ context.Context, loc string) ([]rclone.Entry, error) {
	return f[loc], nil
}

var tree = fakeBrowser{
	"gd:":        {{Name: "photos", IsDir: true}, {Name: "notes.txt", Size: 2048}},
	"gd:photos":  {{Name: "2024", IsDir: true}},
	"nas:":       {{Name: "backup", IsDir: true}},
	"nas:backup": {},
}

// blockRunner stands in for rclone and only returns once its context ends.
func blockRunner(ctx context.Context, _ []string, _ func(string)) error {
	<-ctx.Done()
	return ctx.Err()
}

func newModel(t *testing.T) Model {
	t.Helper()
	m := New(tree, jobs.New(1, blockRunner), "/home/me", "", "")
	m.w, m.h = 100, 30
	for side := range m.panes {
		m = apply(t, m, m.load(side))
	}
	return m
}

// apply runs a command and feeds its message back into the model, the way
// Bubble Tea would, skipping the periodic tick.
func apply(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			m = apply(t, m, c)
		}
	case listedMsg:
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	return m
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, cmd := m.Update(msg)
		m = apply(t, next.(Model), cmd)
	}
	return m
}

func TestPickerListsLocalAndRemotes(t *testing.T) {
	m := newModel(t)
	var names []string
	for _, e := range m.panes[0].entries {
		names = append(names, e.Name)
	}
	if got := strings.Join(names, ","); got != "/home/me,gd:,nas:" {
		t.Fatalf("picker = %s", got)
	}
}

func TestNavigateAndBack(t *testing.T) {
	m := press(t, newModel(t), "j", "enter", "enter")
	if m.panes[0].loc != "gd:photos" {
		t.Fatalf("loc = %q", m.panes[0].loc)
	}
	m = press(t, m, "backspace")
	if e, _ := m.panes[0].selected(); m.panes[0].loc != "gd:" || e.Name != "photos" {
		t.Fatalf("after back: loc=%q selected=%q", m.panes[0].loc, e.Name)
	}
	m = press(t, m, "backspace")
	if e, _ := m.panes[0].selected(); m.panes[0].loc != "" || e.Name != "gd:" {
		t.Fatalf("back at picker should land on gd:, got loc=%q selected=%q", m.panes[0].loc, e.Name)
	}
}

func TestStaleListingIgnored(t *testing.T) {
	m := newModel(t)
	m.panes[0].loc = "nas:"
	next, _ := m.Update(listedMsg{side: 0, loc: "gd:", entries: tree["gd:"]})
	if next.(Model).panes[0].entries[0].Name == "photos" {
		t.Fatal("listing for a location we already left was applied")
	}
}

// openBoth puts gd: on the left and nas:backup on the right.
func openBoth(t *testing.T) Model {
	return press(t, newModel(t), "j", "enter", "tab", "j", "j", "enter", "enter", "tab")
}

func TestCopyFolderKeepsName(t *testing.T) {
	m := press(t, openBoth(t), "c")
	if m.plan == nil || m.plan.Source != "gd:photos" || m.plan.Destination != "nas:backup/photos" || m.plan.DryRun {
		t.Fatalf("plan = %+v", m.plan)
	}
	m = press(t, m, "enter")
	if js := m.jm.Jobs(); len(js) != 1 || js[0].Spec.Kind != operation.Copy {
		t.Fatalf("jobs = %+v", js)
	}
}

func TestCopyFileGoesIntoFolder(t *testing.T) {
	m := press(t, openBoth(t), "j", "c")
	if m.plan == nil || m.plan.Source != "gd:notes.txt" || m.plan.Destination != "nas:backup" {
		t.Fatalf("plan = %+v", m.plan)
	}
}

func TestSyncDefaultsToDryRun(t *testing.T) {
	m := press(t, openBoth(t), "s")
	if !m.plan.DryRun {
		t.Fatal("sync should start as a dry run")
	}
	if m = press(t, m, "d"); m.plan.DryRun {
		t.Fatal("d should toggle dry run off")
	}
	if !strings.Contains(m.View(), "Sync deletes") {
		t.Error("confirmation is missing the sync warning")
	}
}

func TestSyncRefusesSingleFile(t *testing.T) {
	m := press(t, openBoth(t), "j", "s")
	if m.plan != nil || !strings.Contains(m.status, "folders") {
		t.Fatalf("plan=%+v status=%q", m.plan, m.status)
	}
}

func TestInvalidPlanCannotRun(t *testing.T) {
	m := press(t, newModel(t), "j", "enter", "tab", "j", "enter", "tab")
	// both panes on gd:, so copying photos lands on itself
	m = press(t, m, "c")
	if m.planErr == nil {
		t.Fatal("expected a validation error")
	}
	m = press(t, m, "enter")
	if len(m.jm.Jobs()) != 0 {
		t.Fatal("an invalid plan was started")
	}
	if m = press(t, m, "esc"); m.plan != nil {
		t.Fatal("esc should drop the plan")
	}
}

func TestQuitAsksTwiceWhileBusy(t *testing.T) {
	m := press(t, openBoth(t), "c", "enter")
	for deadline := time.Now().Add(time.Second); !m.busy() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd != nil {
		t.Fatal("quit on first q with a running transfer")
	}
	if _, cmd = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd == nil {
		t.Fatal("second q should quit")
	}
	m.jm.Cancel(1)
}

func TestCancelFromJobList(t *testing.T) {
	m := press(t, openBoth(t), "c", "enter", "J", "x")
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if m.jm.Jobs()[0].Status == jobs.Cancelled {
			return
		}
	}
	t.Fatalf("job not cancelled: %+v", m.jm.Jobs()[0])
}

func TestLastLine(t *testing.T) {
	out := "2026/10/27 18:00:00 NOTICE: 1 MiB / 4 MiB, 25%, 1 MiB/s, ETA 3s\r2026/10/27 18:00:01 NOTICE: 2 MiB / 4 MiB, 50%, 1 MiB/s, ETA 2s\n\n"
	if got := lastLine(out); got != "2 MiB / 4 MiB, 50%, 1 MiB/s, ETA 2s" {
		t.Fatalf("lastLine = %q", got)
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 2048: "2.0 KiB", 5 << 30: "5.0 GiB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestViewFitsSmallTerminal(t *testing.T) {
	m := newModel(t)
	m.w, m.h = 40, 12
	for _, line := range strings.Split(m.View(), "\n") {
		if w := len([]rune(stripANSI(line))); w > 40 {
			t.Fatalf("line wider than terminal (%d): %q", w, stripANSI(line))
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			in = true
		case in && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestTruncateLeft(t *testing.T) {
	if got := truncateLeft("gdrive:very/long/path/photos", 12); got != "…path/photos" {
		t.Fatalf("got %q", got)
	}
	if got := truncateLeft("gd:x", 12); got != "gd:x" {
		t.Fatalf("got %q", got)
	}
}

func TestKeysDuringLoadDontUseOldEntries(t *testing.T) {
	m := press(t, newModel(t), "j", "enter", "tab", "j", "j", "enter", "tab")
	// open photos on the left but don't deliver the listing yet
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.panes[0].loc != "gd:photos" {
		t.Fatalf("loc = %q", m.panes[0].loc)
	}
	m = press(t, m, "c")
	if m.plan != nil {
		t.Fatalf("planned %s → %s from a stale listing", m.plan.Source, m.plan.Destination)
	}
}

func TestStartsLoadingNotEmpty(t *testing.T) {
	m := New(tree, jobs.New(1, blockRunner), "/home/me", "gd:", "")
	m.w, m.h = 100, 30
	if v := m.View(); !strings.Contains(v, "loading") || strings.Contains(v, "(empty)") {
		t.Fatal("first frame should say loading, not empty")
	}
}

func TestCursorStaysPutInEmptyFolder(t *testing.T) {
	m := press(t, openBoth(t), "tab", "j", "j")
	if m.panes[1].cursor != 0 {
		t.Fatalf("cursor = %d in an empty folder", m.panes[1].cursor)
	}
}

func TestBatchedKeysAreAllHandled(t *testing.T) {
	m := newModel(t)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("jj")})
	m = apply(t, next.(Model), cmd)
	if e, _ := m.panes[0].selected(); e.Name != "nas:" {
		t.Fatalf("after \"jj\" selected %q, want nas:", e.Name)
	}
}

func TestControlCharactersInNamesAreNeutralised(t *testing.T) {
	evil := fakeBrowser{"gd:": {{Name: "x\x1b]0;pwned\x07\x1b[2J.txt", Size: 1}}}
	m := New(evil, jobs.New(1, blockRunner), "/home/me", "gd:", "")
	m.w, m.h = 100, 30
	m = apply(t, m, m.load(0))
	if strings.Contains(m.View(), "]0;pwned") && strings.Contains(m.View(), "\x07") {
		t.Fatal("OSC sequence from a file name reached the terminal")
	}
	if !strings.Contains(m.View(), "x?]0;pwned??[2J.txt") {
		t.Fatalf("expected escaped name in view")
	}
}

func TestProgress(t *testing.T) {
	line := "25.027 MiB / 61.035 MiB, 41%, 5.000 MiB/s, ETA 7s"
	if got, _ := progress(line, true); got != "41% · 5.000 MiB/s · ETA 7s" {
		t.Errorf("running: %q", got)
	}
	if got, _ := progress(line, false); got != "25.027 MiB" {
		t.Errorf("finished: %q", got)
	}
	if _, ok := progress("Failed to copy: directory not found", true); ok {
		t.Error("an error line was parsed as progress")
	}
}

func TestProgressLineCannotCarryEscapes(t *testing.T) {
	j := jobs.Job{ID: 1, Status: jobs.Running, Spec: operation.Spec{Kind: operation.Copy, Source: "a:", Destination: "b:"},
		Output: "1 MiB / 2 MiB, 50%, 1 \x1b]0;pwned\x07MiB/s, ETA \x1b[2J5s\n"}
	if line := viewJob(j, 200, false); strings.ContainsAny(stripStyles(line), "\x1b\x07") {
		t.Fatalf("escape reached the job line: %q", line)
	}
}

// stripStyles removes the SGR colour codes lipgloss adds, leaving anything else.
func stripStyles(s string) string { return sgr.ReplaceAllString(s, "") }

var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")
