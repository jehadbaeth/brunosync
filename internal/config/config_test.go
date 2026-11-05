package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "brunosync.yml")
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	p := write(t, "version: 1\nrepos:\n  - path: ../backend\n  - git: git@gitlab.example.com:platform/flight-dynamics.git\n    ref: main\n")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Sync.Removed != RemovedArchive || c.Repos[0].Name != "backend" || c.Repos[1].Name != "flight-dynamics" {
		t.Errorf("defaults not applied: %+v", c)
	}
	if c.CollectionDir() != filepath.Join(filepath.Dir(p), "collection") {
		t.Errorf("collection dir = %s", c.CollectionDir())
	}
}

func TestStarterConfigIsValid(t *testing.T) {
	if _, err := Load(write(t, Starter)); err != nil {
		t.Fatalf("the config written by init must load: %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		"version: 1\nrepos: []\n":                                                  "no repos",
		"version: 1\nrepos:\n  - {path: a, git: b}\n":                              "exactly one of path or git",
		"version: 1\nrepos:\n  - {path: a, ref: main}\n":                           "ref only applies",
		"version: 1\nsync: {removed: nuke}\nrepos:\n  - {path: a}\n":               "sync.removed",
		"version: 1\nrepos:\n  - {path: a}\n  - {path: b/a}\n":                     "duplicate repo name",
		"version: 1\nrepos:\n  - {path: a, typo: 1}\n":                             "field typo not found",
		"version: 1\nrepos:\n  - path: a\n    overrides: {x.yml: {name: 'a b'}}\n": "may only contain",
	}
	for in, want := range cases {
		_, err := Load(write(t, in))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("config %q: want error containing %q, got %v", in, want, err)
		}
	}
}

// The documented reference config must stay loadable.
func TestExampleConfigIsValid(t *testing.T) {
	if _, err := Load(filepath.Join("..", "..", "brunosync.config.example.yml")); err != nil {
		t.Fatalf("brunosync.config.example.yml no longer matches the schema: %v", err)
	}
}
