// Package ui is the terminal front end: two browsable panes and a job list.
package ui

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/itsbryanman/RCLIFT/internal/jobs"
	"github.com/itsbryanman/RCLIFT/internal/operation"
	"github.com/itsbryanman/RCLIFT/internal/rclone"
)

// Browser is the part of the rclone adapter the UI needs for navigation.
type Browser interface {
	Remotes(ctx context.Context) ([]string, error)
	List(ctx context.Context, location string) ([]rclone.Entry, error)
}

// pane is one side of the screen. An empty loc means the remote picker.
type pane struct {
	loc     string
	entries []rclone.Entry
	cursor  int
	offset  int
	focus   string // entry to put the cursor on once the listing arrives
	shown   string // location the current entries belong to
	loading bool
	err     error
}

func (p *pane) selected() (rclone.Entry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.entries) {
		return rclone.Entry{}, false
	}
	return p.entries[p.cursor], true
}

// scroll keeps the cursor inside the visible window of rows.
func (p *pane) scroll(rows int) {
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
}

type listedMsg struct {
	side    int
	loc     string
	entries []rclone.Entry
	err     error
}

type tickMsg struct{}

type Model struct {
	br    Browser
	jm    *jobs.Manager
	local string // local directory offered in the remote picker

	panes  [2]pane
	active int

	plan    *operation.Spec // operation waiting for confirmation
	planErr error

	jobFocus  bool
	jobCursor int
	finished  int

	status    string
	quitArmed bool // q pressed once while transfers were running
	w, h      int
}

// New builds a model. left and right are optional starting locations.
func New(br Browser, jm *jobs.Manager, local, left, right string) Model {
	m := Model{br: br, jm: jm, local: local}
	m.panes[0].loc, m.panes[1].loc = left, right
	// Init runs on a copy, so mark the first listings as in flight here
	m.panes[0].loading, m.panes[1].loading = true, true
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.load(0), m.load(1), tick())
}

func tick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) load(side int) tea.Cmd {
	p := &m.panes[side]
	p.loading, p.err = true, nil
	if p.shown != p.loc {
		// never let a keypress act on entries from the folder we just left
		p.entries, p.cursor, p.offset = nil, 0, 0
	}
	loc, br, local := p.loc, m.br, m.local
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute) // Drive can take most of a minute on a cold listing
		defer cancel()
		if loc == "" {
			es := []rclone.Entry{{Name: local, Path: local, IsDir: true}}
			rs, err := br.Remotes(ctx)
			for _, r := range rs {
				es = append(es, rclone.Entry{Name: r, Path: r, IsDir: true})
			}
			return listedMsg{side, loc, es, err}
		}
		es, err := br.List(ctx, loc)
		return listedMsg{side, loc, es, err}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case listedMsg:
		p := &m.panes[msg.side]
		if msg.loc != p.loc {
			return m, nil // the user already moved on
		}
		p.loading, p.err, p.entries, p.shown = false, msg.err, msg.entries, msg.loc
		p.cursor, p.offset = 0, 0
		for i, e := range p.entries {
			if p.focus != "" && (e.Name == p.focus || e.Path == p.focus) {
				p.cursor = i
			}
		}
		p.focus = ""
	case tickMsg:
		return m, tea.Batch(tick(), m.refreshAfterJobs())
	case tea.KeyMsg:
		// Keys typed faster than the terminal is read arrive as one event
		// ("jc"); handle them one at a time instead of dropping them.
		if msg.Type == tea.KeyRunes && len(msg.Runes) > 1 && !msg.Paste {
			var cmds []tea.Cmd
			var next tea.Model = m
			for _, r := range msg.Runes {
				var cmd tea.Cmd
				next, cmd = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
				cmds = append(cmds, cmd)
			}
			return next, tea.Sequence(cmds...)
		}
		next, cmd := m.key(msg)
		nm := next.(Model)
		for side := range nm.panes {
			nm.panes[side].scroll(nm.listHeight())
		}
		return nm, cmd
	}
	return m, nil
}

// refreshAfterJobs reloads both panes whenever another job has finished,
// so copied or deleted files show up without pressing r.
func (m *Model) refreshAfterJobs() tea.Cmd {
	n := 0
	for _, j := range m.jm.Jobs() {
		if j.Status != jobs.Queued && j.Status != jobs.Running {
			n++
		}
	}
	if n == m.finished {
		return nil
	}
	m.finished = n
	return m.reloadKeepingCursor()
}

func (m *Model) reloadKeepingCursor() tea.Cmd {
	var cmds []tea.Cmd
	for side := range m.panes {
		if e, ok := m.panes[side].selected(); ok {
			m.panes[side].focus = e.Name
		}
		cmds = append(cmds, m.load(side))
	}
	return tea.Batch(cmds...)
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.plan != nil {
		return m.confirmKey(k)
	}
	if m.jobFocus {
		return m.jobKey(k)
	}
	m.status = ""
	armed := m.quitArmed
	m.quitArmed = false
	p := &m.panes[m.active]
	switch k.String() {
	case "q":
		if armed || !m.busy() {
			return m, tea.Quit
		}
		m.quitArmed = true
		m.status = "transfers are still running; press q again to stop them and quit"
	case "tab":
		m.active = 1 - m.active
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = max(0, min(len(p.entries)-1, p.cursor+1))
	case "pgup":
		p.cursor = max(0, p.cursor-m.listHeight())
	case "pgdown":
		p.cursor = max(0, min(len(p.entries)-1, p.cursor+m.listHeight()))
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = max(0, len(p.entries)-1)
	case "enter", "right", "l":
		e, ok := p.selected()
		if !ok || !e.IsDir {
			return m, nil
		}
		if p.loc == "" {
			p.loc = e.Path
		} else {
			p.loc = rclone.Join(p.loc, e.Name)
		}
		return m, m.load(m.active)
	case "backspace", "left", "h":
		if p.loc == "" {
			return m, nil
		}
		up := rclone.Parent(p.loc)
		if up == p.loc || strings.HasSuffix(p.loc, ":") {
			p.focus, p.loc = p.loc, ""
		} else {
			p.focus, p.loc = lastElem(p.loc), up
		}
		return m, m.load(m.active)
	case "r":
		return m, m.reloadKeepingCursor()
	case "c":
		m.planOp(operation.Copy)
	case "m":
		m.planOp(operation.Move)
	case "s":
		m.planOp(operation.Sync)
	case "J":
		if len(m.jm.Jobs()) == 0 {
			m.status = "no jobs yet"
		} else {
			m.jobFocus = true
			m.jobCursor = len(m.jm.Jobs()) - 1
		}
	}
	return m, nil
}

func (m Model) busy() bool {
	for _, j := range m.jm.Jobs() {
		if j.Status == jobs.Running || j.Status == jobs.Queued {
			return true
		}
	}
	return false
}

func lastElem(loc string) string {
	loc = strings.TrimSuffix(loc, "/")
	if i := strings.LastIndexAny(loc, "/:"); i >= 0 {
		return loc[i+1:]
	}
	return loc
}

// planOp builds an operation from the highlighted entry in the active pane
// to the folder open in the other pane, and asks for confirmation.
// Folders keep their name on the other side, like a file manager would.
func (m *Model) planOp(kind operation.Kind) {
	src, dst := &m.panes[m.active], &m.panes[1-m.active]
	if src.loc == "" || dst.loc == "" {
		m.status = "open a remote in both panes first"
		return
	}
	e, ok := src.selected()
	if !ok {
		m.status = "nothing selected"
		return
	}
	if kind == operation.Sync && !e.IsDir {
		m.status = "sync works on folders; use copy for a single file"
		return
	}
	spec := operation.Spec{Kind: kind, Source: rclone.Join(src.loc, e.Name), Destination: dst.loc, DryRun: kind == operation.Sync}
	if e.IsDir {
		spec.Destination = rclone.Join(dst.loc, e.Name)
	}
	m.plan, m.planErr = &spec, spec.Validate()
}

func (m Model) confirmKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "n", "q":
		m.plan, m.status = nil, "cancelled"
	case "d":
		m.plan.DryRun = !m.plan.DryRun
	case "enter", "y":
		if m.planErr != nil {
			return m, nil
		}
		j := m.jm.Start(*m.plan)
		m.status = fmt.Sprintf("queued job #%d", j.ID)
		m.plan = nil
	}
	return m, nil
}

func (m Model) jobKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	js := m.jm.Jobs()
	switch k.String() {
	case "esc", "J", "q":
		m.jobFocus = false
	case "up", "k":
		m.jobCursor = max(0, m.jobCursor-1)
	case "down", "j":
		m.jobCursor = max(0, min(len(js)-1, m.jobCursor+1))
	case "x":
		if m.jobCursor < len(js) {
			m.jm.Cancel(js[m.jobCursor].ID)
			m.status = fmt.Sprintf("cancelling job #%d", js[m.jobCursor].ID)
		}
	}
	return m, nil
}

// ---- rendering ----

var (
	accent   = lipgloss.AdaptiveColor{Light: "#0b6bcb", Dark: "#5fafff"}
	dim      = lipgloss.AdaptiveColor{Light: "#6c6c6c", Dark: "#8a8a8a"}
	danger   = lipgloss.AdaptiveColor{Light: "#c4161c", Dark: "#ff6b6b"}
	good     = lipgloss.AdaptiveColor{Light: "#1a7f37", Dark: "#5fd787"}
	warn     = lipgloss.AdaptiveColor{Light: "#9a6700", Dark: "#e3b341"}
	dimStyle = lipgloss.NewStyle().Foreground(dim)
	dirStyle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	selStyle = lipgloss.NewStyle().Reverse(true)
)

const jobRows = 4

func (m Model) listHeight() int {
	// title + pane border(2) + pane header + job box(rows+2) + status line
	return max(3, m.h-1-2-1-(jobRows+2)-1)
}

func (m Model) View() string {
	if m.w == 0 {
		return "starting…"
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(accent).Render("rclift") + dimStyle.Render("  a two-pane front end for rclone")
	var body string
	if m.plan != nil {
		body = lipgloss.Place(m.w, m.listHeight()+3, lipgloss.Center, lipgloss.Center, m.viewPlan())
	} else {
		pw := m.w / 2
		body = lipgloss.JoinHorizontal(lipgloss.Top, m.viewPane(0, pw), m.viewPane(1, m.w-pw))
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, body, m.viewJobs(), m.viewStatus())
}

func (m *Model) viewPane(side, width int) string {
	p := &m.panes[side]
	inner := max(10, width-2)
	rows := m.listHeight()
	p.scroll(rows)
	header := p.loc
	if header == "" {
		header = "remotes"
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Render(truncateLeft(safe(header), inner))}
	switch {
	case p.loading:
		lines = append(lines, dimStyle.Render("loading…"))
	case p.err != nil:
		lines = append(lines, lipgloss.NewStyle().Foreground(danger).Width(inner).Render(safe(p.err.Error())))
	case len(p.entries) == 0:
		lines = append(lines, dimStyle.Render("(empty)"))
	}
	if !p.loading {
		for i := p.offset; i < len(p.entries) && i < p.offset+rows; i++ {
			lines = append(lines, m.viewEntry(p.entries[i], inner, i == p.cursor && side == m.active && !m.jobFocus))
		}
	}
	border := dim
	if side == m.active && !m.jobFocus {
		border = accent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Width(inner).Height(rows + 1).Render(strings.Join(lines, "\n"))
}

func (m Model) viewEntry(e rclone.Entry, width int, selected bool) string {
	name, size := e.Name, ""
	if e.IsDir && !strings.HasSuffix(name, ":") {
		name += "/"
	} else if !e.IsDir {
		size = humanSize(e.Size)
	}
	name = ansi.Truncate(safe(name), max(1, width-len(size)-1), "…")
	gap := strings.Repeat(" ", max(1, width-ansi.StringWidth(name)-len(size)))
	if selected {
		return selStyle.Render(name + gap + size)
	}
	if e.IsDir {
		name = dirStyle.Render(name)
	}
	return name + gap + dimStyle.Render(size)
}

func (m Model) viewPlan() string {
	s := m.plan
	verb := map[operation.Kind]string{operation.Copy: "Copy", operation.Move: "Move", operation.Sync: "Sync"}[s.Kind]
	label := lipgloss.NewStyle().Foreground(dim).Width(6)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", lipgloss.NewStyle().Bold(true).Render(verb))
	fmt.Fprintf(&b, "%s%s\n%s%s\n\n", label.Render("from"), safe(s.Source), label.Render("to"), safe(s.Destination))
	warnColor := warn
	if s.Kind == operation.Copy {
		warnColor = dim
	}
	b.WriteString(lipgloss.NewStyle().Foreground(warnColor).Render(s.Warning()) + "\n")
	dry := "off"
	if s.DryRun {
		dry = "on (nothing will change)"
	}
	fmt.Fprintf(&b, "dry run: %s\n\n", dry)
	b.WriteString(dimStyle.Render(safe("rclone "+strings.Join(s.Command(), " "))) + "\n\n")
	if m.planErr != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(danger).Render("✗ "+safe(m.planErr.Error())) + "\n" + dimStyle.Render("esc to go back"))
	} else {
		b.WriteString(dimStyle.Render("enter run · d toggle dry run · esc cancel"))
	}
	border := accent
	if s.Kind != operation.Copy && !s.DryRun {
		border = danger
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(1, 2).
		Width(min(m.w-4, 90)).Render(b.String())
}

func (m Model) viewJobs() string {
	js := m.jm.Jobs()
	inner := max(10, m.w-2)
	var lines []string
	if len(js) == 0 {
		lines = append(lines, dimStyle.Render("no jobs yet: highlight something and press c, m or s"))
	}
	first := max(0, len(js)-jobRows)
	if m.jobFocus {
		first = max(0, min(m.jobCursor, len(js)-jobRows))
	}
	for i := first; i < len(js) && i < first+jobRows; i++ {
		lines = append(lines, viewJob(js[i], inner, m.jobFocus && i == m.jobCursor))
	}
	border := dim
	if m.jobFocus {
		border = accent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).
		Width(inner).Height(jobRows).Render(strings.Join(lines, "\n"))
}

func viewJob(j jobs.Job, width int, selected bool) string {
	colors := map[jobs.Status]lipgloss.TerminalColor{jobs.Queued: dim, jobs.Running: accent, jobs.Done: good, jobs.Failed: danger, jobs.Cancelled: warn}
	tag := string(j.Spec.Kind)
	if j.Spec.DryRun {
		tag += " (dry)"
	}
	detail := lastLine(j.Output)
	if j.Status == jobs.Failed && j.Err != nil && detail == "" {
		detail = j.Err.Error()
	}
	detail = safe(detail) // before parsing, so neither branch can pass escapes through
	paths := safe(j.Spec.Source) + " → " + safe(j.Spec.Destination)
	var line string
	if p, ok := progress(detail, j.Status == jobs.Running); ok {
		line = fmt.Sprintf("#%-3d %-10s %-9s %-30s %s", j.ID, tag, j.Status, p, paths)
	} else {
		// errors and anything else rclone printed go last, after the paths
		line = fmt.Sprintf("#%-3d %-10s %-9s %s  %s", j.ID, tag, j.Status, paths, detail)
	}
	line = ansi.Truncate(line, width, "…")
	if selected {
		return selStyle.Render(line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line))))
	}
	return lipgloss.NewStyle().Foreground(colors[j.Status]).Render(line)
}

func (m Model) viewStatus() string {
	if m.plan != nil {
		return "" // the dialog lists its own keys
	}
	if m.status != "" {
		return lipgloss.NewStyle().Foreground(warn).Render(m.status)
	}
	help := "tab switch · enter open · ⌫ up · c copy · m move · s sync · r refresh · J jobs · q quit"
	if m.jobFocus {
		help = "↑/↓ pick · x cancel job · esc back"
	}
	return dimStyle.Render(ansi.Truncate(help, m.w, "…"))
}

// safe replaces control characters (ESC, BEL, C1 codes...) in text that came
// from a remote or from rclone, so a crafted file name can't drive the terminal.
func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// truncateLeft keeps the end of a path, which is the part people care about.
func truncateLeft(s string, width int) string {
	if ansi.StringWidth(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && ansi.StringWidth(string(r))+1 > width {
		r = r[1:]
	}
	return "…" + string(r)
}

// rclone's one-line stats: "25.027 MiB / 61.035 MiB, 41%, 5.000 MiB/s, ETA 7s"
var statsLine = regexp.MustCompile(`^([\d.]+ ?\S*B) / [\d.]+ ?\S*B, (\d+%), ([\d.]+ ?\S*/s), ETA (\S+)`)

// progress shortens a stats line to the parts worth a glance: percent, speed
// and ETA while running, total size once finished.
func progress(detail string, running bool) (string, bool) {
	m := statsLine.FindStringSubmatch(detail)
	if m == nil {
		return "", false
	}
	if running {
		return m[2] + " · " + m[3] + " · ETA " + m[4], true
	}
	return m[1], true
}

var logPrefix = regexp.MustCompile(`^\d{4}/\d\d/\d\d \d\d:\d\d:\d\d \w+\s*:\s*`)

// lastLine returns the most recent line rclone printed, minus its timestamp.
func lastLine(out string) string {
	lines := strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' })
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return logPrefix.ReplaceAllString(l, "")
		}
	}
	return ""
}

func humanSize(n int64) string {
	if n < 0 {
		return ""
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
