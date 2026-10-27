package operation

import (
	"slices"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{"plain copy", Spec{Kind: Copy, Source: "gdrive:photos", Destination: "/mnt/backup/photos"}, ""},
		{"different remotes", Spec{Kind: Sync, Source: "a:x", Destination: "b:x/y"}, ""},
		{"sibling prefix is not nesting", Spec{Kind: Copy, Source: "r:foo", Destination: "r:foobar"}, ""},
		{"unknown kind", Spec{Kind: "delete", Source: "a:", Destination: "b:"}, "unsupported"},
		{"missing dest", Spec{Kind: Copy, Source: "a:"}, "required"},
		{"identical", Spec{Kind: Copy, Source: "r:dir/", Destination: "r:dir"}, "identical"},
		{"nested remote", Spec{Kind: Sync, Source: "r:dir", Destination: "r:dir/sub"}, "nested"},
		{"nested remote reversed", Spec{Kind: Sync, Source: "r:dir/sub", Destination: "r:dir"}, "nested"},
		{"remote root into subdir", Spec{Kind: Sync, Source: "r:", Destination: "r:backup"}, "nested"},
		{"subdir onto whole remote", Spec{Kind: Sync, Source: "gdrive:backup", Destination: "gdrive:"}, "nested"},
		{"whole remote spelled with slash", Spec{Kind: Sync, Source: "gdrive:backup", Destination: "gdrive:/"}, "nested"},
		{"whole remote onto itself", Spec{Kind: Sync, Source: "gdrive:", Destination: "gdrive:/"}, "identical"},
		{"whole remote to another remote", Spec{Kind: Sync, Source: "gdrive:", Destination: "storagebox:gdrive"}, ""},
		{"flag-looking source", Spec{Kind: Copy, Source: "--delete-before", Destination: "b:"}, "may not start"},
		{"local dir with colon is local", Spec{Kind: Sync, Source: "/mnt/a:b", Destination: "/mnt/a:b/c"}, "nested"},
		{"nested local", Spec{Kind: Move, Source: "/data", Destination: "/data/old"}, "nested"},
		{"negative max delete", Spec{Kind: Sync, Source: "a:", Destination: "b:", MaxDelete: -1}, "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestCommand(t *testing.T) {
	got := Spec{Kind: Sync, Source: "a:", Destination: "b:", DryRun: true, BackupDir: "b:.trash", MaxDelete: 10}.Command()
	for _, want := range []string{"sync", "--dry-run", "--backup-dir", "b:.trash", "--max-delete", "10"} {
		if !slices.Contains(got, want) {
			t.Errorf("command %v missing %q", got, want)
		}
	}
	if n := len(got); got[n-3] != "--" || got[n-2] != "a:" || got[n-1] != "b:" {
		t.Errorf("paths must come last, after --: %v", got)
	}
	if slices.Contains(Spec{Kind: Copy, Source: "a:", Destination: "b:"}.Command(), "--dry-run") {
		t.Error("dry-run flag present when not requested")
	}
}
