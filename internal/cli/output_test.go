package cli

import (
	"bytes"
	"encoding/csv"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestListOutputFormats(t *testing.T) {
	for _, mode := range []string{"auto", "plain", "table", "markdown", "csv", "jsonl"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			app := New(nil, &out, &out)
			app.Output = mode
			rows := [][]string{{"東京, \"experiment\"\nsecond line", "project_123"}}
			if err := app.listOutput("projects", map[string]any{"projects": rows}, []string{"NAME", "ID"}, rows, "No projects."); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if strings.Contains(got, "[..]") || strings.Contains(got, "\x1b") {
				t.Fatalf("unexpected decoration: %q", got)
			}
			switch mode {
			case "csv":
				records, err := csv.NewReader(strings.NewReader(got)).ReadAll()
				if err != nil || len(records) != 2 || records[1][0] != rows[0][0] {
					t.Fatalf("CSV round trip: %v %q", err, records)
				}
			case "table":
				if !strings.Contains(got, "╭") {
					t.Fatal(got)
				}
			case "markdown":
				if !strings.Contains(got, "| NAME | ID |") {
					t.Fatal(got)
				}
			case "jsonl":
				if !strings.Contains(got, `"event":"projects"`) || !strings.Contains(got, `"version":1`) {
					t.Fatal(got)
				}
			default:
				if strings.ContainsAny(got, "│╭") || !strings.Contains(got, "project_123") {
					t.Fatal(got)
				}
			}
		})
	}
}

func TestListEmptyExports(t *testing.T) {
	for _, mode := range []string{"csv", "markdown", "table"} {
		var out bytes.Buffer
		app := New(nil, &out, &out)
		app.Output = mode
		if err := app.listOutput("projects", nil, []string{"NAME", "ID"}, nil, "No projects."); err != nil {
			t.Fatal(err)
		}
		if mode == "table" && out.String() != "No projects.\n" {
			t.Fatal(out.String())
		}
		if mode != "table" && (!strings.Contains(out.String(), "NAME") || strings.Contains(out.String(), "No projects")) {
			t.Fatal(out.String())
		}
	}
}

func TestListWidthAndControlCharacters(t *testing.T) {
	got := renderList([]string{"NAME", "ID"}, [][]string{{"\x1b[31m東京\x1b[0m\tname", strings.Repeat("x", 80)}}, "table", 40)
	for _, line := range strings.Split(got, "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("line exceeds terminal: %q", line)
		}
	}
	if strings.ContainsAny(got, "\x1b\t") || strings.Count(got, "x") != 80 {
		t.Fatalf("corrupted output: %q", got)
	}
}

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func TestListOutputWriteErrors(t *testing.T) {
	for _, mode := range []string{"csv", "markdown", "table", "plain", "jsonl"} {
		app := New(nil, brokenOutput{}, nil)
		app.Output = mode
		if err := app.listOutput("projects", nil, []string{"NAME"}, [][]string{{"test"}}, "empty"); err == nil {
			t.Fatalf("%s discarded write error", mode)
		}
	}
}

func TestExportRejectedBeforeCommandExecution(t *testing.T) {
	for _, mode := range []string{"csv", "markdown"} {
		var out bytes.Buffer
		app := New(nil, &out, &out)
		cmd := app.Command()
		cmd.SetArgs([]string{"optimize", "--output", mode})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "supported only") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}
