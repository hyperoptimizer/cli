package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func TestSharedCanonicalFormatting(t *testing.T) {
	data, err := os.ReadFile("format-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Input, Expected string
		Value                 map[string]any
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			for _, source := range []string{fixture.Input, fixture.Expected} {
				actual, err := Format([]byte(source))
				if err != nil {
					t.Fatal(err)
				}
				if string(actual) != fixture.Expected {
					t.Fatalf("canonical drift:\n%s\nexpected:\n%s", actual, fixture.Expected)
				}
				if args, exists := fixture.Value["fixedArgs"]; exists {
					var parsed map[string]any
					if err := toml.Unmarshal(actual, &parsed); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(parsed["fixedArgs"], args) {
						t.Fatal("argument values or escapes changed")
					}
				}
				again, err := Format(actual)
				if err != nil || !bytes.Equal(actual, again) {
					t.Fatalf("not idempotent: %v\n%s", err, again)
				}
			}
			actual, err := Encode(fixture.Value)
			if err != nil {
				t.Fatal(err)
			}
			if string(actual) != fixture.Expected {
				t.Fatalf("CLI-created file differs from shared export:\n%s", actual)
			}
		})
	}
}

func TestFormattingCommentsAndOrderedValues(t *testing.T) {
	input := `# Project header

algorithm= 'random' # algorithm
region="eu-west"
fixedArgs=[ # command arguments
 '--z', # first argument
 # second argument
 'last', '--a', 'first'
] # do not reorder arguments

# Slow metric
[[objectives]] # latency header
type='minimize' # direction
name='latency'
conditions = [{value=2, op='<=', metric='cost'}, {metric='accuracy',value=0.9,op='>='}] # ordered conditions

# Fast metric
[[objectives]]
type='maximize'
name='accuracy'

# Learning rate
[[parameters]]
max=0.1
name='learning_rate'
type='double'
min=0.0001
slug='learning-rate'

# Batches
[[parameters]] # batch header
values=['64','16','32'] # categorical order
type='categorical'
slug='batch-size'
name='batch_size'

[build] # build header
repository='training'
context='.'
`
	output, err := Format([]byte(strings.ReplaceAll(input, "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Format(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output, again) {
		t.Fatalf("comments not idempotent:\n%s\nsecond:\n%s", output, again)
	}
	text := string(output)
	for _, comment := range []string{"# Project header", "# algorithm", "# command arguments", "# first argument", "# second argument", "# do not reorder arguments", "# Slow metric", "# latency header", "# direction", "# ordered conditions", "# Fast metric", "# Learning rate", "# Batches", "# batch header", "# categorical order", "# build header"} {
		if strings.Count(text, comment) != 1 {
			t.Errorf("comment lost or duplicated: %s\n%s", comment, text)
		}
	}
	if strings.Index(text, "# Batches") > strings.Index(text, "# Learning rate") || strings.Index(text, "# Fast metric") > strings.Index(text, "# Slow metric") {
		t.Fatal("comments did not move with named blocks")
	}
	var parsed map[string]any
	if err := toml.Unmarshal(output, &parsed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed["fixedArgs"], []any{"--z", "last", "--a", "first"}) {
		t.Fatal("fixedArgs reordered")
	}
	params := parsed["parameters"].([]any)
	if !reflect.DeepEqual(params[0].(map[string]any)["values"], []any{"64", "16", "32"}) {
		t.Fatal("categories reordered")
	}
	conditions := parsed["objectives"].([]any)[1].(map[string]any)["conditions"].([]any)
	if conditions[0].(map[string]any)["metric"] != "cost" {
		t.Fatal("conditions reordered")
	}
	if strings.Contains(text, "\r") || !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
		t.Fatal("line endings not normalized")
	}
	if strings.Contains(text, "maxTrialCount") {
		t.Fatal("default inserted")
	}
}

func TestInlineDottedAndNestedEquivalent(t *testing.T) {
	a := `region="eu-west"
build = {repository='train',context='.'} # build
objectives=[{type='maximize',name='z'},{name='a',type='minimize',conditions=[{value=1,op='>=',metric='x'}]}]
parameters=[{values=['z','a'],type='categorical',slug='x',name='x'}]
samplerConfig.seed=42
`
	b := `region = "eu-west"
parameters=[{name='x',slug='x',type='categorical',values=['z','a']}]
[samplerConfig]
seed=42
[build] # build
context='.'
repository='train'
[[objectives]]
name='a'
type='minimize'
[[objectives.conditions]]
metric='x'
op='>='
value=1
[[objectives]]
name='z'
type='maximize'
`
	first, err := Format([]byte(a))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Format([]byte(b))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("equivalent syntax differs:\n%s\n%s", first, second)
	}
}

func TestFormatFileAtomicAndCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("algorithm='random'\r\nregion=\"eu-west\"")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	// Windows exposes different permission bits; preserve the actual original mode.
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := FormatFile(path, true)
	if err != nil || !changed {
		t.Fatalf("check: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, original) {
		t.Fatal("check wrote file")
	}
	if changed, err = FormatFile(path, false); err != nil || !changed {
		t.Fatalf("write: %v", err)
	}
	stat, _ := os.Stat(path)
	if stat.Mode().Perm() != before.Mode().Perm() {
		t.Fatal("permissions changed")
	}
	if changed, err = FormatFile(path, true); err != nil || changed {
		t.Fatalf("formatted check: %v", err)
	}
	for _, invalid := range []string{"region = [", "name='x'\nname='y'"} {
		_ = os.WriteFile(path, []byte(invalid), 0600)
		if _, err := FormatFile(path, false); err == nil {
			t.Fatal("invalid file accepted")
		}
		after, _ = os.ReadFile(path)
		if string(after) != invalid {
			t.Fatal("invalid file changed")
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatal("temporary file leaked")
	}
}
