package cli

import (
	"context"
	"errors"
	"io"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/muesli/cancelreader"
)

func (a *App) interactive() bool {
	if a.Interactive != nil {
		return a.Interactive()
	}
	return terminalStream(a.In) && terminalStream(a.Err)
}

// Huh accessible forms swallow read errors. Record them so EOF/cancellation
// cannot silently submit a default answer or select the first project.
type promptReader struct {
	io.Reader
	mu  sync.Mutex
	err error
}

func (r *promptReader) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	if n == 0 && err != nil {
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
	}
	return n, err
}
func (r *promptReader) readError() error { r.mu.Lock(); defer r.mu.Unlock(); return r.err }

func (a *App) runPrompt(ctx context.Context, form *huh.Form) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	accessible := a.Accessible || a.dumbTerminal()
	if !accessible {
		// Preserve the native file descriptor: Bubble Tea needs it for raw-mode
		// keyboard input and owns its own cancel-reader in this mode.
		interrupted := false
		// Bubble Tea's immediate interrupt shutdown can close its Linux cancel
		// reader while the input loop still accesses it. Quit gracefully so it
		// waits for the reader, while retaining cancellation as the result.
		form.WithProgramOptions(tea.WithReportFocus(), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.InterruptMsg); ok {
				interrupted = true
				return tea.QuitMsg{}
			}
			return msg
		}))
		err := form.WithInput(a.In).WithOutput(a.Err).RunWithContext(ctx)
		if interrupted {
			return context.Canceled
		}
		return promptError(ctx, err)
	}
	reader, err := cancelreader.NewReader(a.In)
	if err != nil {
		return failure(2, "Cannot read terminal input")
	}
	defer reader.Close()
	stop := context.AfterFunc(ctx, func() { reader.Cancel() })
	defer stop()
	tracked := &promptReader{Reader: reader}
	err = form.WithInput(tracked).WithOutput(a.Err).WithAccessible(true).RunWithContext(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if tracked.readError() != nil {
		return failure(2, "Prompt input ended; no action was taken")
	}
	return promptError(ctx, err)
}

func promptError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, huh.ErrUserAborted) {
		return context.Canceled
	}
	if err != nil {
		return failure(2, "Could not complete terminal prompt")
	}
	return nil
}

func (a *App) confirmBrowser(ctx context.Context) (bool, error) {
	if a.ConfirmBrowser != nil {
		return a.ConfirmBrowser(ctx)
	}
	open := false
	form := huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Open in browser?").Affirmative("Yes").Negative("No, I'll use the URL").Value(&open)))
	if err := a.runPrompt(ctx, form); err != nil {
		return false, err
	}
	return open, nil
}

func (a *App) selectProject(ctx context.Context, projects []api.Project) (string, error) {
	if a.SelectProject != nil {
		return a.SelectProject(ctx, projects)
	}
	options := make([]huh.Option[string], 0, len(projects))
	for _, project := range projects {
		options = append(options, huh.NewOption(project.Name+" ("+project.Id+")", project.Id))
	}
	var selected string
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Choose a project").Options(options...).Filtering(true).Value(&selected)))
	if err := a.runPrompt(ctx, form); err != nil {
		return "", err
	}
	return selected, nil
}
