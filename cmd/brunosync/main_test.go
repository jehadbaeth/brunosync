package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureRepo(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "test", "fixtures", "sample-repo"))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func writeConfig(t *testing.T, dir, extra string) string {
	t.Helper()
	p := filepath.Join(dir, "brunosync.yml")
	body := "version: 1\ncollection:\n  path: ./collection\n  name: Test APIs\nrepos:\n  - path: " + fixtureRepo(t) + "\n" + extra
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String() + errb.String()
}

func TestZipOutput(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "")
	zp := filepath.Join(dir, "out", "apis.zip")
	code, out := runCLI(t, "sync", "-c", cfg, "-q", "--zip", zp)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	zr, err := zip.OpenReader(zp)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	zr.Close()
	for _, want := range []string{
		"Test-APIs/opencollection.yml",
		"Test-APIs/orders-service/orders/createOrder.yml",
		"Test-APIs/.brunosync/manifest.json",
	} {
		if !names[want] {
			t.Errorf("zip misses %s (has %d entries)", want, len(names))
		}
	}

	// the same collection gives a byte identical zip
	first := fileHash(t, zp)
	if code, out := runCLI(t, "sync", "-c", cfg, "-q", "--zip", zp); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if fileHash(t, zp) != first {
		t.Error("zip output is not reproducible")
	}
}

func TestZipFromConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "output:\n  type: zip\n")
	if code, out := runCLI(t, "sync", "-c", cfg, "-q"); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "test-apis.zip")); err != nil {
		t.Fatal("default zip path not used:", err)
	}
}

func TestGitOutput(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	t.Setenv("BRUNOSYNC_CACHE", filepath.Join(dir, "cache"))
	remote := filepath.Join(dir, "remote.git")
	gitRun(t, "", "init", "--quiet", "--bare", "--initial-branch=main", remote)

	cfg := writeConfig(t, dir, "output:\n  type: git\n  git:\n    url: "+remote+"\n    branch: main\n    path: collection\n")

	// 1. first sync into an empty repo creates the branch and pushes
	code, out := runCLI(t, "sync", "-c", cfg, "-q")
	if code != 0 || !strings.Contains(out, "committed and pushed") {
		t.Fatalf("first sync: exit %d: %s", code, out)
	}
	work := filepath.Join(dir, "work")
	gitRun(t, "", "clone", "--quiet", remote, work)
	createOrder := filepath.Join(work, "collection", "orders-service", "orders", "createOrder.yml")
	if _, err := os.Stat(createOrder); err != nil {
		t.Fatal("pushed collection misses createOrder.yml")
	}

	// 2. nothing changed: no commit
	code, out = runCLI(t, "sync", "-c", cfg, "-q")
	if code != 0 || strings.Contains(out, "committed") {
		t.Fatalf("second sync should not commit: exit %d: %s", code, out)
	}

	// 3. a teammate adds a test in the collection repo; the next sync must
	//    keep it, because the merge runs against what is in the repo
	data, _ := os.ReadFile(createOrder)
	edited := string(data) + "\nruntime:\n  scripts:\n    - type: tests\n      code: test('teammate', () => {});\n"
	if err := os.WriteFile(createOrder, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "-A")
	gitRun(t, work, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "add test")
	gitRun(t, work, "push", "--quiet", "origin", "main")

	// the spec changes too
	repo := filepath.Join(dir, "repo")
	copyTree(t, fixtureRepo(t), repo)
	spec := filepath.Join(repo, "orders-service/src/main/resources/openapi.yml")
	s, _ := os.ReadFile(spec)
	if err := os.WriteFile(spec, []byte(strings.Replace(string(s), "summary: Create an order", "summary: Place an order", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgBody, _ := os.ReadFile(cfg)
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(cfgBody), fixtureRepo(t), repo, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out = runCLI(t, "sync", "-c", cfg, "-q")
	if code != 0 || !strings.Contains(out, "committed and pushed") {
		t.Fatalf("third sync: exit %d: %s", code, out)
	}
	gitRun(t, work, "pull", "--quiet")
	merged, _ := os.ReadFile(createOrder)
	if !strings.Contains(string(merged), "teammate") || !strings.Contains(string(merged), "Place an order") {
		t.Fatalf("merge through git lost something:\n%s", merged)
	}
	logOut := gitRun(t, work, "log", "-1", "--format=%s%n%b")
	if !strings.Contains(logOut, "Update API collection") || !strings.Contains(logOut, "brunosync report") {
		t.Errorf("commit message misses subject or report:\n%s", logOut)
	}
}

func TestDryRunWithOutputPublishesNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "")
	zp := filepath.Join(dir, "x.zip")
	if code, out := runCLI(t, "sync", "-c", cfg, "-q", "--dry-run", "--zip", zp); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if _, err := os.Stat(zp); err == nil {
		t.Fatal("dry run wrote a zip")
	}
}

func TestInvalidOutputType(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, dir, "")
	code, out := runCLI(t, "sync", "-c", cfg, "--output", "ftp")
	if code != exitError || !strings.Contains(out, "output.type") {
		t.Fatalf("expected a validation error, got %d: %s", code, out)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func fileHash(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
