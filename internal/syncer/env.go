package syncer

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/config"
	"github.com/jehadbaeth/brunosync/internal/gen"
	"github.com/jehadbaeth/brunosync/internal/merge"
	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

// EnvDir is the folder Bruno reads environments from.
const EnvDir = "environments"

// envReportName groups environment conflicts and warnings in the report.
const envReportName = "environments"

// EnvVariables computes the variables brunosync writes into one environment:
// per service values (services, then defaults) for the server variables each
// service really has, then the extra variables. Warnings name keys that match
// nothing, which usually means a typo or a renamed service.
func EnvVariables(env config.Environment, outputs []gen.Output, failed map[string]bool) ([]gen.Variable, []string) {
	var vars []gen.Variable
	var warnings []string
	index := map[string]int{}
	add := func(name, value string) {
		if i, ok := index[name]; ok {
			vars[i].Value = value
			return
		}
		index[name] = len(vars)
		vars = append(vars, gen.Variable{Name: name, Value: value})
	}

	known := map[string]bool{}
	usedDefaults := map[string]bool{}
	for _, out := range outputs {
		svc := out.Service.Name
		known[svc] = true
		prefix := gen.VarPrefix(svc)
		own := env.Services[svc]
		names := map[string]bool{"baseUrl": true}
		for _, v := range out.Service.Server.Vars {
			names[v.Name] = true
		}
		order := make([]string, 0, len(out.Service.Server.Vars)+1)
		for _, v := range out.Service.Server.Vars {
			order = append(order, v.Name)
		}
		order = append(order, "baseUrl")
		for _, name := range order {
			value, ok := own[name]
			if !ok {
				if value, ok = env.Defaults[name]; ok {
					usedDefaults[name] = true
				}
			}
			if ok {
				add(prefix+name, strings.ReplaceAll(value, "{service}", svc))
			}
		}
		for _, k := range sortedKeys(own) {
			if !names[k] {
				warnings = append(warnings, fmt.Sprintf(
					"environment %s: service %s has no variable %q (it has %s)", env.Name, svc, k, strings.Join(order, ", ")))
			}
		}
	}
	for _, svc := range sortedKeys(env.Services) {
		if !known[svc] && !failed[svc] {
			warnings = append(warnings, fmt.Sprintf("environment %s: no service named %s (renamed or removed?)", env.Name, svc))
		}
	}
	if len(outputs) > 0 {
		for _, k := range sortedKeys(env.Defaults) {
			if !usedDefaults[k] && !overriddenEverywhere(k, env, outputs) {
				hint := ""
				if k == "port" {
					hint = " (no spec URL has a port, write it into host instead: example.com:8443)"
				}
				warnings = append(warnings, fmt.Sprintf("environment %s: defaults.%s matches no service variable%s", env.Name, k, hint))
			}
		}
	}
	for _, k := range sortedKeys(env.Variables) {
		add(k, env.Variables[k])
	}
	return vars, warnings
}

// overriddenEverywhere reports whether a default key exists in some service
// but every such service sets its own value.
func overriddenEverywhere(key string, env config.Environment, outputs []gen.Output) bool {
	for _, out := range outputs {
		if _, ok := env.Services[out.Service.Name][key]; ok {
			return true
		}
	}
	return false
}

// syncEnvironments writes environments/<name>.yml for every configured
// environment. Only variables brunosync generated are managed: their values
// are merged three way against the manifest, everything else in the file
// (tokens, secrets, colors) is left alone.
func (r *run) syncEnvironments(outputs []gen.Output) error {
	var sr *ServiceReport
	report := func() *ServiceReport {
		if sr == nil {
			sr = r.report.service(envReportName, config.DefaultFile)
		}
		return sr
	}

	configured := map[string]bool{}
	for _, env := range r.cfg.Environments {
		configured[env.Name] = true
		want, warnings := EnvVariables(env, outputs, r.failed)
		for _, w := range warnings {
			report().Warnings = append(report().Warnings, w)
		}
		if err := r.syncEnvironment(env.Name, want, report); err != nil {
			return err
		}
	}

	// Environments removed from the config: delete the file when it only
	// holds what brunosync wrote, otherwise stop managing it and say so.
	for _, name := range sortedKeys(r.manifest.Environments) {
		if configured[name] {
			continue
		}
		rel := envRel(name)
		doc, err := readYAML(r.abs(rel))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if envUntouched(doc, r.manifest.Environments[name]) {
			if err := r.remove(rel, true); err != nil {
				return err
			}
			continue
		}
		report().Warnings = append(report().Warnings, fmt.Sprintf(
			"environment %s is no longer configured but has local changes, kept %s (delete it if it is not needed)", name, rel))
	}
	return nil
}

func (r *run) syncEnvironment(name string, want []gen.Variable, report func() *ServiceReport) error {
	rel := envRel(name)
	base := r.manifest.Environments[name]
	doc, err := readYAML(r.abs(rel))
	created := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: %w", rel, err)
		}
		created = true
		doc = ocyaml.M("name", name)
	}
	original := ocyaml.CloneMap(doc)

	vars := doc.List("variables")
	byName := map[string]*ocyaml.Map{}
	for _, v := range vars {
		if m, ok := v.(*ocyaml.Map); ok {
			byName[m.String("name")] = m
		}
	}

	next := map[string]string{}
	generated := map[string]bool{}
	for _, v := range want {
		generated[v.Name] = true
		next[v.Name] = v.Value
		cur, exists := byName[v.Name]
		if !exists {
			e := ocyaml.M("name", v.Name, "value", v.Value)
			vars = append(vars, e)
			byName[v.Name] = e
			continue
		}
		if secret, _ := cur.Get("secret"); secret == true {
			continue // Bruno keeps secret values outside the file, never touch them
		}
		var b any
		if bv, ok := base[v.Name]; ok {
			b = bv
		}
		val, conflict := merge.Pick(b, cur.String("value"), v.Value)
		if conflict {
			report().Conflicts = append(report().Conflicts, fmt.Sprintf(
				"environment %s, variable %s: kept local value %q, %s now says %q", name, v.Name, cur.String("value"), config.DefaultFile, v.Value))
		}
		cur.Set("value", val)
	}

	// Managed variables that are no longer generated.
	var kept []any
	for _, v := range vars {
		m, ok := v.(*ocyaml.Map)
		if !ok {
			kept = append(kept, v)
			continue
		}
		vn := m.String("name")
		baseVal, managed := base[vn]
		if !managed || generated[vn] {
			kept = append(kept, v)
			continue
		}
		if owner := r.varOwner(vn); owner != "" && r.failed[owner] {
			next[vn] = baseVal // the service failed to load, keep its values as they are
			kept = append(kept, v)
			continue
		}
		if secret, _ := m.Get("secret"); secret != true && m.String("value") == baseVal {
			continue // no longer configured and untouched
		}
		report().Warnings = append(report().Warnings, fmt.Sprintf(
			"environment %s: variable %s is no longer generated but was edited, kept", name, vn))
		kept = append(kept, v)
	}

	if len(kept) > 0 {
		doc.Set("variables", kept)
	} else {
		doc.Delete("variables")
	}
	r.next.Environments[name] = next
	if !created && ocyaml.Equal(original, doc) {
		return nil
	}
	data, err := ocyaml.Marshal(doc)
	if err != nil {
		return err
	}
	return r.write(rel, data, true)
}

// envUntouched reports whether an environment file holds exactly the managed
// variables with their base values and nothing else of the user's.
func envUntouched(doc *ocyaml.Map, base map[string]string) bool {
	for _, k := range doc.Keys() {
		if k != "name" && k != "variables" {
			return false
		}
	}
	vars := doc.List("variables")
	if len(vars) != len(base) {
		return false
	}
	for _, v := range vars {
		m, ok := v.(*ocyaml.Map)
		if !ok {
			return false
		}
		bv, managed := base[m.String("name")]
		if !managed || m.Len() != 2 || m.String("value") != bv {
			return false
		}
	}
	return true
}

func envRel(name string) string { return EnvDir + "/" + name + ".yml" }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
