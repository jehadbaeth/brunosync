// Package output publishes a synced collection: as a zip file (CI artifact)
// or by committing it to a git repository. The plain directory output needs
// nothing from this package.
package output

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jehadbaeth/brunosync/internal/config"
)

// zipTime is used for every entry so the same collection always produces a
// byte identical zip, which keeps CI artifacts and caches stable.
var zipTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Zip writes the collection directory into a zip file. Everything sits under
// one top level folder named after the collection, so unzipping gives a
// folder that can be opened in Bruno directly. The .brunosync state is
// included: it lets someone keep syncing a downloaded collection locally.
func Zip(collectionDir, zipPath, topFolder string) (int, error) {
	top := strings.Trim(unsafeName.ReplaceAllString(topFolder, "-"), "-")
	if top == "" {
		top = "collection"
	}
	var files []string
	err := filepath.WalkDir(collectionDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Strings(files)

	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return 0, err
	}
	tmp := zipPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, err
	}
	zw := zip.NewWriter(f)
	for _, p := range files {
		rel, _ := filepath.Rel(collectionDir, p)
		hdr := &zip.FileHeader{
			Name:     top + "/" + filepath.ToSlash(rel),
			Method:   zip.Deflate,
			Modified: zipTime,
		}
		hdr.SetMode(0o644)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			f.Close()
			return 0, err
		}
		src, err := os.Open(p)
		if err != nil {
			f.Close()
			return 0, err
		}
		_, err = io.Copy(w, src)
		src.Close()
		if err != nil {
			f.Close()
			return 0, err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	return len(files), os.Rename(tmp, zipPath)
}

// GitTarget is a prepared checkout of the collection repository.
type GitTarget struct {
	cfg  config.GitOutput
	Dir  string // clone root
	Coll string // collection directory inside the clone
	log  func(string, ...any)
}

// GitOptions control the checkout.
type GitOptions struct {
	CacheDir string
	Log      func(string, ...any)
}

// PrepareGit clones or refreshes the collection repository and returns the
// collection directory to sync into. Local leftovers in the cached clone are
// discarded: the remote is the source of truth, including edits people
// committed there, which the sync then merges with.
func PrepareGit(g config.GitOutput, opt GitOptions) (*GitTarget, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git output: git is not installed or not on PATH")
	}
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	root := opt.CacheDir
	if root == "" {
		if d := os.Getenv("BRUNOSYNC_CACHE"); d != "" {
			root = filepath.Join(d, "outputs")
		} else {
			base, err := os.UserCacheDir()
			if err != nil {
				return nil, err
			}
			root = filepath.Join(base, "brunosync", "outputs")
		}
	}
	sum := sha256.Sum256([]byte(g.URL + "#" + g.Branch))
	dir := filepath.Join(root, hex.EncodeToString(sum[:])[:12])
	t := &GitTarget{cfg: g, Dir: dir, log: logf}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		logf("cloning output repo %s", g.URL)
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		if _, err := git("", "clone", "--quiet", g.URL, dir); err != nil {
			return nil, fmt.Errorf("git output: clone %s: %w", g.URL, err)
		}
	} else {
		logf("updating output repo %s", g.URL)
		if _, err := git(dir, "fetch", "--quiet", "--prune", "origin"); err != nil {
			return nil, fmt.Errorf("git output: fetch: %w", err)
		}
	}

	branch := g.Branch
	if branch == "" {
		out, err := git(dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
		if err == nil {
			branch = strings.TrimPrefix(strings.TrimSpace(out), "origin/")
		}
		if branch == "" {
			branch = "main"
		}
	}
	t.cfg.Branch = branch

	if _, err := git(dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch); err == nil {
		if _, err := git(dir, "checkout", "--quiet", "-B", branch, "origin/"+branch); err != nil {
			return nil, fmt.Errorf("git output: checkout %s: %w", branch, err)
		}
		if _, err := git(dir, "reset", "--quiet", "--hard", "origin/"+branch); err != nil {
			return nil, err
		}
	} else {
		// Empty repository or a branch that does not exist yet.
		logf("branch %s does not exist on the remote yet, it will be created", branch)
		if _, err := git(dir, "checkout", "--quiet", "--orphan", branch); err != nil {
			if _, err2 := git(dir, "checkout", "--quiet", "-B", branch); err2 != nil {
				return nil, fmt.Errorf("git output: create branch %s: %w", branch, err)
			}
		}
	}
	if _, err := git(dir, "clean", "--quiet", "-fdx"); err != nil {
		return nil, err
	}
	t.Coll = filepath.Join(dir, filepath.FromSlash(g.Path))
	return t, nil
}

// Publish commits the collection if anything changed and pushes it. It
// returns the commit id, or "" when there was nothing to commit.
func (t *GitTarget) Publish(body string) (string, error) {
	rel, err := filepath.Rel(t.Dir, t.Coll)
	if err != nil {
		return "", err
	}
	if _, err := git(t.Dir, "add", "--all", "--", filepath.ToSlash(rel)); err != nil {
		return "", err
	}
	status, err := git(t.Dir, "status", "--porcelain", "--", filepath.ToSlash(rel))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		t.log("output repo already up to date, nothing to commit")
		return "", nil
	}
	args := t.identityArgs()
	args = append(args, "commit", "--quiet", "-m", t.cfg.Message)
	if strings.TrimSpace(body) != "" {
		args = append(args, "-m", body)
	}
	if _, err := git(t.Dir, args...); err != nil {
		return "", fmt.Errorf("git output: commit: %w", err)
	}
	sha, _ := git(t.Dir, "rev-parse", "--short", "HEAD")
	sha = strings.TrimSpace(sha)
	if t.cfg.ShouldPush() {
		t.log("pushing %s to %s", t.cfg.Branch, t.cfg.URL)
		if _, err := git(t.Dir, "push", "--quiet", "origin", "HEAD:refs/heads/"+t.cfg.Branch); err != nil {
			// Leave the cache clean so the next run starts from the remote again.
			_, _ = git(t.Dir, "reset", "--quiet", "--hard", "HEAD~1")
			return "", fmt.Errorf("git output: push: %w (someone may have pushed meanwhile, run sync again)", err)
		}
	}
	return sha, nil
}

// identityArgs sets the commit author from the config, or a neutral fallback
// when neither the config nor git has one (fresh CI runners).
func (t *GitTarget) identityArgs() []string {
	name, email := t.cfg.AuthorName, t.cfg.AuthorEmail
	if name == "" {
		if out, err := git(t.Dir, "config", "user.name"); err == nil && strings.TrimSpace(out) != "" {
			name = ""
		} else if os.Getenv("GIT_AUTHOR_NAME") == "" {
			name = "brunosync"
		}
	}
	if email == "" {
		if out, err := git(t.Dir, "config", "user.email"); err == nil && strings.TrimSpace(out) != "" {
			email = ""
		} else if os.Getenv("GIT_AUTHOR_EMAIL") == "" {
			email = "brunosync@users.noreply.github.com"
		}
	}
	var args []string
	if name != "" {
		args = append(args, "-c", "user.name="+name)
	}
	if email != "" {
		args = append(args, "-c", "user.email="+email)
	}
	return args
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", err
		}
		return string(out), fmt.Errorf("%s", msg)
	}
	return string(out), nil
}
