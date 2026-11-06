package wizard

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jehadbaeth/brunosync/internal/config"
	"github.com/jehadbaeth/brunosync/internal/prompt"
	"github.com/jehadbaeth/brunosync/internal/source"
	"github.com/jehadbaeth/brunosync/internal/syncer"
)

func fixtureRepo(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "test", "fixtures", "sample-repo"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// script joins answers into wizard input, one per line.
func script(answers ...string) *strings.Reader {
	return strings.NewReader(strings.Join(answers, "\n") + "\n")
}

func runWizard(t *testing.T, path string, in *strings.Reader) (Result, string) {
	t.Helper()
	var out bytes.Buffer
	res, err := Run(Options{In: in, Out: &out, Path: path})
	if err != nil {
		t.Fatalf("wizard failed: %v\n%s", err, out.String())
	}
	if in.Len() != 0 {
		t.Fatalf("wizard finished with unread answers, the script is out of step:\n%s", out.String())
	}
	return res, out.String()
}

func TestNewConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brunosync.yml")
	res, out := runWizard(t, path, script(
		"Test APIs", "", // collection name, folder
		fixtureRepo(t), "", // add the fixture repo, done
		"1", "1", "billing-api", "", // rename spec 1 (billing), done
		"1", "staging", "https", "{service}.stg.example.com", "", // add staging with defaults
		"y", "2", "", "", "8443", "", "", // orders-service: port 8443, then done
		"tenant=okapi", "", // one extra variable
		"",      // environments done
		"2", "", // zip output, default path
		"", "n", // save, no sync
	))
	if !res.Saved || res.RunSync {
		t.Fatalf("result = %+v", res)
	}
	for _, want := range []string{
		"billing                     2 requests",
		"shared module, no requests",
		"orders_service_port = 8443",
		"billing_api_host = billing-api.stg.example.com",
		"Saved " + path,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collection.Name != "Test APIs" || cfg.Output.Type != config.OutputZip {
		t.Errorf("collection or output wrong: %+v", cfg)
	}
	if o := cfg.Repos[0].Overrides["billing/openapi.yml"]; o.Name != "billing-api" {
		t.Errorf("rename not saved: %+v", cfg.Repos[0].Overrides)
	}
	env := cfg.Environments[0]
	if env.Name != "staging" || env.Defaults["protocol"] != "https" || env.Defaults["port"] != "" ||
		env.Services["orders-service"]["port"] != "8443" || len(env.Services["orders-service"]) != 1 ||
		env.Variables["tenant"] != "okapi" {
		t.Errorf("environment wrong: %+v", env)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "node_modules") || strings.Contains(string(raw), "zip:") {
		t.Errorf("defaults written to the file:\n%s", raw)
	}
}

func TestEditExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brunosync.yml")
	orig := "# my notes\nversion: 1\ncollection:\n  name: Mine\nrepos:\n  - path: " + fixtureRepo(t) + "\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(t.TempDir(), "collection.git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	res, out := runWizard(t, path, script(
		"5", "3", filepath.Join(remote, "missing"), remote, "", "collection", "", // git output, a bad URL first
		"1", "", // save; sync defaults to no for git
	))
	if !res.Saved || res.RunSync {
		t.Fatalf("result = %+v\n%s", res, out)
	}
	for _, want := range []string{"✗ cannot reach it", "✓ reachable, empty repository", "commits and pushes to " + remote} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Output.Type != config.OutputGit || cfg.Output.Git.Path != "collection" || !cfg.Output.Git.ShouldPush() || cfg.Collection.Name != "Mine" {
		t.Errorf("config wrong: %+v", cfg)
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != orig {
		t.Errorf("backup missing or wrong: %q", bak)
	}
}

const miniSpec = `openapi: 3.0.3
info: {title: Mini, version: "1"}
servers: [{url: "http://localhost:9000"}]
paths:
  /ping:
    get: {operationId: ping, responses: {"200": {description: ok}}}
`

func TestNameCollisionIsResolved(t *testing.T) {
	repo := t.TempDir()
	spec := []byte(miniSpec)
	for _, d := range []string{"a/v1", "b/v1"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, d, "openapi.yml"), spec, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "brunosync.yml")
	_, out := runWizard(t, path, script(
		"", "", // collection defaults
		repo, "", // add, done
		"", "2", "v1-b", "", // rename is the default action while names collide
		"", "", // no environments, folder output
		"", "n", // save
	))
	if !strings.Contains(out, "NAME TAKEN") {
		t.Errorf("collision not shown:\n%s", out)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	res := syncer.LoadAll(cfg, source.Options{})
	if len(res.Services) != 2 || len(res.Errors) != 0 {
		t.Errorf("collision not resolved: %d services, errors %v", len(res.Services), res.Errors)
	}
}

func TestRepoWithoutSpecsAsksForPatterns(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "billing-api.yaml"), []byte(miniSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "brunosync.yml")
	_, out := runWizard(t, path, script(
		"", "",
		repo, "*-api.yaml", "", // nothing matches the defaults, give a pattern, then done
		"", "", "", "", "n",
	))
	if !strings.Contains(out, "no spec files match") {
		t.Errorf("missing explanation:\n%s", out)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Repos[0].Specs; len(got) != 1 || got[0] != "*-api.yaml" {
		t.Errorf("patterns not saved: %v", got)
	}
}

func TestAbortWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brunosync.yml")
	var out bytes.Buffer
	_, err := Run(Options{In: strings.NewReader("My APIs\n"), Out: &out, Path: path})
	if !errors.Is(err, prompt.ErrAborted) {
		t.Fatalf("want ErrAborted, got %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("aborted wizard wrote the config")
	}
}

func TestQuitWithoutSaving(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brunosync.yml")
	orig := "version: 1\nrepos:\n  - path: " + fixtureRepo(t) + "\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _ := runWizard(t, path, script("6", "Renamed", "", "7", "y"))
	if res.Saved {
		t.Error("reported saved")
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Error("file changed")
	}
}
