package config

import (
	"encoding/json"
	"github.com/pelletier/go-toml/v2"
	"os"
	"testing"
)

func TestSharedStructuralFixtures(t *testing.T) {
	data, err := os.ReadFile("fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name       string
		Value      map[string]any
		Structural bool
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			source, err := toml.Marshal(test.Value)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Parse(source)
			if (err == nil) != test.Structural {
				t.Fatalf("structural validation: %v", err)
			}
		})
	}
}
func TestGeneratedTemplate(t *testing.T) {
	value, err := Parse(Template())
	if err != nil {
		t.Fatal(err)
	}
	if _, present := value["schemaVersion"]; present {
		t.Fatal("template must be unversioned")
	}
	if value["region"] != "eu-west" {
		t.Fatal("wrong template region")
	}
}
