package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/hyperoptimizer/cli/internal/api"
)

type snapshot struct {
	Experiment *api.Experiment
	Runs       []api.Run
}

func fetchSnapshot(ctx context.Context, client *api.Client, project, id string, source CredentialSource) (snapshot, error) {
	experiment, err := client.Experiment(ctx, project, id)
	if err != nil {
		return snapshot{}, apiError(err, source)
	}
	var runs []api.Run
	cursor := ""
	for {
		page, err := client.Runs(ctx, project, id, cursor, 101, experiment.Objectives)
		if err != nil {
			return snapshot{}, apiError(err, source)
		}
		more := len(page) > 100
		if more {
			page = page[:100]
		}
		runs = append(runs, page...)
		if !more {
			break
		}
		next := page[len(page)-1].Id
		if next == "" || next <= cursor {
			return snapshot{}, failure(5, "GraphQL returned an invalid run pagination cursor")
		}
		cursor = next
	}
	return snapshot{experiment, runs}, nil
}

func terminalStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}
func metricsText(trial api.Run) string {
	keys := make([]string, 0, len(trial.Metrics))
	for key := range trial.Metrics {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, fmt.Sprintf("%s: %.6g", key, trial.Metrics[key]))
	}
	if len(values) == 0 {
		return "—"
	}
	return strings.Join(values, ", ")
}
func changedRuns(trials []api.Run, seen map[string]string) []api.Run {
	changed := []api.Run{}
	for _, trial := range trials {
		encoded, _ := json.Marshal(trial)
		key := string(encoded)
		if seen[trial.Id] != key {
			seen[trial.Id] = key
			changed = append(changed, trial)
		}
	}
	return changed
}
func finalReady(s snapshot) bool {
	if !terminalStatus(s.Experiment.Status) {
		return false
	}
	for _, trial := range s.Runs {
		if trial.Status == "succeeded" {
			if trial.FinalizationState != "complete" {
				return false
			}
			for _, objective := range s.Experiment.Objectives {
				if _, ok := trial.Metrics[objective]; !ok {
					return false
				}
			}
		}
	}
	return true
}
func (a *App) watch(ctx context.Context, client *api.Client, project, id string, source CredentialSource) error {
	defer func() {
		if ctx.Err() != nil {
			fmt.Fprintf(a.Err, "Detached. Experiment continues. Reconnect: hpo watch %s --project %s\n", id, project)
		}
	}()
	interactive := a.liveOutput()
	tableMode := a.Output == "table" || (a.Output == "auto" && interactive)
	if tableMode && !interactive {
		return failure(2, "Live table output requires a terminal. Use --output plain or jsonl.")
	}
	if tableMode {
		model := &watchModel{app: a, ctx: ctx, client: client, project: project, id: id, source: source, seen: map[string]string{}}
		_, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(a.In), tea.WithOutput(a.Out)).Run()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		if model.err != nil {
			return model.err
		}
		if model.last.Experiment == nil {
			fmt.Fprintf(a.Err, "Detached. Reconnect: hpo watch %s --project %s\n", id, project)
			return nil
		}
		if !terminalStatus(model.last.Experiment.Status) {
			fmt.Fprintf(a.Err, "Detached. Experiment continues. Reconnect: hpo watch %s --project %s\n", id, project)
			return nil
		}
		return outcome(model.last.Experiment)
	}
	seen := map[string]string{}
	lastStatus := ""
	finalPolls := 0
	for {
		snap, err := a.fetchWithRetry(ctx, client, project, id, source)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		for _, trial := range changedRuns(snap.Runs, seen) {
			if err = a.event("run_updated", map[string]any{"experimentId": id, "runId": trial.Id, "number": trial.Number, "status": trial.Status, "metrics": trial.Metrics, "finalizationState": trial.FinalizationState, "optimizerEligible": trial.ResultsAcceptedAt != nil}, fmt.Sprintf("Trial %d %s — %s", trial.Number, trial.Status, metricsText(trial))); err != nil {
				return err
			}
		}
		if snap.Experiment.Status != lastStatus {
			lastStatus = snap.Experiment.Status
			if err = a.event("experiment_status", map[string]any{"experimentId": id, "status": lastStatus}, "Experiment "+lastStatus); err != nil {
				return err
			}
		}
		if terminalStatus(lastStatus) {
			finalPolls++
		}
		if finalReady(snap) || finalPolls >= 12 {
			if !finalReady(snap) {
				fmt.Fprintln(a.Err, "Some final metrics are still unavailable. Reattach with hpo watch to refresh them.")
			}
			return outcome(snap.Experiment)
		}
		if err = wait(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}
func outcome(experiment *api.Experiment) error {
	if experiment.Status == "failed" {
		return failure(1, "Experiment failed")
	}
	if experiment.Status == "cancelled" {
		return failure(1, "Experiment cancelled")
	}
	return nil
}

type snapshotMsg struct {
	value snapshot
	err   error
}
type watchModel struct {
	app         *App
	ctx         context.Context
	client      *api.Client
	project, id string
	source      CredentialSource
	last        snapshot
	seen        map[string]string
	err         error
	width       int
	table       table.Model
	finalPolls  int
}

func (m *watchModel) poll(delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		if err := wait(m.ctx, delay); err != nil {
			return snapshotMsg{err: err}
		}
		value, err := m.app.fetchWithRetry(m.ctx, m.client, m.project, m.id, m.source)
		return snapshotMsg{value, err}
	}
}
func (m *watchModel) Init() tea.Cmd { return m.poll(0) }
func (m *watchModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
		var command tea.Cmd
		m.table, command = m.table.Update(msg)
		return m, command
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.refreshTable()
	case snapshotMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.last = msg.value
		m.refreshTable()
		if terminalStatus(m.last.Experiment.Status) {
			m.finalPolls++
		}
		if finalReady(m.last) || m.finalPolls >= 12 {
			return m, tea.Quit
		}
		return m, m.poll(5 * time.Second)
	}
	return m, nil
}

// Wrap metric text across rows instead of truncating later objectives. The table
// retains every metric for the displayed trials, including on narrow terminals.
func (m *watchModel) refreshTable() {
	width := 45
	if m.width > 0 {
		width = max(10, min(80, m.width-27))
	}
	columns := []table.Column{{Title: "TRIAL", Width: 7}, {Title: "STATUS", Width: 12}, {Title: "METRICS", Width: width}}
	rows := []table.Row{}
	trials := m.last.Runs
	if len(trials) > 24 {
		trials = trials[len(trials)-24:]
	}
	for _, trial := range trials {
		for i, line := range strings.Split(ansi.Hardwrap(metricsText(trial), width, true), "\n") {
			if i == 0 {
				rows = append(rows, table.Row{fmt.Sprint(trial.Number), string(trial.Status), line})
			} else {
				rows = append(rows, table.Row{"", "", line})
			}
		}
	}
	cursor := m.table.Cursor()
	m.table = table.New(table.WithColumns(columns), table.WithRows(rows), table.WithHeight(max(1, min(24, len(rows)))), table.WithFocused(true))
	m.table.SetCursor(cursor)
}
func (m *watchModel) View() string {
	if m.last.Experiment == nil {
		return "Connecting to experiment…\n"
	}
	exp := m.last.Experiment
	finished := 0
	for _, run := range m.last.Runs {
		if run.Status == "succeeded" || run.Status == "failed" || run.Status == "cancelled" || run.Status == "lost" {
			finished++
		}
	}
	return fmt.Sprintf("%s · %s · %d/%d finished\n\n%s\n\n↑/↓ scroll · Ctrl+C / q detach · Experiment continues in the cloud\n", exp.Name, exp.Status, finished, exp.MaxTrialCount, m.table.View())
}

func (a *App) fetchWithRetry(ctx context.Context, client *api.Client, project, id string, source CredentialSource) (snapshot, error) {
	var result snapshot
	err := a.retry(ctx, 6, 0, func() error {
		value, err := fetchSnapshot(ctx, client, project, id, source)
		if err == nil {
			result = value
		}
		return err
	})
	return result, err
}
