package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/hyperoptimizer/cli/internal/api"
	"github.com/spf13/cobra"
)

// Fetch one extra row to detect another page without silently truncating results.
func collectPages[T any](fetch func(string, int) ([]T, error), id func(T) string) ([]T, error) {
	rows := make([]T, 0)
	after := ""
	for {
		page, err := fetch(after, 101)
		if err != nil {
			return nil, err
		}
		more := len(page) > 100
		if more {
			page = page[:100]
		}
		for _, row := range page {
			next := id(row)
			if next == "" || next <= after {
				return nil, &api.Error{Code: "INCOMPATIBLE", Message: "GraphQL returned an invalid pagination cursor"}
			}
			after = next
			rows = append(rows, row)
		}
		if !more {
			return rows, nil
		}
	}
}

func listTable(lines []string) string {
	var out strings.Builder
	w := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	w.Flush()
	return strings.TrimSpace(out.String())
}

func (a *App) projectCommand() *cobra.Command {
	root := &cobra.Command{Use: "project", Aliases: []string{"projects"}, Short: "Browse projects in the selected organization"}
	root.AddCommand(&cobra.Command{Use: "list", Short: "List all projects in the selected organization", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		rows, err := collectPages(func(after string, limit int) ([]api.Project, error) {
			return client.Projects(ctx, after, limit)
		}, func(row api.Project) string { return row.Id })
		if err != nil {
			return apiError(err, source)
		}
		lines := []string{"NAME\tID"}
		for _, row := range rows {
			lines = append(lines, fmt.Sprintf("%s\t%s", row.Name, row.Id))
		}
		message := "No projects in this organization."
		if len(rows) > 0 {
			message = listTable(lines)
		}
		return a.event("projects", map[string]any{"projects": rows}, message)
	}})
	return root
}

func (a *App) experimentCommand() *cobra.Command {
	root := &cobra.Command{Use: "experiment", Aliases: []string{"experiments"}, Short: "Browse experiments in the selected organization"}
	root.AddCommand(&cobra.Command{Use: "list", Short: "List all experiments, optionally filtered by --project <id>", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		client, ctx, source, err := a.authorized(cmd.Context())
		if err != nil {
			return err
		}
		rows, err := collectPages(func(after string, limit int) ([]api.ExperimentSummary, error) {
			return client.Experiments(ctx, a.Project, after, limit)
		}, func(row api.ExperimentSummary) string { return row.Id })
		if err != nil {
			return apiError(err, source)
		}
		lines := []string{"NAME\tSTATUS\tKIND\tPROJECT\tID"}
		for _, row := range rows {
			status := "—"
			if row.Optimization != nil {
				status = row.Optimization.Status
			}
			lines = append(lines, fmt.Sprintf("%s\t%s\t%s\t%s\t%s", row.Name, status, row.Kind, row.ProjectId, row.Id))
		}
		message := "No experiments in this organization."
		if a.Project != "" {
			message = "No experiments visible for this project in the selected organization."
		}
		if len(rows) > 0 {
			message = listTable(lines)
		}
		return a.event("experiments", map[string]any{"experiments": rows}, message)
	}})
	return root
}
