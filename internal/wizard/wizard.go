// Package wizard edits brunosync.yml interactively. It only writes the config
// file. Every answer is checked right away (folders exist, git URLs answer,
// branches exist, repos contain specs, names are free), and everything it
// shows comes from the same loader sync uses, so what you see is what sync
// will do.
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
	// UI to talk through. When nil, a plain line based UI on In and Out.
	UI      prompt.UI
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
	ui      prompt.UI
	path    string
	cfg     *config.Config // as written in the file, without defaults
	exists  bool
	offline bool
	fetched map[string]bool    // git sources fetched in this session
	res     *syncer.LoadResult // nil when the repos or overrides changed
}

// Run starts the wizard. It creates the config file when it does not exist
// and edits it otherwise. When the user quits early it returns
// prompt.ErrAborted and writes nothing.
func Run(opt Options) (Result, error) {
	abs, err := filepath.Abs(opt.Path)
	if err != nil {
		return Result{}, err
	}
	ui := opt.UI
	if ui == nil {
		ui = prompt.New(opt.In, opt.Out)
	}
	w := &wizard{ui: ui, path: abs, offline: opt.Offline, fetched: map[string]bool{}}
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
		w.ui.Info("Stopped, nothing was saved.")
	}
	return res, err
}

func (w *wizard) run() (Result, error) {
	if w.exists {
		w.ui.Banner("brunosync configure",
			"Editing "+w.path,
			"Saving rewrites it without comments and keeps the old one as "+filepath.Base(w.path)+".bak.")
	} else {
		w.ui.Banner("brunosync setup",
			"Writes "+w.path,
			"",
			"1. collection name and folder",
			"2. repositories with OpenAPI specs (checked as you add them)",
			"3. service names",
			"4. environments: hosts and ports for staging, prod, ...",
			"5. where the collection goes: folder, zip or git")
		for _, step := range []func() error{w.editCollection, w.editRepos, w.editServices, w.editEnvironments, w.editOutput} {
			if err := step(); err != nil {
				return Result{}, err
			}
		}
	}
	for {
		w.summary()
		i, err := w.ui.Choose("What next?", []string{
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
		switch i {
		case 0:
			saved, err := w.save()
			if err != nil {
				return Result{}, err
			}
			if !saved {
				continue
			}
			question := "Run brunosync sync now?"
			if w.cfg.Output.Type == config.OutputGit {
				question = "Run brunosync sync now? It commits and pushes to " + w.cfg.Output.Git.URL
			}
			runSync, err := w.ui.Confirm(question, w.cfg.Output.Type != config.OutputGit)
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
			quit, err := w.ui.Confirm("Quit without saving?", false)
			if err != nil {
				return Result{}, err
			}
			if quit {
				w.ui.Info("Nothing was saved.")
				return Result{Path: w.path}, nil
			}
		}
		if err != nil {
			return Result{}, err
		}
	}
}

// load returns the loader result for the current config, loading it behind
// a spinner when the repos or overrides changed. Git repos are fetched once
// per session.
func (w *wizard) load() (*syncer.LoadResult, error) {
	if w.res != nil {
		return w.res, nil
	}
	prepared, err := config.Prepared(w.cfg)
	if err != nil {
		// Only possible while no repo is configured.
		w.res = &syncer.LoadResult{FailedServices: map[string]bool{}, FailedRepos: map[string]bool{}}
		return w.res, nil
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
	label := "Loading specs"
	if !offline {
		label = "Fetching repositories and loading specs"
	}
	var res *syncer.LoadResult
	_, err = w.ui.Task(label, func() (string, error) {
		res = syncer.LoadAll(prepared, source.Options{Offline: offline})
		ops := 0
		for _, l := range res.Services {
			ops += len(l.Service.Operations)
		}
		return fmt.Sprintf("%d service(s), %d request(s)", len(res.Services), ops), nil
	})
	if err != nil {
		return nil, err
	}
	for _, r := range prepared.Repos {
		if r.Git != "" && !res.FailedRepos[r.Name] {
			w.fetched[r.Git+"@"+r.Ref] = true
		}
	}
	w.res = res
	return res, nil
}

func (w *wizard) changed() { w.res = nil }

// ---- collection

func (w *wizard) editCollection() error {
	w.ui.Section("Collection")
	name, err := w.ui.AskValid("Name shown in Bruno", or(w.cfg.Collection.Name, "APIs"), required)
	if err != nil {
		return err
	}
	path, err := w.ui.AskValid("Folder for the collection (relative to the config file)", or(w.cfg.Collection.Path, "./collection"), w.checkCollectionDir)
	if err != nil {
		return err
	}
	w.cfg.Collection.Name, w.cfg.Collection.Path = name, path
	return nil
}

func (w *wizard) checkCollectionDir(s string) (string, error) {
	if s == "" {
		return "", errors.New("a folder is required")
	}
	dir := w.cfg.Abs(s)
	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "will be created on the first sync", nil
	case err != nil:
		return "", err
	case !st.IsDir():
		return "", fmt.Errorf("%s is a file", s)
	}
	if _, err := os.Stat(filepath.Join(dir, syncer.StateDir, "manifest.json")); err == nil {
		return "existing brunosync collection, the next sync merges into it", nil
	}
	if _, err := os.Stat(filepath.Join(dir, "opencollection.yml")); err == nil {
		return "existing Bruno collection, brunosync adds its requests next to yours", nil
	}
	return "folder exists, the collection is created inside it", nil
}

// ---- repositories

func (w *wizard) editRepos() error {
	for {
		w.ui.Section("Repositories")
		if len(w.cfg.Repos) == 0 {
			w.ui.Info("Add the repositories that hold your OpenAPI specs. Local checkouts and git URLs both work.")
			if err := w.addRepo(); err != nil {
				return err
			}
			continue
		}
		for _, r := range w.cfg.Repos {
			w.ui.OK("%s  %s", config.RepoName(r), repoSource(r))
		}
		i, err := w.ui.Choose("Repositories", []string{"Add another repository", "Remove a repository", "Done"}, 2)
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

// checkSource validates a repo answer the moment it is given: a folder must
// exist, a git URL must answer with your credentials.
func (w *wizard) checkSource(s string) (string, error) {
	if s == "" {
		return "", errors.New("enter a folder or a git URL")
	}
	for _, r := range w.cfg.Repos {
		if r.Path == s || r.Git == s {
			return "", fmt.Errorf("%s is already added", s)
		}
	}
	if isGitURL(s) {
		if w.offline {
			return "offline, not checked", nil
		}
		branch, err := source.Probe(s)
		if err != nil {
			return "", fmt.Errorf("cannot reach it: %v", err)
		}
		if branch == "" {
			return "reachable, but the repository is empty", nil
		}
		return "reachable, default branch " + branch, nil
	}
	st, err := os.Stat(w.cfg.Abs(s))
	if err != nil || !st.IsDir() {
		return "", fmt.Errorf("no folder %s (relative paths start at %s)", s, w.cfg.Dir)
	}
	return "folder found", nil
}

func (w *wizard) addRepo() error {
	src, err := w.ui.AskValid("Local folder or git URL (git@host:group/repo.git)", "", w.checkSource)
	if err != nil {
		return err
	}
	var r config.Repo
	if isGitURL(src) {
		r.Git = src
		r.Ref, err = w.ui.AskValid("Branch or tag (empty for the default branch)", "", func(ref string) (string, error) {
			if ref == "" {
				return "", nil
			}
			if w.offline {
				return "offline, not checked", nil
			}
			ok, err := source.HasRef(src, ref)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("no branch or tag %s in %s", ref, src)
			}
			return "found", nil
		})
		if err != nil {
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
		name, err := w.ui.AskValid("A repository named "+config.RepoName(r)+" exists already. Name for this one", "", func(s string) (string, error) {
			if s == "" || taken[s] {
				return "", errors.New("enter a name that is not used yet")
			}
			return "", nil
		})
		if err != nil {
			return err
		}
		r.Name = name
	}
	w.cfg.Repos = append(w.cfg.Repos, r)
	return w.scanRepo(len(w.cfg.Repos) - 1)
}

// scanRepo looks for specs in a newly added repo and reports them. When none
// match the patterns it asks for others, checking each answer on the spot.
func (w *wizard) scanRepo(idx int) error {
	name := config.RepoName(w.cfg.Repos[idx])
	for {
		w.changed()
		res, err := w.load()
		if err != nil {
			return err
		}
		if !res.FailedRepos[name] {
			w.printSpecs(res, name)
			return nil
		}
		repoErr := ""
		for _, e := range res.Errors {
			if strings.HasPrefix(e, "repo "+name+":") {
				repoErr = strings.TrimPrefix(e, "repo "+name+": ")
			}
		}
		w.ui.Fail("%s: %s", name, repoErr)
		if strings.HasPrefix(repoErr, "no spec files match") {
			w.ui.Info("Specs are found by file name. ** matches any folders, e.g. **/api/*.yaml")
			patterns, err := w.ui.AskValid("File patterns, comma separated (empty removes the repository)", "", func(s string) (string, error) {
				if s == "" {
					return "", nil
				}
				return w.countMatches(idx, splitList(s))
			})
			if err != nil {
				return err
			}
			if p := splitList(patterns); len(p) > 0 {
				w.cfg.Repos[idx].Specs = p
				continue
			}
		} else {
			keep, err := w.ui.Confirm("Keep this repository anyway?", false)
			if err != nil {
				return err
			}
			if keep {
				return nil
			}
		}
		w.cfg.Repos = append(w.cfg.Repos[:idx], w.cfg.Repos[idx+1:]...)
		w.changed()
		w.ui.Info("Removed %s.", name)
		return nil
	}
}

// countMatches checks spec patterns against a repo that is already on disk.
func (w *wizard) countMatches(idx int, patterns []string) (string, error) {
	prepared, err := config.Prepared(w.cfg)
	if err != nil {
		return "", err
	}
	r := prepared.Repos[idx]
	r.Specs = patterns
	dir, err := source.Resolve(prepared, r, source.Options{Offline: true})
	if err != nil {
		return "", err
	}
	files, err := source.Discover(r, dir)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", errors.New("no file matches, try again")
	}
	names := []string{}
	for i, f := range files {
		if i == 3 {
			names = append(names, "...")
			break
		}
		names = append(names, f.Rel)
	}
	return fmt.Sprintf("%d file(s): %s", len(files), strings.Join(names, ", ")), nil
}

func (w *wizard) removeRepo() error {
	var names []string
	for _, r := range w.cfg.Repos {
		names = append(names, config.RepoName(r)+"  "+repoSource(r))
	}
	i, err := w.ui.Choose("Remove which repository?", append(names, "Cancel"), len(names))
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
func (w *wizard) printSpecs(res *syncer.LoadResult, repo string) {
	shown := 0
	for _, s := range res.Specs {
		if repo != "" && s.Repo != repo {
			continue
		}
		shown++
		where := s.Repo + ":" + s.Rel
		switch s.Status {
		case syncer.SpecOK:
			w.ui.OK("%-24s %4d requests  %s", s.Name, len(s.Service.Operations), where)
			for _, warn := range s.Service.Warnings {
				w.ui.Item("warning: %s", warn)
			}
		case syncer.SpecLibrary:
			w.ui.Info("· %-24s shared module, no requests  %s", s.Name, where)
		case syncer.SpecSkipped:
			w.ui.Info("· %-24s skipped  %s", s.Name, where)
		case syncer.SpecDuplicate:
			w.ui.Warn("%-24s NAME TAKEN (%s)  %s", s.Name, s.Detail, where)
		case syncer.SpecFailed:
			w.ui.Fail("%-24s FAILED  %s", s.Name, where)
			w.ui.Item("%s", brief(s.Detail))
		}
	}
	if shown == 0 {
		w.ui.Info("no specs found")
	}
}

func specLabel(s syncer.SpecStatus) string {
	switch s.Status {
	case syncer.SpecSkipped:
		return s.Name + " (skipped)  " + s.Rel
	case syncer.SpecDuplicate:
		return s.Name + " (name taken)  " + s.Rel
	case syncer.SpecFailed:
		return s.Name + " (failed)  " + s.Rel
	}
	return s.Name + "  " + s.Rel
}

func (w *wizard) editServices() error {
	for {
		res, err := w.load()
		if err != nil {
			return err
		}
		w.ui.Section("Services")
		w.ui.Info("Each spec becomes a folder in the collection. The name is its identity and variable prefix.")
		w.printSpecs(res, "")
		for _, e := range res.Errors {
			if strings.HasPrefix(e, "repo ") {
				w.ui.Fail("%s", brief(e))
			}
		}
		dups := 0
		for _, s := range res.Specs {
			if s.Status == syncer.SpecDuplicate {
				dups++
			}
		}
		if dups > 0 {
			w.ui.Warn("Specs marked NAME TAKEN derive the same service name and will not sync. Rename one of them.")
		}
		specs := selectable(res)
		if len(specs) == 0 {
			return nil
		}
		def := 3
		if dups > 0 {
			def = 0
		}
		i, err := w.ui.Choose("Services", []string{"Rename a service", "Skip or include a spec", "Set the folder name shown in Bruno", "Done"}, def)
		if err != nil {
			return err
		}
		if i == 3 {
			if dups == 0 {
				return nil
			}
			ok, err := w.ui.Confirm("Some specs share a name and will not sync. Continue anyway?", false)
			if err != nil || ok {
				return err
			}
			continue
		}
		var labels []string
		pick := -1
		for k, s := range specs {
			labels = append(labels, specLabel(s))
			if pick < 0 && s.Status == syncer.SpecDuplicate {
				pick = k
			}
		}
		k, err := w.ui.Choose("Which spec?", append(labels, "Cancel"), pick)
		if err != nil {
			return err
		}
		if k == len(specs) {
			continue
		}
		st := specs[k]
		switch i {
		case 0:
			err = w.renameService(res, st)
		case 1:
			skip := st.Status != syncer.SpecSkipped
			w.setOverride(st, func(o *config.Override) { o.Skip = skip })
			if skip {
				w.ui.OK("%s will be skipped", st.Rel)
			} else {
				w.ui.OK("%s will be synced again", st.Rel)
			}
		case 2:
			def := ""
			if st.Service != nil {
				def = st.Service.Folder
			}
			folder, err := w.ui.Ask("Folder name ('-' goes back to the spec title)", def)
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
	others := map[string]string{}
	for _, s := range res.Specs {
		if s.Repo != st.Repo || s.Rel != st.Rel {
			others[s.Name] = s.Repo + ":" + s.Rel
		}
	}
	derived := source.ServiceName(source.SpecFile{Repo: st.Repo, Rel: st.Rel})
	if w.exists && st.Status == syncer.SpecOK {
		w.ui.Warn("Renaming renames the folder and the %s* variables. Requests with tests or scripts move to _removed/ on the next sync.", gen.VarPrefix(st.Name))
	}
	name, err := w.ui.AskValid("Service name", st.Name, func(s string) (string, error) {
		if !config.ValidServiceName(s) {
			return "", errors.New("use letters, digits, _ and -, starting with a letter or digit")
		}
		if other, ok := others[s]; ok {
			return "", fmt.Errorf("%s is used by %s", s, other)
		}
		return "variables will be " + gen.VarPrefix(s) + "host, " + gen.VarPrefix(s) + "port, ...", nil
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
		w.ui.Section("Environments")
		w.ui.Info("Each becomes a Bruno environment that overrides the hosts and ports from the specs.")
		if len(w.cfg.Environments) == 0 {
			w.ui.Info("None yet. Without one, requests use the server URL of each spec.")
		}
		for _, e := range w.cfg.Environments {
			w.ui.OK("%s", describeEnv(e))
		}
		opts := []string{"Add an environment"}
		if len(w.cfg.Environments) > 0 {
			opts = append(opts, "Change an environment", "Remove an environment")
		}
		opts = append(opts, "Done")
		i, err := w.ui.Choose("Environments", opts, len(opts)-1)
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
			k, err := w.ui.Choose("Which environment?", append(names, "Cancel"), len(names))
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
	name, err := w.ui.AskValid("Environment name (local, staging, prod, ...)", e.Name, func(s string) (string, error) {
		if !config.ValidEnvName(s) {
			return "", errors.New("use letters, digits, space, _, . and -")
		}
		for i, o := range w.cfg.Environments {
			if i != index && strings.EqualFold(o.Name, s) {
				return "", fmt.Errorf("%s exists already", o.Name)
			}
		}
		return "file environments/" + s + ".yml", nil
	})
	if err != nil {
		return err
	}
	e.Name = name

	res, err := w.load()
	if err != nil {
		return err
	}
	services := res.Services
	if len(services) > 0 {
		w.ui.Info("Values in the specs:")
		for _, l := range services {
			w.ui.Item("%-24s %s", l.Service.Name, serverSummary(l))
		}
	}
	w.ui.Info("Values for every service. Empty keeps the grey value, '-' clears it. {service} is the service name.")
	// Only ask for what the services have: a spec URL without a port has no
	// port variable, so a port default would do nothing for it.
	keys := []string{"protocol", "host", "port"}
	if len(services) > 0 {
		has := map[string]bool{}
		var noPort []string
		for _, l := range services {
			hasPort := false
			for _, v := range l.Service.Server.Vars {
				has[v.Name] = true
				hasPort = hasPort || v.Name == "port"
			}
			if !hasPort {
				noPort = append(noPort, l.Service.Name)
			}
		}
		keys = keys[:0]
		for _, k := range []string{"protocol", "host", "port"} {
			if has[k] || e.Defaults[k] != "" {
				keys = append(keys, k)
			}
		}
		if len(noPort) > 0 {
			w.ui.Info("No port in the spec URL of %s. For those, write the port into the host: host.example.com:8443", strings.Join(noPort, ", "))
		}
	}
	for _, k := range keys {
		v, err := w.askValue(k+" for all services", e.Defaults[k], checkVar(k))
		if err != nil {
			return err
		}
		e.Defaults = setOrDelete(e.Defaults, k, v)
	}

	if len(services) > 0 {
		single, err := w.ui.Confirm("Set values for single services (another host, port or base URL)?", len(e.Services) > 0)
		if err != nil {
			return err
		}
		for single {
			var names []string
			for _, l := range services {
				label := l.Service.Name
				if n := len(e.Services[l.Service.Name]); n > 0 {
					label += fmt.Sprintf("  (%d value(s) set)", n)
				}
				names = append(names, label)
			}
			k, err := w.ui.Choose("Which service?", append(names, "Done"), len(names))
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
				val, err := w.askValue(svc.Name+" "+n, own[n], checkVar(n))
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
		w.ui.Info("Extra variables: %s", strings.Join(sortedPairs(e.Variables), ", "))
	}
	for {
		s, err := w.ui.AskValid("Extra variable as name=value (empty to finish, name= removes one)", "", func(s string) (string, error) {
			if s == "" {
				return "", nil
			}
			k, v, ok := strings.Cut(s, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return "", errors.New("write it as name=value")
			}
			if strings.TrimSpace(v) == "" {
				return "removes " + strings.TrimSpace(k), nil
			}
			return "", nil
		})
		if err != nil {
			return err
		}
		if s == "" {
			break
		}
		k, v, _ := strings.Cut(s, "=")
		e.Variables = setOrDelete(e.Variables, strings.TrimSpace(k), strings.TrimSpace(v))
	}

	// Show what the environment file will contain.
	var outputs []gen.Output
	for _, l := range services {
		outputs = append(outputs, gen.Service(l.Service, gen.FileSafe(l.Service.Name)))
	}
	vars, warnings := syncer.EnvVariables(*e, outputs, res.FailedServices)
	if len(vars) == 0 {
		w.ui.OK("%s sets nothing, requests use the spec values", e.Name)
	} else {
		w.ui.OK("%s sets %d variable(s):", e.Name, len(vars))
		for _, v := range vars {
			w.ui.Item("%s = %s", v.Name, v.Value)
		}
	}
	for _, x := range warnings {
		w.ui.Warn("%s", strings.TrimPrefix(x, "environment "+e.Name+": "))
	}
	return nil
}

// checkVar gives immediate feedback on the usual mistakes in base URL parts.
func checkVar(name string) prompt.Check {
	return func(s string) (string, error) {
		if s == "" || s == "-" || strings.Contains(s, "{{") {
			return "", nil
		}
		switch name {
		case "protocol":
			if strings.ContainsAny(s, ":/") {
				return "", errors.New("just the scheme, e.g. https")
			}
		case "port":
			for _, r := range s {
				if r < '0' || r > '9' {
					return "", errors.New("a port is a number, e.g. 8443")
				}
			}
		case "host":
			if strings.Contains(s, "://") || strings.Contains(s, "/") {
				return "", errors.New("just the host name, without protocol or path (use baseUrl for paths)")
			}
		case "baseUrl":
			if !strings.Contains(s, "://") {
				return "", errors.New("a full URL, e.g. https://gateway.example.com/orders")
			}
		}
		return "", nil
	}
}

func (w *wizard) askValue(label, cur string, check prompt.Check) (string, error) {
	v, err := w.ui.AskValid(label, cur, check)
	if v == "-" {
		v = ""
	}
	return v, err
}

// ---- output

func (w *wizard) editOutput() error {
	w.ui.Section("Output")
	cur := map[string]int{"": 0, config.OutputDir: 0, config.OutputZip: 1, config.OutputGit: 2}[w.cfg.Output.Type]
	i, err := w.ui.Choose("Where should the collection go?", []string{
		"A folder (" + or(w.cfg.Collection.Path, "./collection") + "), for local use",
		"A zip file, e.g. a CI artifact people download and open in Bruno",
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
		p, err := w.ui.AskValid("Zip file", or(w.cfg.Output.Zip.Path, def), func(s string) (string, error) {
			if !strings.HasSuffix(strings.ToLower(s), ".zip") {
				return "", errors.New("the file name should end in .zip")
			}
			return "", nil
		})
		if err != nil {
			return err
		}
		if p != def {
			out.Zip.Path = p
		}
	case 2:
		out.Type = config.OutputGit
		g := w.cfg.Output.Git
		if g.URL, err = w.ui.AskValid("Collection repository URL", g.URL, func(s string) (string, error) {
			if s == "" {
				return "", errors.New("a URL is required")
			}
			if w.offline {
				return "offline, not checked", nil
			}
			branch, err := source.Probe(s)
			if err != nil {
				return "", fmt.Errorf("cannot reach it: %v", err)
			}
			if branch == "" {
				return "reachable, empty repository (the first sync creates the branch)", nil
			}
			return "reachable, default branch " + branch, nil
		}); err != nil {
			return err
		}
		url := g.URL
		if g.Branch, err = w.ui.AskValid("Branch (empty for the default branch)", g.Branch, func(s string) (string, error) {
			if s == "" || w.offline {
				return "", nil
			}
			ok, err := source.HasRef(url, s)
			if err != nil {
				return "", err
			}
			if !ok {
				return "does not exist yet, the first sync creates it", nil
			}
			return "found", nil
		}); err != nil {
			return err
		}
		if g.Path, err = w.ui.AskValid("Collection folder inside the repository", or(g.Path, "."), func(s string) (string, error) {
			if filepath.IsAbs(s) || strings.HasPrefix(s, "/") || strings.Contains(s, "..") {
				return "", errors.New("use a relative path inside the repository")
			}
			return "", nil
		}); err != nil {
			return err
		}
		if g.Path == "." {
			g.Path = ""
		}
		push, err := w.ui.Confirm("Push after committing?", g.ShouldPush())
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
	w.ui.Section("Summary of " + filepath.Base(w.path))
	w.ui.Item("Collection    %s in %s", or(c.Collection.Name, "APIs"), or(c.Collection.Path, "./collection"))
	var repos []string
	for _, r := range c.Repos {
		repos = append(repos, config.RepoName(r))
	}
	w.ui.Item("Repositories  %s", orNone(strings.Join(repos, ", ")))
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
		w.ui.Item("Services      %s", line)
	}
	var envs []string
	for _, e := range c.Environments {
		envs = append(envs, e.Name)
	}
	w.ui.Item("Environments  %s", orNone(strings.Join(envs, ", ")))
	switch c.Output.Type {
	case config.OutputZip:
		w.ui.Item("Output        zip file %s", or(c.Output.Zip.Path, config.DefaultZipPath(or(c.Collection.Name, "APIs"))))
	case config.OutputGit:
		w.ui.Item("Output        git %s (%s)", c.Output.Git.URL, or(c.Output.Git.Branch, "default branch"))
	default:
		w.ui.Item("Output        folder %s", or(c.Collection.Path, "./collection"))
	}
}

func (w *wizard) save() (bool, error) {
	if _, err := config.Prepared(w.cfg); err != nil {
		w.ui.Fail("Cannot save yet: %v", err)
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
			w.ui.OK("No changes, %s left as it is", w.path)
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
	w.ui.OK("Saved %s", w.path)
	w.exists = true
	return true, nil
}

// ---- helpers

func required(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", errors.New("a value is required")
	}
	return "", nil
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

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
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
