package config

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/pelletier/go-toml/v2"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed schema.json template.toml
var files embed.FS

func Template() []byte { b, _ := files.ReadFile("template.toml"); return b }

func Parse(source []byte) (map[string]any, error) {
	if len(source) > 100000 {
		return nil, fmt.Errorf("TOML document exceeds 100 KB")
	}
	var parsed map[string]any
	if err := toml.Unmarshal(source, &parsed); err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	// TOML integers become JSON-compatible values before schema validation.
	b, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &parsed); err != nil {
		return nil, err
	}
	schema, _ := files.ReadFile("schema.json")
	compiler := jsonschema.NewCompiler()
	if err = compiler.AddResource("config.json", bytes.NewReader(schema)); err != nil {
		return nil, err
	}
	validator, err := compiler.Compile("config.json")
	if err != nil {
		return nil, err
	}
	if err = validator.Validate(parsed); err != nil {
		return nil, fmt.Errorf("configuration: %w", err)
	}
	return parsed, nil
}

func Read(path string) ([]byte, map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if stat.Size() > 100000 {
		return nil, nil, fmt.Errorf("TOML document exceeds 100 KB")
	}
	b, err := io.ReadAll(io.LimitReader(f, 100001))
	if err != nil {
		return nil, nil, err
	}
	value, err := Parse(b)
	return b, value, err
}

func Encode(value map[string]any) ([]byte, error) {
	source, err := toml.Marshal(value)
	if err != nil {
		return nil, err
	}
	return Format(source)
}
