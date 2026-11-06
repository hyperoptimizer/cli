package cli

import (
	"encoding/csv"
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/jedib0t/go-pretty/v6/table"
	"golang.org/x/term"
)

// listOutput keeps presentation separate from the versioned JSONL event contract.
func (a *App) listOutput(kind string, data map[string]any, headers []string, rows [][]string, empty string) error {
	if a.Output == "jsonl" {
		return a.event(kind, data, empty)
	}
	if a.Output == "csv" {
		w := csv.NewWriter(a.Out)
		if err := w.Write(headers); err != nil {
			return err
		}
		for _, row := range rows {
			if err := w.Write(row); err != nil {
				return err
			}
		}
		w.Flush()
		return w.Error()
	}
	if len(rows) == 0 && a.Output != "markdown" {
		_, err := fmt.Fprintln(a.Out, empty)
		return err
	}
	mode := a.Output
	if mode == "auto" {
		mode = "plain"
		if a.liveOutput() {
			mode = "table"
		}
	}
	width := 0
	if mode == "table" {
		width = 120
		if fd, ok := a.Out.(interface{ Fd() uintptr }); ok {
			if cols, _, err := term.GetSize(int(fd.Fd())); err == nil && cols > 0 {
				width = cols
			}
		}
	}
	_, err := fmt.Fprintln(a.Out, renderList(headers, rows, mode, width))
	return err
}

func displayCell(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}

func renderList(headers []string, rows [][]string, mode string, width int) string {
	t := table.NewWriter()
	header := make(table.Row, len(headers))
	for i, value := range headers {
		header[i] = value
	}
	t.AppendHeader(header)
	for _, values := range rows {
		row := make(table.Row, len(values))
		for i, value := range values {
			row[i] = displayCell(value)
		}
		t.AppendRow(row)
	}
	if mode == "markdown" {
		return t.RenderMarkdown()
	}
	style := table.StyleRounded
	if mode != "table" {
		style.Options.DrawBorder = false
		style.Options.SeparateColumns = false
		style.Options.SeparateHeader = false
	}
	t.SetStyle(style)
	if width > 0 && len(headers) > 0 {
		// Wrap, never truncate, so IDs and long names remain available.
		widths := make([]int, len(headers))
		total := 0
		for i, header := range headers {
			widths[i] = ansi.StringWidth(header)
			for _, row := range rows {
				if i < len(row) {
					for _, line := range strings.Split(displayCell(row[i]), "\n") {
						widths[i] = max(widths[i], ansi.StringWidth(line))
					}
				}
			}
			total += widths[i]
		}
		budget := max(len(headers), width-3*len(headers)-1)
		for total > budget {
			widest := 0
			for i := range widths {
				if widths[i] > widths[widest] {
					widest = i
				}
			}
			widths[widest]--
			total--
		}
		configs := make([]table.ColumnConfig, len(headers))
		for i := range configs {
			configs[i] = table.ColumnConfig{Number: i + 1, WidthMax: max(1, widths[i])}
		}
		t.SetColumnConfigs(configs)
	}
	return t.Render()
}
