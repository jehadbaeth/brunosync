// Package source turns configured repos into directories on disk and finds the
// OpenAPI spec files inside them.
package source

import (
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
	return syncGit(r, opt)
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
	var out []SpecFile
	for rel := range found {
		if excluded(rel, r.Exclude) {
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
		if ok, _ := doublestar.Match(p, rel); ok {
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
