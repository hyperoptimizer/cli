package cli

import (
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

func (a *App) projectCommand() *cobra.Command {
	root := &cobra.Command{Use: "project", Aliases: []string{"projects"}, Short: "Browse projects in the selected organization"}
	root.AddCommand(a.createProjectCommand())
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
		tableRows := [][]string{}
		for _, row := range rows {
			tableRows = append(tableRows, []string{row.Name, row.Id})
		}
		message := "No projects in this organization."
		return a.listOutput("projects", map[string]any{"projects": rows}, []string{"NAME", "ID"}, tableRows, message)
	}})
	return root
}

func (a *App) experimentCommand() *cobra.Command {
	root := &cobra.Command{Use: "experiment", Aliases: []string{"experiments"}, Short: "Browse experiments in the selected organization"}
	root.AddCommand(a.experimentShowCommand(), a.experimentControlCommand("pause"), a.experimentControlCommand("resume"), a.experimentBestCommand())
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
		tableRows := [][]string{}
		for _, row := range rows {
			status := "—"
			if row.Optimization != nil {
				status = row.Optimization.Status
			}
			tableRows = append(tableRows, []string{row.Name, status, row.Kind, row.ProjectId, row.Id})
		}
		message := "No experiments in this organization."
		if a.Project != "" {
			message = "No experiments visible for this project in the selected organization."
		}
		return a.listOutput("experiments", map[string]any{"experiments": rows}, []string{"NAME", "STATUS", "KIND", "PROJECT", "ID"}, tableRows, message)
	}})
	return root
}
