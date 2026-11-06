package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jehadbaeth/brunosync/internal/config"
	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

func envVars(t *testing.T, p string) map[string]*ocyaml.Map {
	t.Helper()
	out := map[string]*ocyaml.Map{}
	for _, v := range load(t, p).List("variables") {
		m := v.(*ocyaml.Map)
		out[m.String("name")] = m
	}
	return out
}

func stagingEnv() config.Environment {
	return config.Environment{
		Name:      "staging",
		Defaults:  map[string]string{"protocol": "https", "host": "{service}.staging.example.com"},
		Services:  map[string]map[string]string{"orders-service": {"port": "443"}},
		Variables: map[string]string{"tenant": "okapi"},
	}
}

func TestEnvironmentFiles(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.Environments = []config.Environment{{Name: "local"}, stagingEnv()}
	rep := mustSync(t, cfg)
	root := cfg.CollectionDir()

	local := load(t, filepath.Join(root, "environments", "local.yml"))
	if local.String("name") != "local" || len(local.List("variables")) != 0 {
		t.Errorf("an empty environment should be a bare file, got %v", local)
	}

	vars := envVars(t, filepath.Join(root, "environments", "staging.yml"))
	want := map[string]string{
		"orders_service_protocol": "https",
		"orders_service_host":     "orders-service.staging.example.com",
		"orders_service_port":     "443",
		"billing_protocol":        "https",
		"billing_host":            "billing.staging.example.com",
		"tenant":                  "okapi",
	}
	for k, v := range want {
		if vars[k] == nil || vars[k].String("value") != v {
			t.Errorf("%s = %v, want %q", k, vars[k], v)
		}
	}
	if len(vars) != len(want) {
		t.Errorf("unexpected variables: %v", vars)
	}
	if s := findOptional(rep, envReportName); s != nil {
		t.Errorf("unexpected environment report: %+v", s)
	}

	// second run changes nothing
	if rep := mustSync(t, cfg); rep.HasChanges() {
		t.Errorf("second sync changed files: %v", rep.Changes)
	}
}

func TestEnvironmentMergeKeepsUserWork(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.Environments = []config.Environment{stagingEnv()}
	mustSync(t, cfg)
	p := filepath.Join(cfg.CollectionDir(), "environments", "staging.yml")

	// In Bruno: edit one managed value, add a token, mark one managed var
	// secret, pick a color.
	doc := load(t, p)
	doc.Set("color", "#ff0000")
	var vars []any
	for _, v := range doc.List("variables") {
		m := v.(*ocyaml.Map)
		switch m.String("name") {
		case "billing_host":
			m.Set("value", "localhost")
		case "tenant":
			m = ocyaml.M("secret", true, "name", "tenant")
		}
		vars = append(vars, m)
	}
	vars = append(vars, ocyaml.M("name", "token", "value", "abc"))
	doc.Set("variables", vars)
	writeDoc(t, p, doc)

	// config changes: billing host changes too (conflict), orders port changes
	// (clean update), the extra variable changes (secret, so left alone)
	env := stagingEnv()
	env.Services["billing"] = map[string]string{"host": "billing.internal"}
	env.Services["orders-service"]["port"] = "8443"
	env.Variables["tenant"] = "other"
	cfg.Environments = []config.Environment{env}
	rep := mustSync(t, cfg)

	got := envVars(t, p)
	if got["billing_host"].String("value") != "localhost" {
		t.Errorf("local edit lost: %v", got["billing_host"])
	}
	if got["orders_service_port"].String("value") != "8443" {
		t.Errorf("config change not applied: %v", got["orders_service_port"])
	}
	if got["token"] == nil || got["token"].String("value") != "abc" {
		t.Error("user variable lost")
	}
	if got["tenant"].String("value") != "" || got["tenant"].Value("secret") != true {
		t.Errorf("secret variable touched: %v", got["tenant"])
	}
	if load(t, p).String("color") != "#ff0000" {
		t.Error("color lost")
	}
	s := findService(t, rep, envReportName)
	if len(s.Conflicts) != 1 || !strings.Contains(s.Conflicts[0], "billing_host") {
		t.Errorf("want one billing_host conflict, got %v", s.Conflicts)
	}

	// a managed variable that is no longer configured disappears when
	// untouched, the user's variable stays
	env.Services["orders-service"] = nil
	cfg.Environments = []config.Environment{env}
	mustSync(t, cfg)
	got = envVars(t, p)
	if got["orders_service_port"] != nil {
		t.Error("unconfigured managed variable kept")
	}
	if got["token"] == nil {
		t.Error("user variable lost")
	}
}

func TestEnvironmentRemovedFromConfig(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.Environments = []config.Environment{{Name: "dev", Defaults: map[string]string{"host": "dev"}}, stagingEnv()}
	mustSync(t, cfg)
	envDir := filepath.Join(cfg.CollectionDir(), "environments")
	staging := filepath.Join(envDir, "staging.yml")
	doc := load(t, staging)
	doc.Set("variables", append(doc.List("variables"), ocyaml.M("name", "token", "value", "abc")))
	writeDoc(t, staging, doc)

	cfg.Environments = nil
	rep := mustSync(t, cfg)
	if _, err := os.Stat(filepath.Join(envDir, "dev.yml")); !os.IsNotExist(err) {
		t.Error("untouched environment file should be deleted")
	}
	if _, err := os.Stat(staging); err != nil {
		t.Error("edited environment file must be kept")
	}
	if s := findService(t, rep, envReportName); !containsSub(s.Warnings, "staging is no longer configured") {
		t.Errorf("missing warning: %v", s.Warnings)
	}
	// the kept file is unmanaged now, the next run is quiet
	if rep := mustSync(t, cfg); rep.HasChanges() || findOptional(rep, envReportName) != nil {
		t.Errorf("unmanaged environment still reported: %+v", rep)
	}
}

func TestEnvironmentWarnings(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.Environments = []config.Environment{{
		Name:     "prod",
		Defaults: map[string]string{"hots": "x"},
		Services: map[string]map[string]string{
			"orders":         {"host": "x"},
			"orders-service": {"baseUrl": "https://gw.example.com/orders", "prot": "https"},
		},
	}}
	rep := mustSync(t, cfg)
	w := findService(t, rep, envReportName).Warnings
	for _, want := range []string{"no service named orders", `has no variable "prot"`, "defaults.hots matches no service variable"} {
		if !containsSub(w, want) {
			t.Errorf("missing warning %q in %v", want, w)
		}
	}
	vars := envVars(t, filepath.Join(cfg.CollectionDir(), "environments", "prod.yml"))
	if vars["orders_service_baseUrl"].String("value") != "https://gw.example.com/orders" {
		t.Error("baseUrl override not written")
	}
}

func findOptional(rep *Report, name string) *ServiceReport {
	for _, s := range rep.Services {
		if s.Name == name {
			return s
		}
	}
	return nil
}
