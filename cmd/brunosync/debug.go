package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

// cmdDebug holds helpers for test harnesses. Not part of the public CLI.
//
//	brunosync debug compare <dirA> <dirB>
//
// compares every .yml file under dirA with the same path under dirB using
// brunosync's own parser and equality, the way the merge sees them. Exit 1
// when any file differs. The Bruno compatibility harnesses use it to prove
// that a file saved by Bruno is unchanged for brunosync.
func cmdDebug(args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "compare" {
		fmt.Fprintln(stderr, "usage: brunosync debug compare <dirA> <dirB>")
		return exitUsage
	}
	a, b := args[1], args[2]
	n, bad := 0, 0
	err := filepath.WalkDir(a, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".brunosync" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".yml") {
			return nil
		}
		rel, _ := filepath.Rel(a, p)
		n++
		da, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		db, err := os.ReadFile(filepath.Join(b, rel))
		if err != nil {
			bad++
			fmt.Fprintf(stdout, "%s: missing in %s\n", rel, b)
			return nil
		}
		ma, errA := ocyaml.Parse(da)
		mb, errB := ocyaml.Parse(db)
		if errA != nil || errB != nil {
			bad++
			fmt.Fprintf(stdout, "%s: parse error %v %v\n", rel, errA, errB)
			return nil
		}
		if d := ocyaml.Diff(ma, mb, ""); d != "" {
			bad++
			fmt.Fprintf(stdout, "%s: %s\n", filepath.ToSlash(rel), d)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "%d files compared, %d differ\n", n, bad)
	if bad > 0 {
		return exitError
	}
	return exitOK
}
