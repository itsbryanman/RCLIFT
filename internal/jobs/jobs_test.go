package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/itsbryanman/RCLIFT/internal/operation"
)

var spec = operation.Spec{Kind: operation.Copy, Source: "a:", Destination: "b:"}

func waitFor(t *testing.T, m *Manager, id int, want Status) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, j := range m.Jobs() {
			if j.ID == id && j.Status == want {
				return j
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %d never reached %s; jobs: %+v", id, want, m.Jobs())
	return Job{}
}

func TestDoneAndFailed(t *testing.T) {
	m := New(2, func(_ context.Context, args []string, out func(string)) error {
		out("hello")
		if args[1] == "bad:" {
			return errors.New("boom")
		}
		return nil
	})
	ok := m.Start(spec)
	bad := m.Start(operation.Spec{Kind: operation.Copy, Source: "bad:", Destination: "b:"})
	if j := waitFor(t, m, ok.ID, Done); j.Output != "hello" {
		t.Errorf("output = %q", j.Output)
	}
	waitFor(t, m, bad.ID, Failed)
}

func TestCancelRunning(t *testing.T) {
	m := New(1, func(ctx context.Context, _ []string, _ func(string)) error {
		<-ctx.Done()
		return ctx.Err()
	})
	j := m.Start(spec)
	waitFor(t, m, j.ID, Running)
	m.Cancel(j.ID)
	waitFor(t, m, j.ID, Cancelled)
}

func TestCancelQueued(t *testing.T) {
	release := make(chan struct{})
	m := New(1, func(ctx context.Context, _ []string, _ func(string)) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	first := m.Start(spec)
	waitFor(t, m, first.ID, Running)
	second := m.Start(spec)
	m.Cancel(second.ID)
	close(release)
	waitFor(t, m, first.ID, Done)
	waitFor(t, m, second.ID, Cancelled)
}

func TestOutputIsCapped(t *testing.T) {
	big := make([]byte, 20000)
	for i := range big {
		big[i] = 'x'
	}
	m := New(1, func(_ context.Context, _ []string, out func(string)) error {
		out(string(big))
		return nil
	})
	j := waitFor(t, m, m.Start(spec).ID, Done)
	if len(j.Output) > 8000 {
		t.Errorf("output not capped: %d bytes", len(j.Output))
	}
}
