// Package source turns configured repos into directories on disk and finds the
// OpenAPI spec files inside them.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jehadbaeth/brunosync/internal/config"
)

// Options control how sources are resolved.
type Options struct {
	// Offline skips fetching git repos that are already cached.
	Offline bool
	// CacheDir overrides the clone cache location.
	CacheDir string
	// Log receives progress messages. May be nil.
	Log func(format string, args ...any)
}

// SpecFile is one discovered spec.
type SpecFile struct {
	Repo    string // repo name from config
	RepoDir string // absolute repo root
	Rel     string // path relative to RepoDir, forward slashes
	Abs     string // absolute path
	// Override from config, zero value if none.
	Override config.Override
}

// Resolve returns the local directory for a repo, cloning or updating git
// sources into the cache.
func Resolve(cfg *config.Config, r config.Repo, opt Options) (string, error) {
	if r.Path != "" {
		dir := cfg.Abs(r.Path)
		st, err := os.Stat(dir)
		if err != nil {
			return "", fmt.Errorf("repo %s: %w", r.Name, err)
		}
		if !st.IsDir() {
			return "", fmt.Errorf("repo %s: %s is not a directory", r.Name, dir)
		}
		return dir, nil
	}
	if !IsRemoteGit(r.Git) {
		// A git repository on this machine: relative to the config file
		// like every other path, and cached under its absolute path.
		r.Git = cfg.Abs(r.Git)
	}
	return syncGit(r, opt)
}

// IsRemoteGit tells a git URL (https://, ssh://, git@host:group/repo) from
// a path on this machine.
func IsRemoteGit(s string) bool {
	if strings.HasPrefix(s, "file://") {
		return false
	}
	if strings.Contains(s, "://") {
		return true
	}
	if filepath.IsAbs(s) || strings.HasPrefix(s, "~") || strings.HasPrefix(s, ".") {
		return false
	}
	// scp style host:path, with the colon before any slash.
	colon, slash := strings.Index(s, ":"), strings.Index(s, "/")
	return colon > 0 && (slash < 0 || colon < slash)
}

// Checkout describes a folder on this machine.
type Checkout struct {
	Repo   bool   // inside a git working tree or a bare repository
	Bare   bool   // a bare repository: there are no files to read, only refs
	Root   bool   // the folder is the top of the working tree
	Branch string // checked out branch, empty when detached
	Dirty  int    // files with uncommitted changes
}

// Inspect looks at a local folder with git. A folder that is not a
// repository, or a machine without git, gives the zero Checkout.
func Inspect(dir string) Checkout {
	var c Checkout
	out, err := gitOutput("-C", dir, "rev-parse", "--is-bare-repository", "--show-toplevel")
	if err != nil {
		return c
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	c.Repo = true
	if lines[0] == "true" {
		c.Bare = true
		return c
	}
	if len(lines) > 1 {
		top, err1 := filepath.EvalSymlinks(lines[1])
		here, err2 := filepath.EvalSymlinks(dir)
		c.Root = err1 == nil && err2 == nil && filepath.Clean(top) == filepath.Clean(here)
	}
	if b, err := gitOutput("-C", dir, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		c.Branch = strings.TrimSpace(b)
	}
	if st, err := gitOutput("-C", dir, "status", "--porcelain"); err == nil {
		for _, l := range strings.Split(st, "\n") {
			if strings.TrimSpace(l) != "" {
				c.Dirty++
			}
		}
	}
	return c
}

func cacheRoot(opt Options) (string, error) {
	if opt.CacheDir != "" {
		return opt.CacheDir, nil
	}
	if d := os.Getenv("BRUNOSYNC_CACHE"); d != "" {
		return d, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "brunosync", "repos"), nil
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func syncGit(r config.Repo, opt Options) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("repo %s: git is not installed or not on PATH", r.Name)
	}
	root, err := cacheRoot(opt)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(r.Git))
	dir := filepath.Join(root, unsafeChars.ReplaceAllString(r.Name, "_")+"-"+hex.EncodeToString(sum[:])[:10])
	ref := r.Ref
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if opt.Offline {
			logf("using cached %s (offline)", r.Name)
			return dir, nil
		}
		logf("updating %s", r.Name)
		fetchArgs := []string{"-C", dir, "fetch", "--quiet", "--depth", "1", "origin"}
		if ref != "" {
			fetchArgs = append(fetchArgs, ref)
		}
		if err := runGit(fetchArgs...); err != nil {
			return "", fmt.Errorf("repo %s: fetch failed: %w", r.Name, err)
		}
		if err := runGit("-C", dir, "checkout", "--quiet", "--force", "--detach", "FETCH_HEAD"); err != nil {
			return "", fmt.Errorf("repo %s: checkout failed: %w", r.Name, err)
		}
		return dir, nil
	}

	if opt.Offline {
		return "", fmt.Errorf("repo %s: not cached yet, run once without --offline", r.Name)
	}
	logf("cloning %s", r.Name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	args := []string{"clone", "--quiet", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, r.Git, dir)
	if err := runGit(args...); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("repo %s: clone failed: %w", r.Name, err)
	}
	return dir, nil
}

func runGit(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// Discover finds spec files in a repo directory according to the repo's globs.
func Discover(r config.Repo, dir string) ([]SpecFile, error) {
	fsys := os.DirFS(dir)
	found := map[string]bool{}
	for _, pattern := range r.Specs {
		if !doublestar.ValidatePattern(pattern) {
			return nil, fmt.Errorf("repo %s: invalid spec pattern %q", r.Name, pattern)
		}
		matches, err := doublestar.Glob(fsys, pattern, doublestar.WithFilesOnly(), doublestar.WithFailOnIOErrors())
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", r.Name, err)
		}
		for _, m := range matches {
			found[m] = true
		}
	}
	// A pattern without wildcards names one file on purpose; the default
	// excludes (dist, build, ...) must not hide it.
	literal := map[string]bool{}
	for _, p := range r.Specs {
		if !strings.ContainsAny(p, "*?[{") {
			literal[p] = true
		}
	}
	var out []SpecFile
	for rel := range found {
		if !literal[rel] && excluded(rel, r.Exclude) {
			continue
		}
		out = append(out, SpecFile{
			Repo:     r.Name,
			RepoDir:  dir,
			Rel:      rel,
			Abs:      filepath.Join(dir, filepath.FromSlash(rel)),
			Override: r.Overrides[rel],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	for rel := range r.Overrides {
		if !found[rel] {
			return out, fmt.Errorf("repo %s: override for %q matches no discovered spec", r.Name, rel)
		}
	}
	return out, nil
}

func excluded(rel string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := doublestar.Match(config.ExcludePattern(p), rel); ok {
			return true
		}
	}
	return false
}

// genericDirs are directory names that say nothing about which service a spec
// belongs to. They are skipped when deriving a service name from the path.
var genericDirs = map[string]bool{
	"src": true, "main": true, "resources": true, "resource": true, "openapi": true,
	"api": true, "apis": true, "spec": true, "specs": true, "docs": true, "doc": true,
	"static": true, "public": true, "config": true, "contract": true, "contracts": true,
	"schema": true, "schemas": true, "swagger": true, "yaml": true, "yml": true,
}

// genericFiles are spec file names that say nothing about the service.
var genericFiles = map[string]bool{
	"openapi": true, "openapi3": true, "openapiv3": true, "openapi-v3": true, "swagger": true,
	"api": true, "apis": true, "spec": true, "schema": true, "index": true, "main": true,
	"root": true, "definition": true, "oas": true, "oas3": true, "api-docs": true,
}

// ServiceName derives a service name from a spec path.
//
// A meaningful file name wins, because several specs often share one folder
// (xero-finance.yaml, xero-payroll-uk.yaml). Generic file names such as
// openapi.yml use the nearest parent directory that is not a generic one,
// else the repo name.
//
//	orders/src/main/resources/openapi.yml -> orders
//	services/billing/openapi.yml          -> billing
//	specs/xero-finance.yaml               -> xero-finance
//	openapi.yml                           -> <repo>
func ServiceName(sf SpecFile) string {
	if sf.Override.Name != "" {
		return sf.Override.Name
	}
	parts := strings.Split(sf.Rel, "/")
	file := parts[len(parts)-1]
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	if !genericFiles[strings.ToLower(stem)] {
		return Slug(stem)
	}
	for i := len(parts) - 2; i >= 0; i-- {
		p := strings.ToLower(parts[i])
		if !genericDirs[p] && p != "" {
			return Slug(parts[i])
		}
	}
	return Slug(sf.Repo)
}

var slugRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// Slug makes a string safe for file names and identities.
func Slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(s, "-"), "-")
	if s == "" {
		return "service"
	}
	return s
}

// Exists reports whether a path exists.
func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var _ fs.FS = os.DirFS(".")

// probeTimeout bounds the network checks the wizard runs while you type.
const probeTimeout = 45 * time.Second

// Probe checks that a git URL is reachable with the current credentials and
// returns its default branch ("" for an empty repository).
func Probe(url string) (string, error) {
	out, err := gitOutput("ls-remote", "--symref", url, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			branch, _, _ := strings.Cut(rest, "\t")
			return branch, nil
		}
	}
	return "", nil
}

// HasRef reports whether a branch or tag exists in a git repository.
func HasRef(url, ref string) (bool, error) {
	out, err := gitOutput("ls-remote", "--heads", "--tags", url, ref)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		_, name, ok := strings.Cut(line, "\t")
		if ok && (name == "refs/heads/"+ref || name == "refs/tags/"+ref) {
			return true, nil
		}
	}
	return false, nil
}

func gitOutput(args ...string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("git is not installed or not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND="+sshCommand())
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("no answer after %s", probeTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		msg = lastLine(msg)
		if strings.Contains(msg, "Host key verification failed") {
			msg += " (first connection to this host: run ssh -T once to accept its key, e.g. ssh -T " + sshHost(args[len(args)-2:]) + ")"
		}
		return "", fmt.Errorf("%s", msg)
	}
	return string(out), nil
}

// sshCommand keeps ssh from asking questions (host keys, passwords) that
// nobody can answer while the wizard owns the terminal.
func sshCommand() string {
	if c := os.Getenv("GIT_SSH_COMMAND"); c != "" {
		return c
	}
	return "ssh -o BatchMode=yes"
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" && !strings.HasPrefix(l, "fatal: Could not read from remote repository") && !strings.HasPrefix(l, "Please make sure you have") && !strings.HasPrefix(l, "and the repository exists") {
			return strings.TrimPrefix(l, "fatal: ")
		}
	}
	return strings.TrimSpace(s)
}

// sshHost picks user@host out of the URL among the git arguments.
func sshHost(args []string) string {
	for _, a := range args {
		if user, rest, ok := strings.Cut(a, "@"); ok && !strings.Contains(user, "/") {
			host, _, _ := strings.Cut(rest, ":")
			return user + "@" + host
		}
		if after, ok := strings.CutPrefix(a, "ssh://"); ok {
			host, _, _ := strings.Cut(after, "/")
			return host
		}
	}
	return "git@<host>"
}
