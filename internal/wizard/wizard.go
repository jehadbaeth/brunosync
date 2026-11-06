// Package wizard edits brunosync.yml interactively. It only writes the config
// file. Everything it shows (service names, operation counts, problems) comes
// from the same loader sync uses, so what you see is what sync will do.
package wizard

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/config"
	"github.com/jehadbaeth/brunosync/internal/gen"
	"github.com/jehadbaeth/brunosync/internal/prompt"
	"github.com/jehadbaeth/brunosync/internal/source"
	"github.com/jehadbaeth/brunosync/internal/syncer"
)

// Options configure a wizard run.
type Options struct {
	In      io.Reader
	Out     io.Writer
	Path    string // config file to create or edit
	Offline bool   // never fetch git repos, use the cache
}

// Result tells the caller what happened.
type Result struct {
	Saved   bool
	Path    string // absolute path of the config file
	RunSync bool   // the user wants a sync right away
}

type wizard struct {
	p       *prompt.Prompter
	out     io.Writer
	path    string
	cfg     *config.Config // as written in the file, without defaults
	exists  bool
	offline bool
	fetched map[string]bool    // git sources fetched in this session
	res     *syncer.LoadResult // nil when the repos or overrides changed
}

// Run starts the wizard. It creates the config file when it does not exist
// and edits it otherwise. When the input ends early it returns
// prompt.ErrAborted and writes nothing.
func Run(opt Options) (Result, error) {
	abs, err := filepath.Abs(opt.Path)
	if err != nil {
		return Result{}, err
	}
	w := &wizard{
		p:       prompt.New(opt.In, opt.Out),
		out:     opt.Out,
		path:    abs,
		offline: opt.Offline,
		fetched: map[string]bool{},
	}
	if _, err := os.Stat(abs); err == nil {
		if w.cfg, err = config.Decode(abs); err != nil {
			return Result{}, err
		}
		w.exists = true
	} else if errors.Is(err, os.ErrNotExist) {
		w.cfg = &config.Config{Version: 1, Dir: filepath.Dir(abs)}
	} else {
		return Result{}, err
	}
	res, err := w.run()
	if errors.Is(err, prompt.ErrAborted) {
		w.say("Stopped, nothing was saved.")
	}
	return res, err
}

func (w *wizard) say(format string, a ...any) { fmt.Fprintf(w.out, format+"\n", a...) }

func (w *wizard) run() (Result, error) {
	if w.exists {
		w.say("Editing %s.", w.path)
		w.say("Saving rewrites the file without its comments. The previous version is kept as %s.bak.", filepath.Base(w.path))
	} else {
		w.say("This wizard writes %s. It asks for:", w.path)
		w.say("  1. the collection name and folder")
		w.say("  2. the repositories holding your OpenAPI specs")
		w.say("  3. the service names found in them")
		w.say("  4. environments (staging, prod, ...) with hosts and ports")
		w.say("  5. where the collection goes (folder, zip or git)")
		w.say("Press Enter to take the value in [brackets]. Ctrl+C stops without saving.")
		for _, step := range []func() error{w.editCollection, w.editRepos, w.editServices, w.editEnvironments, w.editOutput} {
			w.say("")
			if err := step(); err != nil {
				return Result{}, err
			}
		}
	}
	for {
		w.say("")
		w.summary()
		i, err := w.p.Choose("\nWhat next?", []string{
			"Save",
			"Repositories",
			"Services",
			"Environments",
			"Output",
			"Collection name and folder",
			"Quit without saving",
		}, 0)
		if err != nil {
			return Result{}, err
		}
		w.say("")
		switch i {
		case 0:
			saved, err := w.save()
			if err != nil || !saved {
				if err != nil {
					return Result{}, err
				}
				continue
			}
			question := "Run brunosync sync now?"
			if w.cfg.Output.Type == config.OutputGit {
				question = "Run brunosync sync now? It commits and pushes to " + w.cfg.Output.Git.URL
			}
			runSync, err := w.p.Confirm(question, w.cfg.Output.Type != config.OutputGit)
			if err != nil {
				return Result{}, err
			}
			return Result{Saved: true, Path: w.path, RunSync: runSync}, nil
		case 1:
			err = w.editRepos()
		case 2:
			err = w.editServices()
		case 3:
			err = w.editEnvironments()
		case 4:
			err = w.editOutput()
		case 5:
			err = w.editCollection()
		case 6:
			quit, err := w.p.Confirm("Quit without saving?", false)
			if err != nil {
				return Result{}, err
			}
			if quit {
				w.say("Nothing was saved.")
				return Result{Path: w.path}, nil
			}
		}
		if err != nil {
			return Result{}, err
		}
	}
}

// load returns the loader result for the current config, loading it when the
// repos or overrides changed. Git repos are fetched once per session.
func (w *wizard) load() *syncer.LoadResult {
	if w.res != nil {
		return w.res
	}
	prepared, err := config.Prepared(w.cfg)
	if err != nil {
		// Only possible with repos missing; show nothing rather than fail.
		w.res = &syncer.LoadResult{FailedServices: map[string]bool{}, FailedRepos: map[string]bool{}, Errors: []string{err.Error()}}
		return w.res
	}
	offline := w.offline
	if !offline {
		offline = true
		for _, r := range prepared.Repos {
			if r.Git != "" && !w.fetched[r.Git+"@"+r.Ref] {
				offline = false
			}
		}
	}
	w.res = syncer.LoadAll(prepared, source.Options{Offline: offline, Log: func(f string, a ...any) {
		if strings.HasPrefix(f, "cloning") || strings.HasPrefix(f, "updating") {
			w.say("  "+f+" (with your git credentials)...", a...)
		}
	}})
	for _, r := range prepared.Repos {
		if r.Git != "" && !w.res.FailedRepos[r.Name] {
			w.fetched[r.Git+"@"+r.Ref] = true
		}
	}
	return w.res
}

func (w *wizard) changed() { w.res = nil }

// ---- collection

func (w *wizard) editCollection() error {
	w.say("Collection")
	name, err := w.p.AskValid("  Name shown in Bruno", or(w.cfg.Collection.Name, "APIs"), required)
	if err != nil {
		return err
	}
	path, err := w.p.AskValid("  Folder to write it to (relative to the config file)", or(w.cfg.Collection.Path, "./collection"), required)
	if err != nil {
		return err
	}
	w.cfg.Collection.Name, w.cfg.Collection.Path = name, path
	return nil
}

// ---- repositories

func (w *wizard) editRepos() error {
	for {
		w.say("Repositories")
		if len(w.cfg.Repos) == 0 {
			w.say("  none yet")
		}
		for i, r := range w.cfg.Repos {
			w.say("  %d. %-20s %s", i+1, config.RepoName(r), repoSource(r))
		}
		if len(w.cfg.Repos) == 0 {
			if err := w.addRepo(); err != nil {
				return err
			}
			continue
		}
		i, err := w.p.Choose("", []string{"Add a repository", "Remove a repository", "Done"}, 2)
		if err != nil {
			return err
		}
		switch i {
		case 0:
			err = w.addRepo()
		case 1:
			err = w.removeRepo()
		case 2:
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (w *wizard) addRepo() error {
	src, err := w.p.AskValid("  Local folder or git URL (git@host:group/repo.git)", "", func(s string) error {
		if s == "" {
			return errors.New("enter a folder or a git URL")
		}
		if !isGitURL(s) {
			if st, err := os.Stat(w.cfg.Abs(s)); err != nil || !st.IsDir() {
				return fmt.Errorf("%s is not a folder (paths are relative to %s)", s, w.cfg.Dir)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var r config.Repo
	if isGitURL(src) {
		r.Git = src
		if r.Ref, err = w.p.Ask("  Branch or tag (Enter for the default branch)", ""); err != nil {
			return err
		}
	} else {
		r.Path = src
	}
	taken := map[string]bool{}
	for _, o := range w.cfg.Repos {
		taken[config.RepoName(o)] = true
	}
	if taken[config.RepoName(r)] {
		name, err := w.p.AskValid("  A repository named "+config.RepoName(r)+" exists already. Name for this one", "", func(s string) error {
			if s == "" || taken[s] {
				return errors.New("enter a name that is not used yet")
			}
			return nil
		})
		if err != nil {
			return err
		}
		r.Name = name
	}
	w.cfg.Repos = append(w.cfg.Repos, r)
	idx := len(w.cfg.Repos) - 1
	name := config.RepoName(r)
	w.changed()

	for {
		res := w.load()
		if !res.FailedRepos[name] {
			w.printSpecs(res, name)
			return nil
		}
		for _, e := range res.Errors {
			if strings.HasPrefix(e, "repo "+name+":") {
				w.say("  %s", e)
			}
		}
		noSpecs := false
		for _, e := range res.Errors {
			if strings.HasPrefix(e, "repo "+name+": no spec files match") {
				noSpecs = true
			}
		}
		if noSpecs {
			w.say("  Specs are found by file name patterns. ** matches any folders.")
			patterns, err := w.p.List("  Patterns, comma separated (Enter to remove the repository)", nil)
			if err != nil {
				return err
			}
			if len(patterns) > 0 {
				w.cfg.Repos[idx].Specs = patterns
				w.changed()
				continue
			}
		} else {
			keep, err := w.p.Confirm("  Keep this repository anyway?", false)
			if err != nil {
				return err
			}
			if keep {
				return nil
			}
		}
		w.cfg.Repos = append(w.cfg.Repos[:idx], w.cfg.Repos[idx+1:]...)
		w.changed()
		w.say("  Removed %s.", name)
		return nil
	}
}

func (w *wizard) removeRepo() error {
	var names []string
	for _, r := range w.cfg.Repos {
		names = append(names, config.RepoName(r)+"  "+repoSource(r))
	}
	i, err := w.p.Choose("  Which one?", append(names, "Cancel"), len(names))
	if err != nil || i == len(names) {
		return err
	}
	w.cfg.Repos = append(w.cfg.Repos[:i], w.cfg.Repos[i+1:]...)
	w.changed()
	return nil
}

// ---- services

// selectable returns the specs a user can act on, in display order.
func selectable(res *syncer.LoadResult) []syncer.SpecStatus {
	var out []syncer.SpecStatus
	for _, s := range res.Specs {
		if s.Status != syncer.SpecLibrary {
			out = append(out, s)
		}
	}
	return out
}

// printSpecs lists the specs of one repo, or of all repos when repo is "".
// Selectable specs get the numbers used by the service menu.
func (w *wizard) printSpecs(res *syncer.LoadResult, repo string) {
	n := 0
	for _, s := range res.Specs {
		if s.Status != syncer.SpecLibrary {
			n++
		}
		if repo != "" && s.Repo != repo {
			continue
		}
		num := "  "
		if s.Status != syncer.SpecLibrary {
			num = fmt.Sprintf("%2d", n)
		}
		where := s.Repo + ":" + s.Rel
		switch s.Status {
		case syncer.SpecOK:
			w.say("  %s. %-24s %4d requests  %s", num, s.Name, len(s.Service.Operations), where)
			for _, warn := range s.Service.Warnings {
				w.say("        warning: %s", warn)
			}
		case syncer.SpecLibrary:
			w.say("      %-24s shared module, no requests  %s", s.Name, where)
		case syncer.SpecSkipped:
			w.say("  %s. %-24s skipped  %s", num, s.Name, where)
		case syncer.SpecDuplicate:
			w.say("  %s. %-24s NAME TAKEN (%s)  %s", num, s.Name, s.Detail, where)
		case syncer.SpecFailed:
			w.say("  %s. %-24s FAILED  %s", num, s.Name, where)
			w.say("        %s", brief(s.Detail))
		}
	}
	if len(res.Specs) == 0 {
		w.say("  no specs found")
	}
}

func (w *wizard) editServices() error {
	for {
		res := w.load()
		w.say("Services. Each spec becomes a folder in the collection.")
		w.printSpecs(res, "")
		for _, e := range res.Errors {
			if strings.HasPrefix(e, "repo ") {
				w.say("  %s", firstLine(e))
			}
		}
		dups := 0
		for _, s := range res.Specs {
			if s.Status == syncer.SpecDuplicate {
				dups++
			}
		}
		if dups > 0 {
			w.say("  Specs marked NAME TAKEN derive the same service name and will not sync. Rename one of them.")
		}
		specs := selectable(res)
		if len(specs) == 0 {
			return nil
		}
		def := 3
		if dups > 0 {
			def = 0
		}
		i, err := w.p.Choose("", []string{"Rename a service", "Skip or include a spec", "Set the folder name shown in Bruno", "Done"}, def)
		if err != nil {
			return err
		}
		if i == 3 {
			if dups == 0 {
				return nil
			}
			ok, err := w.p.Confirm("  Some specs share a name and will not sync. Continue anyway?", false)
			if err != nil || ok {
				return err
			}
			continue
		}
		n, err := w.p.AskValid("  Spec number", "", func(s string) error {
			var k int
			if _, err := fmt.Sscanf(s, "%d", &k); err != nil || k < 1 || k > len(specs) {
				return fmt.Errorf("enter a number from 1 to %d", len(specs))
			}
			return nil
		})
		if err != nil {
			return err
		}
		var k int
		fmt.Sscanf(n, "%d", &k)
		st := specs[k-1]
		switch i {
		case 0:
			err = w.renameService(res, st)
		case 1:
			skip := st.Status != syncer.SpecSkipped
			w.setOverride(st, func(o *config.Override) { o.Skip = skip })
			if skip {
				w.say("  %s will be skipped.", st.Rel)
			} else {
				w.say("  %s will be synced again.", st.Rel)
			}
		case 2:
			def := ""
			if st.Service != nil {
				def = st.Service.Folder
			}
			folder, err := w.p.Ask("  Folder name ('-' goes back to the spec title)", def)
			if err != nil {
				return err
			}
			if folder == "-" {
				folder = ""
			}
			if st.Service != nil && folder == st.Service.Title {
				folder = ""
			}
			w.setOverride(st, func(o *config.Override) { o.Folder = folder })
		}
		if err != nil {
			return err
		}
	}
}

func (w *wizard) renameService(res *syncer.LoadResult, st syncer.SpecStatus) error {
	others := map[string]bool{}
	for _, s := range res.Specs {
		if s.Repo != st.Repo || s.Rel != st.Rel {
			others[s.Name] = true
		}
	}
	derived := source.ServiceName(source.SpecFile{Repo: st.Repo, Rel: st.Rel})
	if w.exists && st.Status == syncer.SpecOK {
		w.say("  Note: the name is the service's identity. Renaming it renames its folder and variables")
		w.say("  (%s...), and requests with tests or scripts move to _removed/ on the next sync.", gen.VarPrefix(st.Name))
	}
	name, err := w.p.AskValid("  Service name", st.Name, func(s string) error {
		if !config.ValidServiceName(s) {
			return errors.New("use letters, digits, _ and -, starting with a letter or digit")
		}
		if others[s] {
			return fmt.Errorf("%s is used by another spec", s)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if name == derived {
		name = ""
	}
	w.setOverride(st, func(o *config.Override) { o.Name = name })
	return nil
}

func (w *wizard) setOverride(st syncer.SpecStatus, edit func(*config.Override)) {
	for i := range w.cfg.Repos {
		r := &w.cfg.Repos[i]
		if config.RepoName(*r) != st.Repo {
			continue
		}
		o := r.Overrides[st.Rel]
		edit(&o)
		if r.Overrides == nil {
			r.Overrides = map[string]config.Override{}
		}
		if o == (config.Override{}) {
			delete(r.Overrides, st.Rel)
		} else {
			r.Overrides[st.Rel] = o
		}
		if len(r.Overrides) == 0 {
			r.Overrides = nil
		}
	}
	w.changed()
}

// ---- environments

func (w *wizard) editEnvironments() error {
	for {
		w.say("Environments. Each becomes a Bruno environment that overrides the hosts and ports from the specs.")
		if len(w.cfg.Environments) == 0 {
			w.say("  none yet (requests then use the server URL of each spec)")
		}
		for i, e := range w.cfg.Environments {
			w.say("  %d. %s", i+1, describeEnv(e))
		}
		opts := []string{"Add an environment"}
		if len(w.cfg.Environments) > 0 {
			opts = append(opts, "Change an environment", "Remove an environment")
		}
		opts = append(opts, "Done")
		i, err := w.p.Choose("", opts, len(opts)-1)
		if err != nil {
			return err
		}
		switch opts[i] {
		case "Done":
			return nil
		case "Add an environment":
			e := config.Environment{}
			if err := w.editEnv(&e, -1); err != nil {
				return err
			}
			w.cfg.Environments = append(w.cfg.Environments, e)
		case "Change an environment", "Remove an environment":
			var names []string
			for _, e := range w.cfg.Environments {
				names = append(names, e.Name)
			}
			k, err := w.p.Choose("  Which one?", append(names, "Cancel"), len(names))
			if err != nil {
				return err
			}
			if k == len(names) {
				continue
			}
			if opts[i] == "Remove an environment" {
				w.cfg.Environments = append(w.cfg.Environments[:k], w.cfg.Environments[k+1:]...)
				continue
			}
			e := w.cfg.Environments[k]
			if err := w.editEnv(&e, k); err != nil {
				return err
			}
			w.cfg.Environments[k] = e
		}
	}
}

// editEnv edits e in place. index is its position, or -1 for a new one.
func (w *wizard) editEnv(e *config.Environment, index int) error {
	name, err := w.p.AskValid("  Name (for example local, staging, prod)", e.Name, func(s string) error {
		if !config.ValidEnvName(s) {
			return errors.New("use letters, digits, space, _, . and -")
		}
		for i, o := range w.cfg.Environments {
			if i != index && strings.EqualFold(o.Name, s) {
				return fmt.Errorf("%s exists already", o.Name)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.Name = name

	res := w.load()
	services := res.Services
	if len(services) > 0 {
		w.say("  Values from the specs:")
		for _, l := range services {
			w.say("    %-24s %s", l.Service.Name, serverSummary(l))
		}
	}
	w.say("  Values for every service. Enter keeps the value in [brackets], '-' clears it.")
	w.say("  {service} is replaced by the service name, e.g. {service}.staging.example.com")
	for _, k := range []string{"protocol", "host", "port"} {
		v, err := w.askValue("    "+k, e.Defaults[k])
		if err != nil {
			return err
		}
		e.Defaults = setOrDelete(e.Defaults, k, v)
	}

	if len(services) > 0 {
		single, err := w.p.Confirm("  Set values for single services (a different host, port or base URL)?", len(e.Services) > 0)
		if err != nil {
			return err
		}
		for single {
			var names []string
			for _, l := range services {
				names = append(names, l.Service.Name)
			}
			k, err := w.p.Choose("  Which service?", append(names, "Done"), len(names))
			if err != nil {
				return err
			}
			if k == len(names) {
				break
			}
			svc := services[k].Service
			own := e.Services[svc.Name]
			// baseUrl replaces the whole base URL, e.g. behind a gateway path.
			varNames := []string{}
			for _, v := range svc.Server.Vars {
				varNames = append(varNames, v.Name)
			}
			for _, n := range append(varNames, "baseUrl") {
				val, err := w.askValue("    "+n, own[n])
				if err != nil {
					return err
				}
				own = setOrDelete(own, n, val)
			}
			if e.Services == nil {
				e.Services = map[string]map[string]string{}
			}
			if len(own) == 0 {
				delete(e.Services, svc.Name)
			} else {
				e.Services[svc.Name] = own
			}
			if len(e.Services) == 0 {
				e.Services = nil
			}
		}
	}

	if len(e.Variables) > 0 {
		w.say("  Extra variables: %s", strings.Join(sortedPairs(e.Variables), ", "))
	}
	for {
		s, err := w.p.Ask("  Extra variable as name=value (Enter to finish, name= removes one)", "")
		if err != nil {
			return err
		}
		if s == "" {
			break
		}
		k, v, ok := strings.Cut(s, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			w.say("  write it as name=value")
			continue
		}
		e.Variables = setOrDelete(e.Variables, k, strings.TrimSpace(v))
	}

	// Show what the environment file will contain.
	var outputs []gen.Output
	for _, l := range services {
		outputs = append(outputs, gen.Service(l.Service, gen.FileSafe(l.Service.Name)))
	}
	vars, warnings := syncer.EnvVariables(*e, outputs, res.FailedServices)
	if len(vars) == 0 {
		w.say("  %s sets nothing, requests use the spec values.", e.Name)
	} else {
		w.say("  %s will set:", e.Name)
		for _, v := range vars {
			w.say("    %s = %s", v.Name, v.Value)
		}
	}
	for _, x := range warnings {
		w.say("  warning: %s", x)
	}
	return nil
}

func (w *wizard) askValue(label, cur string) (string, error) {
	v, err := w.p.Ask(label, cur)
	if v == "-" {
		v = ""
	}
	return v, err
}

// ---- output

func (w *wizard) editOutput() error {
	w.say("Output")
	cur := map[string]int{"": 0, config.OutputDir: 0, config.OutputZip: 1, config.OutputGit: 2}[w.cfg.Output.Type]
	i, err := w.p.Choose("  Where should the collection go?", []string{
		"A folder (" + or(w.cfg.Collection.Path, "./collection") + "), for local use",
		"A zip file, for example a CI artifact people download and open in Bruno",
		"A git repository: sync inside a clone, commit and push",
	}, cur)
	if err != nil {
		return err
	}
	out := config.Output{}
	switch i {
	case 1:
		out.Type = config.OutputZip
		def := config.DefaultZipPath(or(w.cfg.Collection.Name, "APIs"))
		p, err := w.p.Ask("  Zip file", or(w.cfg.Output.Zip.Path, def))
		if err != nil {
			return err
		}
		if p != def {
			out.Zip.Path = p
		}
	case 2:
		out.Type = config.OutputGit
		g := w.cfg.Output.Git
		if g.URL, err = w.p.AskValid("  Repository URL", g.URL, required); err != nil {
			return err
		}
		if g.Branch, err = w.p.Ask("  Branch (Enter for the default branch)", g.Branch); err != nil {
			return err
		}
		if g.Path, err = w.p.AskValid("  Collection folder inside the repository", or(g.Path, "."), func(s string) error {
			if filepath.IsAbs(s) || strings.HasPrefix(s, "/") || strings.Contains(s, "..") {
				return errors.New("use a relative path inside the repository")
			}
			return nil
		}); err != nil {
			return err
		}
		if g.Path == "." {
			g.Path = ""
		}
		push, err := w.p.Confirm("  Push after committing?", g.ShouldPush())
		if err != nil {
			return err
		}
		g.Push = nil
		if !push {
			g.Push = &push
		}
		out.Git = g
	}
	w.cfg.Output = out
	return nil
}

// ---- summary and save

func (w *wizard) summary() {
	c := w.cfg
	w.say("Summary of %s", filepath.Base(w.path))
	w.say("  Collection    %s in %s", or(c.Collection.Name, "APIs"), or(c.Collection.Path, "./collection"))
	var repos []string
	for _, r := range c.Repos {
		repos = append(repos, config.RepoName(r))
	}
	w.say("  Repositories  %s", orNone(strings.Join(repos, ", ")))
	if w.res != nil {
		ok, problems := 0, 0
		for _, s := range w.res.Specs {
			switch s.Status {
			case syncer.SpecOK:
				ok++
			case syncer.SpecFailed, syncer.SpecDuplicate:
				problems++
			}
		}
		line := fmt.Sprintf("%d", ok)
		if problems > 0 {
			line += fmt.Sprintf(", %d with problems (see Services)", problems)
		}
		w.say("  Services      %s", line)
	}
	var envs []string
	for _, e := range c.Environments {
		envs = append(envs, e.Name)
	}
	w.say("  Environments  %s", orNone(strings.Join(envs, ", ")))
	switch c.Output.Type {
	case config.OutputZip:
		w.say("  Output        zip file %s", or(c.Output.Zip.Path, config.DefaultZipPath(or(c.Collection.Name, "APIs"))))
	case config.OutputGit:
		w.say("  Output        git %s (%s)", c.Output.Git.URL, or(c.Output.Git.Branch, "default branch"))
	default:
		w.say("  Output        folder %s", or(c.Collection.Path, "./collection"))
	}
}

func (w *wizard) save() (bool, error) {
	if _, err := config.Prepared(w.cfg); err != nil {
		w.say("Cannot save yet:\n  %v", err)
		return false, nil
	}
	data, err := config.Marshal(w.cfg)
	if err != nil {
		return false, err
	}
	if w.exists {
		old, err := os.ReadFile(w.path)
		if err != nil {
			return false, err
		}
		if string(old) == string(data) {
			w.say("No changes, %s left as it is.", w.path)
			return true, nil
		}
		if err := os.WriteFile(w.path+".bak", old, 0o644); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(w.path, data, 0o644); err != nil {
		return false, err
	}
	w.say("Saved %s.", w.path)
	w.exists = true
	return true, nil
}

// ---- helpers

func required(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("a value is required")
	}
	return nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orNone(s string) string { return or(s, "none") }

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "git@") || strings.Contains(s, "://") || strings.HasSuffix(s, ".git")
}

func repoSource(r config.Repo) string {
	if r.Git != "" {
		if r.Ref != "" {
			return r.Git + " @ " + r.Ref
		}
		return r.Git
	}
	return r.Path
}

// brief squeezes a multi line error into one short line.
func brief(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

func setOrDelete(m map[string]string, k, v string) map[string]string {
	if v == "" {
		delete(m, k)
		if len(m) == 0 {
			return nil
		}
		return m
	}
	if m == nil {
		m = map[string]string{}
	}
	m[k] = v
	return m
}

func sortedPairs(m map[string]string) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func describeEnv(e config.Environment) string {
	var parts []string
	for _, k := range []string{"protocol", "host", "port"} {
		if v := e.Defaults[k]; v != "" {
			parts = append(parts, k+" "+v)
		}
	}
	if n := len(e.Services); n > 0 {
		parts = append(parts, fmt.Sprintf("%d service(s) with own values", n))
	}
	if n := len(e.Variables); n > 0 {
		parts = append(parts, fmt.Sprintf("%d extra variable(s)", n))
	}
	if len(parts) == 0 {
		return e.Name + "  (spec values)"
	}
	return e.Name + "  " + strings.Join(parts, ", ")
}

func serverSummary(l syncer.Loaded) string {
	var parts []string
	for _, v := range l.Service.Server.Vars {
		parts = append(parts, v.Name+"="+v.Value)
	}
	return strings.Join(parts, "  ")
}
