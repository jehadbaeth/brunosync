// Package config loads and validates brunosync.yml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
	Version      int           `yaml:"version,omitempty"`
	Collection   Collection    `yaml:"collection,omitempty"`
	Repos        []Repo        `yaml:"repos,omitempty"`
	Environments []Environment `yaml:"environments,omitempty"`
	Output       Output        `yaml:"output,omitempty"`
	Sync         Sync          `yaml:"sync,omitempty"`

	// Dir is the directory of the config file. Relative paths resolve against it.
	Dir string `yaml:"-"`
}

// Collection describes the Bruno collection that brunosync writes.
type Collection struct {
	Path string `yaml:"path,omitempty"`
	Name string `yaml:"name,omitempty"`
}

// Sync holds merge behaviour switches.
type Sync struct {
	Removed             string `yaml:"removed,omitempty"`
	RequireOperationIDs bool   `yaml:"requireOperationIds,omitempty"`
}

// Output types: where the synced collection ends up.
const (
	OutputDir = "dir" // write to collection.path (default, local use)
	OutputZip = "zip" // sync into collection.path, then zip it (CI artifact)
	OutputGit = "git" // sync inside a clone of a collection repo, commit, push
)

// Output selects where the collection is published.
type Output struct {
	Type string    `yaml:"type,omitempty"`
	Zip  ZipOutput `yaml:"zip,omitempty"`
	Git  GitOutput `yaml:"git,omitempty"`
}

// ZipOutput writes the collection as a zip file.
type ZipOutput struct {
	Path string `yaml:"path,omitempty"` // default ./<collection name>.zip
}

// GitOutput syncs into a git repository that holds the collection.
type GitOutput struct {
	URL         string `yaml:"url,omitempty"`
	Branch      string `yaml:"branch,omitempty"`  // default: the remote's default branch
	Path        string `yaml:"path,omitempty"`    // collection folder inside the repo, default "."
	Message     string `yaml:"message,omitempty"` // commit subject
	Push        *bool  `yaml:"push,omitempty"`    // default true
	AuthorName  string `yaml:"authorName,omitempty"`
	AuthorEmail string `yaml:"authorEmail,omitempty"`
}

// ShouldPush reports whether git output pushes after committing.
func (g GitOutput) ShouldPush() bool { return g.Push == nil || *g.Push }

// Environment becomes a Bruno environment file, environments/<name>.yml.
// Values override the collection variables brunosync generates per service
// (protocol, host, port and any other server variable). A value may contain
// {service}, which is replaced with the service name.
type Environment struct {
	Name string `yaml:"name,omitempty"`
	// Defaults apply to every service that has the variable, for example
	// protocol: https for all services.
	Defaults map[string]string `yaml:"defaults,omitempty"`
	// Services holds per service values, keyed by service name, then by the
	// unprefixed variable name (host, port, ...). They win over Defaults.
	Services map[string]map[string]string `yaml:"services,omitempty"`
	// Variables are extra environment variables written as they are.
	Variables map[string]string `yaml:"variables,omitempty"`
}

// Repo is one source repository holding one or more specs.
type Repo struct {
	Name      string              `yaml:"name,omitempty"`
	Path      string              `yaml:"path,omitempty"`
	Git       string              `yaml:"git,omitempty"`
	Ref       string              `yaml:"ref,omitempty"`
	Specs     []string            `yaml:"specs,omitempty"`
	Exclude   []string            `yaml:"exclude,omitempty"`
	Overrides map[string]Override `yaml:"overrides,omitempty"`
}

// Override changes how one discovered spec is treated. Keyed by the spec path
// relative to the repo root, for example "orders/src/main/resources/openapi.yml".
type Override struct {
	Name   string `yaml:"name,omitempty"`
	Folder string `yaml:"folder,omitempty"`
	Skip   bool   `yaml:"skip,omitempty"`
}

var (
	serviceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
	// Environment names become file names, so keep them portable.
	envNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.-]*$`)
)

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	c, err := Decode(path)
	if err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Decode reads a config file as written, without defaults or validation.
// Editors (the wizard) work on this form so that saving does not bake the
// defaults into the file.
func Decode(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config file %s not found (run `brunosync init` to create one)", path)
		}
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c, err := decode(data, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse decodes config data, applies defaults and validates it. dir is the
// directory relative paths resolve against.
func Parse(data []byte, dir string) (*Config, error) {
	c, err := decode(data, dir)
	if err != nil {
		return nil, err
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func decode(data []byte, dir string) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	c.Dir = dir
	return &c, nil
}

// Marshal writes a config the way it was decoded: only what is set, no
// defaults. Comments of an existing file are not kept.
func Marshal(c *Config) ([]byte, error) {
	out := *c
	if out.Version == 0 {
		out.Version = 1
	}
	var buf bytes.Buffer
	buf.WriteString("# brunosync configuration. Reference: brunosync.config.example.yml\n# https://github.com/jehadbaeth/brunosync\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&out); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Prepared returns a copy of a decoded config with defaults applied and
// validated, ready for loading specs.
func Prepared(c *Config) (*Config, error) {
	data, err := Marshal(c)
	if err != nil {
		return nil, err
	}
	return Parse(data, c.Dir)
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
	c.ApplyOutputDefaults()
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
	switch c.Output.Type {
	case OutputDir, OutputZip:
	case OutputGit:
		if c.Output.Git.URL == "" {
			errs = append(errs, "output.git.url is required for output type git")
		}
		if strings.HasPrefix(c.Output.Git.Path, "/") || strings.Contains(c.Output.Git.Path, "..") {
			errs = append(errs, "output.git.path must be a relative path inside the repo")
		}
	default:
		errs = append(errs, fmt.Sprintf("output.type must be dir, zip or git, got %q", c.Output.Type))
	}
	if len(c.Repos) == 0 {
		errs = append(errs, "no repos configured")
	}
	envSeen := map[string]bool{}
	for i, e := range c.Environments {
		switch {
		case e.Name == "":
			errs = append(errs, fmt.Sprintf("environments[%d]: name is required", i))
		case !envNameRe.MatchString(e.Name):
			errs = append(errs, fmt.Sprintf("environments[%d]: name %q may only contain letters, digits, space, _, . and -", i, e.Name))
		case envSeen[strings.ToLower(e.Name)]:
			errs = append(errs, fmt.Sprintf("environments[%d]: duplicate name %q", i, e.Name))
		}
		envSeen[strings.ToLower(e.Name)] = true
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

// ApplyOutputDefaults fills output defaults. Call it again after changing
// the output type from the command line.
func (c *Config) ApplyOutputDefaults() {
	if c.Output.Type == "" {
		c.Output.Type = OutputDir
	}
	if c.Output.Zip.Path == "" {
		c.Output.Zip.Path = "./" + slugName(c.Collection.Name) + ".zip"
	}
	if c.Output.Git.Path == "" {
		c.Output.Git.Path = "."
	}
	if c.Output.Git.Message == "" {
		c.Output.Git.Message = "Update API collection (brunosync)"
	}
}

func slugName(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
	if s == "" {
		return "collection"
	}
	return strings.ToLower(s)
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

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

# Where the result goes. Default is the folder above (collection.path).
# output:
#   type: zip                    # dir | zip | git
#   zip: { path: ./apis.zip }    # CI artifact people download and open in Bruno
#   git:                         # sync inside a collection repo, commit and push
#     url: git@gitlab.example.com:platform/api-collection.git
#     branch: main
#     path: collection
`
