// Package config loads and validates brunosync.yml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// DefaultFile is the config file name looked up when none is given.
const DefaultFile = "brunosync.yml"

// Default spec globs and excludes, used when a repo does not set its own.
var (
	DefaultSpecs   = []string{"**/openapi.yml", "**/openapi.yaml"}
	DefaultExclude = []string{"**/node_modules/**", "**/build/**", "**/target/**", "**/dist/**", "**/.git/**"}
)

// Removed endpoint handling (DESIGN.md 4.3).
const (
	RemovedArchive = "archive"
	RemovedDelete  = "delete"
	RemovedKeep    = "keep"
)

// Config is the root of brunosync.yml.
type Config struct {
	Version    int        `yaml:"version"`
	Collection Collection `yaml:"collection"`
	Sync       Sync       `yaml:"sync"`
	Repos      []Repo     `yaml:"repos"`

	// Dir is the directory of the config file. Relative paths resolve against it.
	Dir string `yaml:"-"`
}

// Collection describes the Bruno collection that brunosync writes.
type Collection struct {
	Path string `yaml:"path"`
	Name string `yaml:"name"`
}

// Sync holds merge behaviour switches.
type Sync struct {
	Removed             string `yaml:"removed"`
	RequireOperationIDs bool   `yaml:"requireOperationIds"`
}

// Repo is one source repository holding one or more specs.
type Repo struct {
	Name      string              `yaml:"name"`
	Path      string              `yaml:"path"`
	Git       string              `yaml:"git"`
	Ref       string              `yaml:"ref"`
	Specs     []string            `yaml:"specs"`
	Exclude   []string            `yaml:"exclude"`
	Overrides map[string]Override `yaml:"overrides"`
}

// Override changes how one discovered spec is treated. Keyed by the spec path
// relative to the repo root, for example "orders/src/main/resources/openapi.yml".
type Override struct {
	Name   string `yaml:"name"`
	Folder string `yaml:"folder"`
	Skip   bool   `yaml:"skip"`
}

var serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file %s not found (run `brunosync init` to create one)", path)
		}
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Dir = filepath.Dir(abs)
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Collection.Path == "" {
		c.Collection.Path = "./collection"
	}
	if c.Collection.Name == "" {
		c.Collection.Name = "APIs"
	}
	if c.Sync.Removed == "" {
		c.Sync.Removed = RemovedArchive
	}
	for i := range c.Repos {
		r := &c.Repos[i]
		if len(r.Specs) == 0 {
			r.Specs = DefaultSpecs
		}
		r.Exclude = append(append([]string{}, DefaultExclude...), r.Exclude...)
		if r.Name == "" {
			r.Name = repoNameFrom(r)
		}
	}
}

// Validate checks the config for mistakes that would otherwise surface late.
func (c *Config) Validate() error {
	var errs []string
	if c.Version != 1 {
		errs = append(errs, fmt.Sprintf("unsupported config version %d (expected 1)", c.Version))
	}
	switch c.Sync.Removed {
	case RemovedArchive, RemovedDelete, RemovedKeep:
	default:
		errs = append(errs, fmt.Sprintf("sync.removed must be archive, delete or keep, got %q", c.Sync.Removed))
	}
	if len(c.Repos) == 0 {
		errs = append(errs, "no repos configured")
	}
	seen := map[string]bool{}
	for i, r := range c.Repos {
		where := fmt.Sprintf("repos[%d] (%s)", i, r.Name)
		if (r.Path == "") == (r.Git == "") {
			errs = append(errs, where+": set exactly one of path or git")
		}
		if r.Ref != "" && r.Git == "" {
			errs = append(errs, where+": ref only applies to git sources")
		}
		if seen[r.Name] {
			errs = append(errs, where+": duplicate repo name, set a unique name")
		}
		seen[r.Name] = true
		for spec, o := range r.Overrides {
			if o.Name != "" && !serviceNameRe.MatchString(o.Name) {
				errs = append(errs, fmt.Sprintf("%s: overrides[%s].name %q may only contain letters, digits, _ and -", where, spec, o.Name))
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n  "))
	}
	return nil
}

// CollectionDir is the absolute collection directory.
func (c *Config) CollectionDir() string { return c.Abs(c.Collection.Path) }

// Abs resolves a path relative to the config file.
func (c *Config) Abs(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.Dir, p)
}

func repoNameFrom(r *Repo) string {
	src := r.Path
	if r.Git != "" {
		src = r.Git
	}
	src = strings.TrimSuffix(strings.TrimRight(src, "/"), ".git")
	if i := strings.LastIndexAny(src, "/:"); i >= 0 {
		src = src[i+1:]
	}
	if src == "" || src == "." {
		return "repo"
	}
	return src
}

// Starter is the config written by `brunosync init`.
const Starter = `# brunosync configuration. See DESIGN.md for the full reference.
version: 1

collection:
  path: ./collection        # Bruno collection folder (created on first sync)
  name: Platform APIs

sync:
  removed: archive          # archive | delete | keep
  requireOperationIds: false

repos:
  # A local checkout:
  - path: ../backend
    # specs: ["**/openapi.yml", "**/openapi.yaml"]   # default
    # exclude: ["**/legacy/**"]
    # overrides:
    #   orders/src/main/resources/openapi.yml: { name: orders, folder: "Orders" }
    #   tools/openapi.yml: { skip: true }

  # A remote repo, cloned with your normal git credentials:
  # - git: git@gitlab.example.com:platform/flight-dynamics.git
  #   ref: main
`
