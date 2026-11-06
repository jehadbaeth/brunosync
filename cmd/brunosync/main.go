// Command brunosync keeps one Bruno collection in sync with the OpenAPI specs
// of many repositories.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/config"
	"github.com/jehadbaeth/brunosync/internal/gen"
	"github.com/jehadbaeth/brunosync/internal/output"
	"github.com/jehadbaeth/brunosync/internal/source"
	"github.com/jehadbaeth/brunosync/internal/syncer"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `brunosync keeps a Bruno collection in sync with OpenAPI specs.

Usage:
  brunosync <command> [flags]

Commands:
  init       write a starter brunosync.yml in the current directory
  validate   load every configured spec and report problems, writes nothing
  sync       generate and merge requests into the collection
  version    print the version

Run "brunosync <command> -h" for the flags of a command.
`

// Exit codes. With --exit-code, sync reports changes and conflicts too.
const (
	exitOK        = 0
	exitError     = 1
	exitUsage     = 2
	exitChanges   = 10
	exitConflicts = 20
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "sync":
		return cmdSync(args[1:], stdout, stderr)
	case "validate", "check":
		return cmdValidate(args[1:], stdout, stderr)
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "debug":
		return cmdDebug(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "brunosync %s\n", version)
		return exitOK
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
}

func configFlag(fs *flag.FlagSet) *string {
	p := fs.String("config", config.DefaultFile, "path to the config file")
	fs.StringVar(p, "c", config.DefaultFile, "shorthand for --config")
	return p
}

func cmdSync(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := configFlag(fs)
	dryRun := fs.Bool("dry-run", false, "show what would change without writing anything")
	reportPath := fs.String("report", "", "also write a Markdown report to this file (for merge request descriptions)")
	reportJSON := fs.String("report-json", "", "also write the report as JSON to this file")
	offline := fs.Bool("offline", false, "do not fetch git repos, use the cached clones")
	exitCode := fs.Bool("exit-code", false, "exit with 10 when files changed and 20 when there are conflicts")
	quiet := fs.Bool("q", false, "only print the summary")
	outType := fs.String("output", "", "where the result goes: dir, zip or git (overrides output.type)")
	zipPath := fs.String("zip", "", "write the collection to this zip file (implies --output zip)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if *zipPath != "" {
		cfg.Output.Type = config.OutputZip
		cfg.Output.Zip.Path = *zipPath
	}
	if *outType != "" {
		cfg.Output.Type = *outType
	}
	cfg.ApplyOutputDefaults()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	logf := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(stderr, "  "+format+"\n", a...)
		}
	}

	var gitTarget *output.GitTarget
	if cfg.Output.Type == config.OutputGit {
		gitTarget, err = output.PrepareGit(cfg.Output.Git, output.GitOptions{Log: logf})
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitError
		}
		cfg.Collection.Path = gitTarget.Coll
	}

	rep, runErr := syncer.Run(cfg, syncer.Options{
		DryRun: *dryRun,
		Source: source.Options{Offline: *offline},
		Log:    logf,
	})
	if rep != nil {
		rep.WriteText(stdout)
		if *reportPath != "" {
			f, err := os.Create(*reportPath)
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return exitError
			}
			rep.WriteMarkdown(f)
			_ = f.Close()
		}
		if *reportJSON != "" {
			f, err := os.Create(*reportJSON)
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return exitError
			}
			err = rep.WriteJSON(f)
			_ = f.Close()
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return exitError
			}
		}
	}
	if runErr != nil {
		fmt.Fprintln(stderr, "error:", runErr)
		return exitError
	}

	// Publish even when some specs failed: their requests were left untouched,
	// the others are up to date. The exit code still reports the failure.
	if !*dryRun {
		switch cfg.Output.Type {
		case config.OutputZip:
			zp := cfg.Abs(cfg.Output.Zip.Path)
			n, err := output.Zip(cfg.CollectionDir(), zp, cfg.Collection.Name)
			if err != nil {
				fmt.Fprintln(stderr, "error: writing zip:", err)
				return exitError
			}
			fmt.Fprintf(stdout, "wrote %s (%d files)\n", zp, n)
		case config.OutputGit:
			var body strings.Builder
			rep.WriteMarkdown(&body)
			sha, err := gitTarget.Publish(body.String())
			if err != nil {
				fmt.Fprintln(stderr, "error:", err)
				return exitError
			}
			if sha != "" {
				pushed := "committed"
				if cfg.Output.Git.ShouldPush() {
					pushed = "committed and pushed"
				}
				fmt.Fprintf(stdout, "%s %s to %s\n", pushed, sha, cfg.Output.Git.URL)
			}
		}
	}

	if len(rep.Errors) > 0 {
		return exitError
	}
	if *exitCode {
		if rep.ConflictCount() > 0 {
			return exitConflicts
		}
		if rep.HasChanges() {
			return exitChanges
		}
	}
	return exitOK
}

func cmdValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := configFlag(fs)
	offline := fs.Bool("offline", false, "do not fetch git repos, use the cached clones")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	res := syncer.LoadAll(cfg, source.Options{Offline: *offline, Log: func(f string, a ...any) {
		fmt.Fprintf(stderr, "  "+f+"\n", a...)
	}})
	fmt.Fprintf(stdout, "Config OK: %d repo(s), collection at %s\n\n", len(cfg.Repos), cfg.CollectionDir())
	sort.Slice(res.Services, func(i, j int) bool { return res.Services[i].Service.Name < res.Services[j].Service.Name })
	for _, l := range res.Services {
		s := l.Service
		fmt.Fprintf(stdout, "%-24s %3d operations  %-28s %s\n", s.Name, len(s.Operations), "\""+s.Folder+"\"", s.Source)
		for _, w := range s.Warnings {
			fmt.Fprintf(stdout, "    warning: %s\n", w)
		}
	}
	if len(cfg.Environments) > 0 {
		var outputs []gen.Output
		for _, l := range res.Services {
			outputs = append(outputs, gen.Service(l.Service, gen.FileSafe(l.Service.Name)))
		}
		fmt.Fprintln(stdout, "\nEnvironments:")
		for _, env := range cfg.Environments {
			vars, warnings := syncer.EnvVariables(env, outputs, res.FailedServices)
			fmt.Fprintf(stdout, "  %-22s %3d variables\n", env.Name, len(vars))
			if len(res.Errors) > 0 {
				continue // missing services are expected while specs fail to load
			}
			for _, w := range warnings {
				fmt.Fprintf(stdout, "    warning: %s\n", w)
			}
		}
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintln(stdout, "\nSkipped:")
		for _, s := range res.Skipped {
			fmt.Fprintf(stdout, "  %s\n", s)
		}
	}
	if len(res.Errors) > 0 {
		fmt.Fprintln(stdout, "\nErrors:")
		for _, e := range res.Errors {
			fmt.Fprintf(stdout, "  %s\n", strings.ReplaceAll(e, "\n", "\n  "))
		}
		return exitError
	}
	return exitOK
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if _, err := os.Stat(*cfgPath); err == nil {
		fmt.Fprintf(stderr, "error: %s already exists\n", *cfgPath)
		return exitError
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	if err := os.WriteFile(*cfgPath, []byte(config.Starter), 0o644); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitError
	}
	fmt.Fprintf(stdout, "Wrote %s. Edit the repos section, then run `brunosync validate`.\n", *cfgPath)
	return exitOK
}
