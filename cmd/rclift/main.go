// Command rclift is a two-pane terminal front end for rclone.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/itsbryanman/RCLIFT/internal/jobs"
	"github.com/itsbryanman/RCLIFT/internal/rclone"
	"github.com/itsbryanman/RCLIFT/internal/ui"
)

var version = "dev"

func main() {
	bin := flag.String("rclone", "rclone", "path to the rclone binary")
	concurrency := flag.Int("jobs", 2, "how many transfers may run at once")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: rclift [flags] [left-location] [right-location]\n\n")
		fmt.Fprintf(os.Stderr, "Locations are anything rclone understands, e.g. gdrive:photos or /mnt/backup.\n")
		fmt.Fprintf(os.Stderr, "Leave them out to start at the remote picker.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Println("rclift", version)
		return
	}
	if flag.NArg() > 2 {
		flag.Usage()
		os.Exit(2)
	}

	rc := rclone.New(*bin)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err := rc.Version(ctx)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rclift: can't run %q (%v)\nInstall rclone from https://rclone.org/install/ or point -rclone at it.\n", *bin, err)
		os.Exit(1)
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "/"
	}
	jm := jobs.New(*concurrency, rc.Run)
	m := ui.New(rc, jm, cwd, flag.Arg(0), flag.Arg(1))
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "rclift:", err)
		os.Exit(1)
	}
	stopJobs(jm)
}

// stopJobs cancels anything still queued or running and waits a little for
// rclone to exit, so quitting never leaves an orphaned transfer behind.
func stopJobs(jm *jobs.Manager) {
	pending := func() (n int) {
		for _, j := range jm.Jobs() {
			if j.Status == jobs.Running || j.Status == jobs.Queued {
				n++
			}
		}
		return n
	}
	if pending() == 0 {
		return
	}
	for _, j := range jm.Jobs() {
		if j.Status == jobs.Running || j.Status == jobs.Queued {
			jm.Cancel(j.ID)
			fmt.Fprintf(os.Stderr, "rclift: stopping job #%d (%s %s → %s)\n", j.ID, j.Spec.Kind, j.Spec.Source, j.Spec.Destination)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); pending() > 0 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
}
