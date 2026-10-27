package rclone

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJoin(t *testing.T) {
	cases := [][3]string{
		{"gdrive:", "docs", "gdrive:docs"},
		{"gdrive:docs", "a.txt", "gdrive:docs/a.txt"},
		{"gdrive:docs/", "a.txt", "gdrive:docs/a.txt"},
		{"/home/me", "x", "/home/me/x"},
	}
	for _, c := range cases {
		if got := Join(c[0], c[1]); got != c[2] {
			t.Errorf("Join(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestParent(t *testing.T) {
	cases := [][2]string{
		{"gdrive:", "gdrive:"},
		{"gdrive:docs", "gdrive:"},
		{"gdrive:docs/2024/", "gdrive:docs"},
		{"/home/me", "/home"},
	}
	for _, c := range cases {
		if got := Parent(c[0]); got != c[1] {
			t.Errorf("Parent(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestListErrorShowsRcloneReason(t *testing.T) {
	// a stand-in "rclone" that fails the way a revoked token does
	dir := t.TempDir()
	script := filepath.Join(dir, "rclone")
	body := "#!/bin/sh\necho '2026/10/27 18:30:45 ERROR : : error listing: 403' >&2\necho 'Failed to lsjson with 2 errors: last error was: Bad Authorization: 403 Forbidden' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := New(script).List(context.Background(), "RD:")
	if err == nil || !strings.Contains(err.Error(), "Bad Authorization: 403 Forbidden") || strings.Contains(err.Error(), "exit status") {
		t.Fatalf("err = %v", err)
	}
}

func TestListLocal(t *testing.T) {
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone not installed")
	}
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "b-dir"), 0o755)
	os.WriteFile(filepath.Join(dir, "A.txt"), []byte("hello"), 0o644)
	es, err := New("").List(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 || es[0].Name != "b-dir" || !es[0].IsDir || es[1].Size != 5 {
		t.Fatalf("entries = %+v (want dirs first)", es)
	}
}
