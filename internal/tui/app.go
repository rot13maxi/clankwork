package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rot13maxi/clankwork/internal/model"
)

type Mode string

const (
	ModeDashboard   Mode = "dashboard"
	ModeTasks       Mode = "tasks"
	ModeEscalations Mode = "escalations"
	ModeMergeQueue  Mode = "merge-queue"
	ModeHealth      Mode = "health"
	ModeEvents      Mode = "events"
)

var focusedModes = []Mode{ModeTasks, ModeEscalations, ModeMergeQueue, ModeHealth, ModeEvents}

type Config struct {
	Home string
	Mode Mode
}

type appModel struct {
	loader     Backend
	mode       Mode
	width      int
	height     int
	active     int
	selected   map[Mode]int
	inspect    bool
	snapshot   Snapshot
	err        error
	notice     string
	loading    bool
	acting     bool
	lastAction string
}

type snapshotMsg Snapshot

type errMsg struct{ err error }

type refreshTickMsg time.Time

type actionMsg struct {
	summary string
	err     error
}

const refreshInterval = 2 * time.Second

func Run(cfg Config) error {
	if cfg.Mode == "" {
		cfg.Mode = ModeDashboard
	}
	m := appModel{
		loader:   NewLoader(cfg.Home),
		mode:     cfg.Mode,
		active:   0,
		selected: make(map[Mode]int),
		loading:  true,
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

func (m appModel) Init() tea.Cmd {
	return tea.Batch(loadSnapshot(m.loader), refreshTick())
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			if m.inspect {
				m.inspect = false
				return m, nil
			}
			return m, tea.Quit
		case "esc":
			m.inspect = false
		case "r":
			m.loading = true
			return m, loadSnapshot(m.loader)
		case "enter":
			m.inspect = !m.inspect
		case "up", "k":
			m.moveSelection(-1)
		case "down", "j":
			m.moveSelection(1)
		case "x":
			cmd := m.resolveSelectedEscalation()
			if cmd != nil {
				m.acting = true
			}
			return m, cmd
		case "t":
			cmd := m.retrySelectedTaskStep()
			if cmd != nil {
				m.acting = true
			}
			return m, cmd
		case "y":
			cmd := m.retrySelectedQueueItem()
			if cmd != nil {
				m.acting = true
			}
			return m, cmd
		case "s":
			cmd := m.skipSelectedQueueItem()
			if cmd != nil {
				m.acting = true
			}
			return m, cmd
		case "tab":
			if m.mode == ModeDashboard {
				m.inspect = false
				m.active = (m.active + 1) % len(focusedModes)
			}
		case "shift+tab":
			if m.mode == ModeDashboard {
				m.inspect = false
				m.active--
				if m.active < 0 {
					m.active = len(focusedModes) - 1
				}
			}
		case "1", "2", "3", "4", "5":
			if m.mode == ModeDashboard {
				m.inspect = false
				m.active = int(msg.String()[0] - '1')
			}
		}
	case snapshotMsg:
		m.snapshot = Snapshot(msg)
		m.err = nil
		m.clampSelections()
		m.loading = false
	case errMsg:
		m.err = msg.err
		m.loading = false
	case actionMsg:
		m.acting = false
		m.lastAction = msg.summary
		if msg.err != nil {
			m.err = msg.err
			m.notice = ""
			return m, nil
		}
		m.notice = msg.summary
		m.err = nil
		m.loading = true
		return m, loadSnapshot(m.loader)
	case refreshTickMsg:
		if m.loading {
			return m, refreshTick()
		}
		m.loading = true
		return m, tea.Batch(loadSnapshot(m.loader), refreshTick())
	}
	return m, nil
}

func (m appModel) View() string {
	if m.width == 0 {
		return "loading..."
	}

	body := ""
	if m.mode == ModeDashboard {
		if m.inspect {
			body = m.detailView(m.activeMode(), m.height-4)
		} else {
			body = m.dashboardView()
		}
	} else if m.inspect {
		body = m.detailView(m.mode, m.height-4)
	} else {
		body = m.focusedView(m.mode, m.height-4)
	}

	footer := subtleStyle.Render("r refresh  arrows select  enter inspect  tab focus  1-5 panes  q quit")
	if m.mode != ModeDashboard {
		footer = subtleStyle.Render("r refresh  arrows select  enter inspect  q quit")
	}
	if m.activeMode() == ModeEscalations {
		footer += "  " + subtleStyle.Render("x resolve  t retry-task")
	}
	if m.activeMode() == ModeMergeQueue {
		footer += "  " + subtleStyle.Render("y retry  s skip")
	}
	if m.loading {
		footer += "  " + accentStyle.Render("refreshing")
	}
	if m.acting {
		footer += "  " + accentStyle.Render("acting")
	}
	if m.notice != "" {
		footer += "  " + successStyle.Render(m.notice)
	}
	if m.err != nil {
		footer += "  " + dangerStyle.Render(m.err.Error())
	}

	title := titleStyle.Render("Clankwork")
	if m.mode != ModeDashboard {
		title = titleStyle.Render("Clankwork " + string(m.mode))
	}
	return lipgloss.JoinVertical(lipgloss.Left, title, body, footer)
}

func loadSnapshot(loader Backend) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		snap := loader.Snapshot(ctx)
		if snap.HealthError != "" && snap.Status == nil {
			return errMsg{err: fmt.Errorf("%s", snap.HealthError)}
		}
		return snapshotMsg(snap)
	}
}

func refreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg {
		return refreshTickMsg(t)
	})
}

func (m appModel) dashboardView() string {
	usableWidth := max(40, m.width-2)
	leftW := max(36, usableWidth*60/100)
	rightW := max(28, usableWidth-leftW-2)
	halfH := max(8, (m.height-5)/2)

	tasks := m.panel(ModeTasks, leftW, halfH)
	escalations := m.panel(ModeEscalations, rightW, halfH)
	queue := m.panel(ModeMergeQueue, leftW, halfH)
	health := m.panel(ModeHealth, rightW, max(6, halfH/2))
	events := m.panel(ModeEvents, rightW, max(6, halfH/2))

	top := lipgloss.JoinHorizontal(lipgloss.Top, tasks, escalations)
	bottomRight := lipgloss.JoinVertical(lipgloss.Left, health, events)
	bottom := lipgloss.JoinHorizontal(lipgloss.Top, queue, bottomRight)
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom)
}

func (m appModel) panel(mode Mode, width, height int) string {
	style := panelStyle.Width(width).Height(height)
	if focusedModes[m.active] == mode {
		style = activePanelStyle.Width(width).Height(height)
	}
	title := panelTitle(mode)
	content := m.focusedView(mode, height-3)
	return style.Render(lipgloss.JoinVertical(lipgloss.Left, headingStyle.Render(title), content))
}

func (m appModel) focusedView(mode Mode, maxLines int) string {
	switch mode {
	case ModeTasks:
		return limitLines(m.tasksView(), maxLines)
	case ModeEscalations:
		return limitLines(m.escalationsView(), maxLines)
	case ModeMergeQueue:
		return limitLines(m.mergeQueueView(), maxLines)
	case ModeHealth:
		return limitLines(m.healthView(), maxLines)
	case ModeEvents:
		return limitLines(m.eventsView(), maxLines)
	default:
		return limitLines(m.tasksView(), maxLines)
	}
}

func (m appModel) tasksView() string {
	if len(m.snapshot.Tasks) == 0 {
		return emptyStyle.Render("no tasks")
	}
	lines := []string{tableHeader("STATUS", "STEP", "TRY", "AGENT", "TITLE")}
	for i, task := range m.snapshot.Tasks {
		agent := "-"
		if a := m.snapshot.AgentByTaskID(task.ID); a != nil {
			agent = fallback(a.Runtime, a.Model, a.ID)
		}
		line := fmt.Sprintf("%-10s %-12s %-3d %-14s %s",
			statusText(task.Status),
			truncate(blank(task.CurrentStep), 12),
			task.RetryCount,
			truncate(agent, 14),
			truncate(displayTask(task), 54),
		)
		lines = append(lines, m.selectLine(ModeTasks, i, line))
	}
	return strings.Join(lines, "\n")
}

func (m appModel) escalationsView() string {
	if len(m.snapshot.Escalations) == 0 {
		return emptyStyle.Render("no open escalations")
	}
	lines := []string{tableHeader("STATUS", "TASK", "TARGET", "REASON")}
	for i, esc := range m.snapshot.Escalations {
		task := esc.TaskID
		if t := m.snapshot.TaskByID(esc.TaskID); t != nil {
			task = displayTask(t)
		}
		line := fmt.Sprintf("%-10s %-22s %-14s %s",
			statusText(esc.Status),
			truncate(task, 22),
			truncate(esc.TargetType, 14),
			truncate(esc.Reason, 58),
		)
		lines = append(lines, m.selectLine(ModeEscalations, i, line))
	}
	return strings.Join(lines, "\n")
}

func (m appModel) mergeQueueView() string {
	if len(m.snapshot.Queue) == 0 {
		return emptyStyle.Render("merge queue is empty")
	}
	lines := []string{tableHeader("STATUS", "TRY", "TASK", "BRANCH", "TARGET")}
	for i, item := range m.snapshot.Queue {
		line := fmt.Sprintf("%-10s %-3d %-22s %-18s %s",
			statusText(item.Status),
			item.AttemptCount,
			truncate(item.TaskID, 22),
			truncate(item.Branch, 18),
			truncate(item.Target, 24),
		)
		lines = append(lines, m.selectLine(ModeMergeQueue, i, line))
	}
	return strings.Join(lines, "\n")
}

func (m appModel) healthView() string {
	lines := []string{}
	if m.snapshot.HealthError != "" {
		lines = append(lines, dangerStyle.Render("daemon: error"), truncate(m.snapshot.HealthError, 90))
	} else {
		lines = append(lines, successStyle.Render("daemon: reachable"))
	}
	if s := m.snapshot.Status; s != nil {
		lines = append(lines,
			fmt.Sprintf("tasks: %d total  %d pending  %d running  %d blocked", s.Tasks.Total, s.Tasks.Pending, s.Tasks.Running, s.Tasks.Blocked),
			fmt.Sprintf("agents: %d running", s.Agents.Running),
			fmt.Sprintf("plans: %d total  %d active", s.Plans.Total, s.Plans.Active),
			fmt.Sprintf("queue: %d queued  %d active  pressure=%s", s.MergeQueue.Queued, s.MergeQueue.InProgress, s.QueuePressure.Level),
		)
		if s.QueuePressure.Reason != "" {
			lines = append(lines, "reason: "+truncate(s.QueuePressure.Reason, 80))
		}
	}
	lines = append(lines, "loaded: "+m.snapshot.LoadedAt.Format("15:04:05"))
	return strings.Join(lines, "\n")
}

func (m appModel) eventsView() string {
	if len(m.snapshot.Events) == 0 {
		return emptyStyle.Render("no recent events")
	}
	lines := []string{tableHeader("TIME", "SOURCE", "TYPE", "SUMMARY")}
	for i, ev := range m.snapshot.Events {
		line := fmt.Sprintf("%-8s %-10s %-18s %s",
			ev.CreatedAt.Format("15:04:05"),
			truncate(ev.Source, 10),
			truncate(ev.Type, 18),
			truncate(ev.Summary, 70),
		)
		lines = append(lines, m.selectLine(ModeEvents, i, line))
	}
	return strings.Join(lines, "\n")
}

func (m appModel) detailView(mode Mode, maxLines int) string {
	var lines []string
	switch mode {
	case ModeTasks:
		task := m.selectedTask()
		if task == nil {
			return emptyStyle.Render("no selected task")
		}
		lines = append(lines,
			headingStyle.Render("Task Detail"),
			"ID: "+task.ID,
			"Name: "+blank(task.Name),
			"Title: "+task.Title,
			"Status: "+task.Status,
			"Step: "+blank(task.CurrentStep),
			"Runtime: "+blank(task.Runtime),
			fmt.Sprintf("Retries: %d", task.RetryCount),
			"Updated: "+task.UpdatedAt.Format(time.RFC3339),
		)
		if agent := m.snapshot.AgentByTaskID(task.ID); agent != nil {
			lines = append(lines, "", headingStyle.Render("Agent"), "ID: "+agent.ID, "Status: "+agent.Status, "Runtime: "+blank(agent.Runtime), "Worktree: "+blank(agent.WorktreePath))
		}
	case ModeEscalations:
		esc := m.selectedEscalation()
		if esc == nil {
			return emptyStyle.Render("no selected escalation")
		}
		lines = append(lines,
			headingStyle.Render("Escalation Detail"),
			"ID: "+esc.ID,
			"Status: "+esc.Status,
			"Task: "+blank(esc.TaskID),
			"Step: "+blank(esc.StepName),
			"Target: "+esc.TargetType,
			"Action: "+blank(esc.RequestedAction),
			"Signature: "+blank(esc.FailureSignature),
			"Created: "+esc.CreatedAt.Format(time.RFC3339),
			"",
			headingStyle.Render("Reason"),
			esc.Reason,
		)
		if len(esc.SuggestedCommands) > 0 {
			lines = append(lines, "", headingStyle.Render("Suggested Commands"))
			for _, command := range esc.SuggestedCommands {
				lines = append(lines, "  "+command)
			}
		}
		lines = append(lines, "", subtleStyle.Render("x resolve escalation  t retry task step  esc/q back"))
	case ModeMergeQueue:
		item := m.selectedQueueItem()
		if item == nil {
			return emptyStyle.Render("no selected queue item")
		}
		lines = append(lines,
			headingStyle.Render("Merge Queue Detail"),
			"ID: "+item.ID,
			"Status: "+item.Status,
			"Task: "+item.TaskID,
			"Branch: "+item.Branch,
			"Target: "+item.Target,
			fmt.Sprintf("Attempts: %d", item.AttemptCount),
			"Queued: "+item.QueuedAt.Format(time.RFC3339),
			"Worktree: "+blank(item.WorktreePath),
		)
		if item.FailureLog != "" {
			lines = append(lines, "", headingStyle.Render("Failure"), item.FailureLog)
		}
		lines = append(lines, "", subtleStyle.Render("y retry queue item  s skip queue item  esc/q back"))
	case ModeEvents:
		event := m.selectedEvent()
		if event == nil {
			return emptyStyle.Render("no selected event")
		}
		lines = append(lines,
			headingStyle.Render("Event Detail"),
			"ID: "+event.ID,
			"Time: "+event.CreatedAt.Format(time.RFC3339),
			"Source: "+event.Source,
			"Type: "+event.Type,
			"Task: "+blank(event.TaskID),
			"Agent: "+blank(event.AgentID),
			"",
			headingStyle.Render("Summary"),
			event.Summary,
		)
		if event.Payload != "" {
			lines = append(lines, "", headingStyle.Render("Payload"), event.Payload)
		}
	case ModeHealth:
		lines = append(lines, headingStyle.Render("Health Detail"))
		lines = append(lines, strings.Split(m.healthView(), "\n")...)
	}
	return limitLines(strings.Join(lines, "\n"), maxLines)
}

func (m appModel) activeMode() Mode {
	if m.mode != ModeDashboard {
		return m.mode
	}
	return focusedModes[m.active]
}

func (m *appModel) moveSelection(delta int) {
	if m.selected == nil {
		m.selected = make(map[Mode]int)
	}
	mode := m.activeMode()
	count := m.itemCount(mode)
	if count == 0 {
		m.selected[mode] = 0
		return
	}
	next := m.selected[mode] + delta
	if next < 0 {
		next = count - 1
	}
	if next >= count {
		next = 0
	}
	m.selected[mode] = next
}

func (m *appModel) clampSelections() {
	if m.selected == nil {
		m.selected = make(map[Mode]int)
	}
	for _, mode := range focusedModes {
		count := m.itemCount(mode)
		if count == 0 || m.selected[mode] < 0 {
			m.selected[mode] = 0
			continue
		}
		if m.selected[mode] >= count {
			m.selected[mode] = count - 1
		}
	}
}

func (m appModel) itemCount(mode Mode) int {
	switch mode {
	case ModeTasks:
		return len(m.snapshot.Tasks)
	case ModeEscalations:
		return len(m.snapshot.Escalations)
	case ModeMergeQueue:
		return len(m.snapshot.Queue)
	case ModeEvents:
		return len(m.snapshot.Events)
	default:
		return 0
	}
}

func (m appModel) selectLine(mode Mode, index int, line string) string {
	if mode == m.activeMode() && m.selected[mode] == index {
		return selectedStyle.Render("> " + line)
	}
	return "  " + line
}

func (m appModel) selectedTask() *model.Task {
	if len(m.snapshot.Tasks) == 0 {
		return nil
	}
	return m.snapshot.Tasks[m.selected[ModeTasks]]
}

func (m appModel) selectedEscalation() *model.Escalation {
	if len(m.snapshot.Escalations) == 0 {
		return nil
	}
	return m.snapshot.Escalations[m.selected[ModeEscalations]]
}

func (m appModel) selectedQueueItem() *model.MergeQueueItem {
	if len(m.snapshot.Queue) == 0 {
		return nil
	}
	return m.snapshot.Queue[m.selected[ModeMergeQueue]]
}

func (m appModel) selectedEvent() *model.ControlPlaneEvent {
	if len(m.snapshot.Events) == 0 {
		return nil
	}
	return m.snapshot.Events[m.selected[ModeEvents]]
}

func (m appModel) resolveSelectedEscalation() tea.Cmd {
	esc := m.selectedEscalation()
	if esc == nil || m.acting {
		return nil
	}
	return runAction("resolved "+esc.ID, func(ctx context.Context) error {
		return m.loader.ResolveEscalation(ctx, esc.ID, "resolved_from_tui")
	})
}

func (m appModel) retrySelectedTaskStep() tea.Cmd {
	taskID, step := "", ""
	if m.activeMode() == ModeEscalations {
		if esc := m.selectedEscalation(); esc != nil {
			taskID = esc.TaskID
			step = esc.StepName
		}
	} else if task := m.selectedTask(); task != nil {
		taskID = task.ID
		step = task.CurrentStep
	}
	if taskID == "" || m.acting {
		return nil
	}
	return runAction("queued retry for "+taskID, func(ctx context.Context) error {
		return m.loader.RetryTaskStep(ctx, taskID, step)
	})
}

func (m appModel) retrySelectedQueueItem() tea.Cmd {
	item := m.selectedQueueItem()
	if item == nil || m.acting {
		return nil
	}
	return runAction("queued merge retry "+item.ID, func(ctx context.Context) error {
		return m.loader.RetryQueueItem(ctx, item.ID)
	})
}

func (m appModel) skipSelectedQueueItem() tea.Cmd {
	item := m.selectedQueueItem()
	if item == nil || m.acting {
		return nil
	}
	return runAction("skipped queue item "+item.ID, func(ctx context.Context) error {
		return m.loader.SkipQueueItem(ctx, item.ID)
	})
}

func runAction(summary string, fn func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return actionMsg{summary: summary, err: fn(ctx)}
	}
}

func panelTitle(mode Mode) string {
	switch mode {
	case ModeTasks:
		return "1 Tasks"
	case ModeEscalations:
		return "2 Escalations"
	case ModeMergeQueue:
		return "3 Merge Queue"
	case ModeHealth:
		return "4 Health"
	case ModeEvents:
		return "5 Events"
	default:
		return string(mode)
	}
}

func displayTask(task *model.Task) string {
	if task == nil {
		return "-"
	}
	if task.Name != "" {
		return task.Name
	}
	if task.Title != "" {
		return task.Title
	}
	return task.ID
}

func limitLines(s string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	lines = lines[:maxLines]
	if len(lines) > 0 {
		lines[len(lines)-1] = subtleStyle.Render("...")
	}
	return strings.Join(lines, "\n")
}

func tableHeader(cols ...string) string {
	return subtleStyle.Render(strings.Join(cols, " "))
}

func blank(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func fallback(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "-"
}

func statusText(status string) string {
	switch status {
	case "blocked", "failed":
		return dangerStyle.Render(status)
	case "running":
		return accentStyle.Render(status)
	case "done", "merged":
		return successStyle.Render(status)
	default:
		return status
	}
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if maxLen <= 0 || len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var (
	titleStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	headingStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	subtleStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	emptyStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	accentStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	successStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	dangerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	selectedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("14"))
	panelStyle       = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	activePanelStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("14")).Padding(0, 1)
)
