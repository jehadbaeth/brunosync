package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jehadbaeth/brunosync/internal/config"
)

func TestServiceName(t *testing.T) {
	cases := map[string]string{
		"orders-service/src/main/resources/openapi.yml": "orders-service",
		"services/billing/openapi.yml":                  "billing",
		"api/openapi.yaml":                              "myrepo",
		"openapi.yml":                                   "myrepo",
		"Flight Dynamics/openapi.yml":                   "Flight-Dynamics",
	}
	for rel, want := range cases {
		if got := ServiceName(SpecFile{Repo: "myrepo", Rel: rel}); got != want {
			t.Errorf("%s: got %q want %q", rel, got, want)
		}
	}
	if got := ServiceName(SpecFile{Repo: "r", Rel: "a/openapi.yml", Override: config.Override{Name: "custom"}}); got != "custom" {
		t.Errorf("override ignored: %s", got)
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{
		"a/openapi.yml",
		"b/src/main/resources/openapi.yaml",
		"node_modules/x/openapi.yml",
		"build/generated/openapi.yml",
		"c/other.yml",
	} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := config.Repo{Name: "r", Specs: config.DefaultSpecs, Exclude: config.DefaultExclude}
	files, err := Discover(r, dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Rel)
	}
	if strings.Join(got, ",") != "a/openapi.yml,b/src/main/resources/openapi.yaml" {
		t.Errorf("discovered %v", got)
	}

	r.Overrides = map[string]config.Override{"does/not/exist.yml": {Skip: true}}
	if _, err := Discover(r, dir); err == nil {
		t.Error("an override that matches nothing should be an error (typo protection)")
	}
}
