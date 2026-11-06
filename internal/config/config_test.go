package config

import (
	"os"
	"path/filepath"
	"reflect"
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
		"version: 1\nrepos: []\n":                                                                "no repos",
		"version: 1\nrepos:\n  - {path: a, git: b}\n":                                            "exactly one of path or git",
		"version: 1\nrepos:\n  - {path: a, ref: main}\n":                                         "ref only applies",
		"version: 1\nsync: {removed: nuke}\nrepos:\n  - {path: a}\n":                             "sync.removed",
		"version: 1\nrepos:\n  - {path: a}\n  - {path: b/a}\n":                                   "duplicate repo name",
		"version: 1\nrepos:\n  - {path: a, typo: 1}\n":                                           "field typo not found",
		"version: 1\nrepos:\n  - path: a\n    overrides: {x.yml: {name: 'a b'}}\n":               "may only contain",
		"version: 1\nenvironments:\n  - defaults: {host: x}\nrepos:\n  - {path: a}\n":            "name is required",
		"version: 1\nenvironments:\n  - {name: ../prod}\nrepos:\n  - {path: a}\n":                "may only contain",
		"version: 1\nenvironments:\n  - {name: Prod}\n  - {name: prod}\nrepos:\n  - {path: a}\n": "duplicate name",
		"version: 1\nenvironments:\n  - {name: prod, servers: {}}\nrepos:\n  - {path: a}\n":      "field servers not found",
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

func TestEnvironments(t *testing.T) {
	c, err := Load(write(t, `version: 1
environments:
  - name: local
  - name: staging
    defaults: {protocol: https, host: "{service}.staging.example.com", port: "443"}
    services:
      orders-service: {host: orders.internal}
    variables: {tenant: okapi}
repos:
  - path: a
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Environments) != 2 || c.Environments[1].Services["orders-service"]["host"] != "orders.internal" ||
		c.Environments[1].Defaults["port"] != "443" || c.Environments[1].Variables["tenant"] != "okapi" {
		t.Errorf("environments not decoded: %+v", c.Environments)
	}
}

// Marshal writes what was decoded and nothing more, so editing a config with
// the wizard never bakes defaults into the file, and the result loads to the
// same config as the original.
func TestMarshalRoundTrip(t *testing.T) {
	path := filepath.Join("..", "..", "brunosync.config.example.yml")
	raw, err := Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range []string{"node_modules", "message:", ".zip"} {
		if strings.Contains(string(data), def) {
			t.Errorf("marshalled config contains the default %q:\n%s", def, data)
		}
	}
	again, err := Parse(data, raw.Dir)
	if err != nil {
		t.Fatalf("marshalled config does not load: %v\n%s", err, data)
	}
	want, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, want) {
		t.Errorf("round trip changed the config:\n got %+v\nwant %+v", again, want)
	}

	// marshalling twice is stable
	raw2, err := decode(data, raw.Dir)
	if err != nil {
		t.Fatal(err)
	}
	data2, _ := Marshal(raw2)
	if string(data2) != string(data) {
		t.Errorf("not stable:\n%s\n---\n%s", data, data2)
	}
}

func TestPreparedDoesNotTouchTheRawConfig(t *testing.T) {
	raw := &Config{Dir: t.TempDir(), Repos: []Repo{{Path: "../x"}}}
	c, err := Prepared(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Repos[0].Name != "x" || len(c.Repos[0].Exclude) == 0 {
		t.Errorf("defaults not applied: %+v", c.Repos[0])
	}
	if raw.Repos[0].Name != "" || raw.Repos[0].Exclude != nil || raw.Output.Type != "" {
		t.Errorf("raw config changed: %+v", raw)
	}
}
