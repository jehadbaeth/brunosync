// Package syncer runs a full sync: load specs, generate, merge into the
// collection, archive removed requests and record state.
package syncer

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/config"
	"github.com/jehadbaeth/brunosync/internal/gen"
	"github.com/jehadbaeth/brunosync/internal/merge"
	"github.com/jehadbaeth/brunosync/internal/ocyaml"
	"github.com/jehadbaeth/brunosync/internal/source"
	"github.com/jehadbaeth/brunosync/internal/spec"
)

// Options for a sync run.
type Options struct {
	DryRun bool
	Source source.Options
	Log    func(format string, args ...any)
}

// Loaded is a successfully loaded service.
type Loaded struct {
	Service *spec.Service
	Repo    string
}

// LoadResult is the outcome of loading every configured spec.
type LoadResult struct {
	Services       []Loaded
	FailedServices map[string]bool
	FailedRepos    map[string]bool
	Skipped        []string
	Errors         []string
}

// LoadAll resolves every repo and loads every spec. A failing repo or spec is
// recorded and does not stop the others.
func LoadAll(cfg *config.Config, opt source.Options) *LoadResult {
	res := &LoadResult{FailedServices: map[string]bool{}, FailedRepos: map[string]bool{}}
	bySource := map[string]string{}
	index := map[string]int{}
	for _, r := range cfg.Repos {
		dir, err := source.Resolve(cfg, r, opt)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			res.FailedRepos[r.Name] = true
			continue
		}
		files, err := source.Discover(r, dir)
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
			res.FailedRepos[r.Name] = true
			continue
		}
		if len(files) == 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("repo %s: no spec files match %v", r.Name, r.Specs))
			res.FailedRepos[r.Name] = true
			continue
		}
		for _, sf := range files {
			src := r.Name + ":" + sf.Rel
			if sf.Override.Skip {
				res.Skipped = append(res.Skipped, src+" (skip in config)")
				continue
			}
			name := source.ServiceName(sf)
			svc, err := spec.Load(sf.Abs, spec.LoadOptions{
				Name:                name,
				Folder:              sf.Override.Folder,
				Source:              src,
				RequireOperationIDs: cfg.Sync.RequireOperationIDs,
			})
			if errors.Is(err, spec.ErrNoOperations) {
				res.Skipped = append(res.Skipped, src+" (no operations, treated as a shared module)")
				continue
			}
			if err != nil {
				res.Errors = append(res.Errors, err.Error())
				res.FailedServices[name] = true
				continue
			}
			if prev, dup := bySource[name]; dup {
				res.Errors = append(res.Errors, fmt.Sprintf(
					"service name %q is derived for both %s and %s, set overrides.<spec>.name for one of them", name, prev, src))
				res.FailedServices[name] = true
				continue
			}
			bySource[name] = src
			index[name] = len(res.Services)
			res.Services = append(res.Services, Loaded{Service: svc, Repo: r.Name})
		}
	}
	// Drop services whose name turned out to be ambiguous.
	kept := res.Services[:0]
	for _, l := range res.Services {
		if !res.FailedServices[l.Service.Name] {
			kept = append(kept, l)
		}
	}
	res.Services = kept
	return res
}

type requestFile struct {
	rel    string
	doc    *ocyaml.Map
	method string
	url    string
}

type run struct {
	cfg       *config.Config
	opt       Options
	root      string
	report    *Report
	manifest  *Manifest
	next      *Manifest
	files     map[string]*requestFile // rel path -> request file
	claimed   map[string]bool
	maxSeq    map[string]int64 // dir -> highest request seq
	folders   map[string]bool  // folder dirs created in this run
	folderSeq map[string]int64 // parent dir -> highest folder seq handed out
	failed    map[string]bool  // services whose state must be kept as is
	generated map[string]bool  // request keys generated in this run
}

// Run performs a sync.
func Run(cfg *config.Config, opt Options) (*Report, error) {
	logf := opt.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	opt.Log = logf
	opt.Source.Log = logf

	loaded := LoadAll(cfg, opt.Source)
	r := &run{
		cfg:       cfg,
		opt:       opt,
		root:      cfg.CollectionDir(),
		report:    &Report{DryRun: opt.DryRun, Skipped: loaded.Skipped, Errors: loaded.Errors},
		files:     map[string]*requestFile{},
		claimed:   map[string]bool{},
		maxSeq:    map[string]int64{},
		folders:   map[string]bool{},
		folderSeq: map[string]int64{},
		failed:    map[string]bool{},
		generated: map[string]bool{},
	}

	if len(loaded.Services) == 0 && len(loaded.Errors) > 0 {
		return r.report, fmt.Errorf("no spec could be loaded")
	}

	var err error
	if r.manifest, err = loadManifest(r.root); err != nil {
		return r.report, fmt.Errorf("reading %s: %w", manifestPath(r.root), err)
	}
	r.next = newManifest()
	r.next.Folders = nil

	// Services that failed to load keep their previous state untouched.
	for name := range loaded.FailedServices {
		r.failed[name] = true
	}
	for name, st := range r.manifest.Services {
		if loaded.FailedRepos[st.Repo] {
			r.failed[name] = true
		}
	}
	for name := range r.failed {
		if st, ok := r.manifest.Services[name]; ok {
			r.next.Services[name] = st
		}
	}
	for key, st := range r.manifest.Requests {
		if r.failed[st.Service] {
			r.next.Requests[key] = st
		}
	}

	if err := r.scan(); err != nil {
		return r.report, err
	}
	for _, st := range r.manifest.Requests {
		if _, ok := r.files[st.Path]; ok {
			r.claimed[st.Path] = true
		}
	}

	var outputs []gen.Output
	for _, l := range loaded.Services {
		svc := l.Service
		out := gen.Service(svc, gen.FileSafe(svc.Name))
		outputs = append(outputs, out)
		sr := r.report.service(svc.Name, svc.Source)
		sr.Warnings = append(sr.Warnings, svc.Warnings...)
		r.next.Services[svc.Name] = ServiceState{Repo: l.Repo, Source: svc.Source, Title: svc.Title, Version: svc.Version, Dir: out.Dir}
		logf("syncing %s (%d operations)", svc.Name, len(svc.Operations))
		if err := r.syncService(out, sr); err != nil {
			return r.report, err
		}
	}

	if err := r.removeStale(); err != nil {
		return r.report, err
	}
	if err := r.cleanupFolders(outputs); err != nil {
		return r.report, err
	}
	if err := r.syncCollectionFile(outputs); err != nil {
		return r.report, err
	}
	if err := r.syncEnvironments(outputs); err != nil {
		return r.report, err
	}
	if err := r.write(StateDir+"/manifest.json", r.next.encode(), false); err != nil {
		return r.report, err
	}
	// Keep the report of the last run that did something next to the state,
	// so conflicts do not just scroll past in a terminal. A no-op run leaves
	// it alone, which keeps git quiet.
	if r.report.HasChanges() || r.report.ConflictCount() > 0 {
		var buf bytes.Buffer
		r.report.WriteMarkdown(&buf)
		if err := r.write(StateDir+"/last-report.md", buf.Bytes(), false); err != nil {
			return r.report, err
		}
	}
	return r.report, nil
}

// scan indexes every request file in the collection.
func (r *run) scan() error {
	if _, err := os.Stat(r.root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(r.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(r.root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case StateDir, ".git", "node_modules", "environments":
				if rel != "." {
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			return nil
		}
		if name == "folder.yml" || name == "opencollection.yml" || name == "collection.yml" {
			return nil
		}
		doc, err := readYAML(p)
		if err != nil {
			r.report.Warnings = append(r.report.Warnings, fmt.Sprintf("%s: cannot parse (%v), ignored", rel, err))
			return nil
		}
		if doc.Path("info.type") != "http" {
			return nil
		}
		f := &requestFile{rel: rel, doc: doc}
		f.method, _ = doc.Path("http.method").(string)
		f.url = urlPathOf(doc)
		r.files[rel] = f
		if s, ok := doc.Path("info.seq").(int64); ok {
			dir := path.Dir(rel)
			if s > r.maxSeq[dir] {
				r.maxSeq[dir] = s
			}
		}
		return nil
	})
}

// locate finds the current file of a managed request, following moves.
func (r *run) locate(st RequestState, base *ocyaml.Map) *requestFile {
	if f, ok := r.files[st.Path]; ok {
		return f
	}
	if base == nil {
		return nil
	}
	method, _ := base.Path("http.method").(string)
	url := urlPathOf(base)
	var candidates []string
	for rel, f := range r.files {
		if !r.claimed[rel] && f.method == method && f.url == url {
			candidates = append(candidates, rel)
		}
	}
	if len(candidates) != 1 {
		return nil // none, or ambiguous: do not guess
	}
	r.claimed[candidates[0]] = true
	return r.files[candidates[0]]
}

func (r *run) readBase(key string) *ocyaml.Map {
	doc, err := readYAML(filepath.Join(r.root, filepath.FromSlash(basePath(key))))
	if err != nil {
		return nil
	}
	return doc
}

func (r *run) syncService(out gen.Output, sr *ServiceReport) error {
	for _, f := range out.Folders {
		if err := r.ensureFolder(f.Dir, f.Doc); err != nil {
			return err
		}
	}
	for _, req := range out.Requests {
		r.generated[req.Key] = true
		incoming := req.Doc
		baseJSON := []byte(ocyaml.JSON(incoming) + "\n")
		st, managed := r.manifest.Requests[req.Key]

		if managed {
			base := r.readBase(req.Key)
			cur := r.locate(st, base)
			switch {
			case cur == nil:
				// Deleted by a human, or moved and edited beyond recognition.
				rel, err := r.createRequest(req, incoming)
				if err != nil {
					return err
				}
				sr.Recreated = append(sr.Recreated, rel)
				r.track(req, rel, baseJSON)
			case base == nil:
				sr.Warnings = append(sr.Warnings, fmt.Sprintf(
					"%s: base snapshot missing, file left as is. Delete the file to regenerate it.", cur.rel))
				r.next.Requests[req.Key] = RequestState{Service: req.Service, Path: cur.rel}
			default:
				if cur.rel != st.Path {
					sr.Moved = append(sr.Moved, st.Path+" -> "+cur.rel)
				}
				res := merge.Request(base, cur.doc, incoming)
				for _, c := range res.Conflicts {
					sr.Conflicts = append(sr.Conflicts, describeConflict(cur.rel, c))
				}
				if res.Changed {
					data, err := ocyaml.Marshal(res.Doc)
					if err != nil {
						return err
					}
					if err := r.write(cur.rel, data, true); err != nil {
						return err
					}
					sr.Updated = append(sr.Updated, cur.rel)
				} else {
					sr.Unchanged++
				}
				r.track(req, cur.rel, baseJSON)
			}
			continue
		}

		target := req.RelPath()
		if f, exists := r.files[target]; exists && !r.claimed[target] {
			sr.Conflicts = append(sr.Conflicts, fmt.Sprintf(
				"%s: a request file that brunosync does not manage already exists there, %s was not written. Move or delete that file.", f.rel, req.ID))
			continue
		}
		rel, err := r.createRequest(req, incoming)
		if err != nil {
			return err
		}
		sr.Added = append(sr.Added, rel)
		r.track(req, rel, baseJSON)
	}
	return nil
}

func (r *run) track(req gen.Request, rel string, baseJSON []byte) {
	r.claimed[rel] = true
	r.next.Requests[req.Key] = RequestState{Service: req.Service, Path: rel}
	_ = r.write(basePath(req.Key), baseJSON, false)
}

func (r *run) createRequest(req gen.Request, doc *ocyaml.Map) (string, error) {
	rel := req.RelPath()
	if _, taken := r.files[rel]; taken {
		ext := path.Ext(req.FileName)
		stem := strings.TrimSuffix(req.FileName, ext)
		for i := 2; ; i++ {
			rel = fmt.Sprintf("%s/%s-%d%s", req.Dir, stem, i, ext)
			if _, taken := r.files[rel]; !taken && !r.exists(rel) {
				break
			}
		}
	}
	doc = ocyaml.CloneMap(doc)
	r.maxSeq[req.Dir]++
	doc.Map("info").SetAfter("type", "seq", r.maxSeq[req.Dir])
	data, err := ocyaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	if err := r.write(rel, data, true); err != nil {
		return "", err
	}
	r.files[rel] = &requestFile{rel: rel, doc: doc, method: req.Doc.Path("http.method").(string), url: urlPathOf(req.Doc)}
	return rel, nil
}

// ensureFolder creates folder.yml for a folder brunosync owns, if missing.
// Existing folder files are never changed.
func (r *run) ensureFolder(dir string, doc *ocyaml.Map) error {
	if !containsString(r.next.Folders, dir) {
		r.next.Folders = append(r.next.Folders, dir)
	}
	rel := dir + "/folder.yml"
	if r.exists(rel) || r.folders[dir] {
		return nil
	}
	r.folders[dir] = true
	doc = ocyaml.CloneMap(doc)
	doc.Map("info").Set("seq", r.nextFolderSeq(path.Dir(dir)))
	data, err := ocyaml.Marshal(doc)
	if err != nil {
		return err
	}
	return r.write(rel, data, true)
}

// nextFolderSeq returns the next free folder seq among the siblings in parent.
// The highest existing seq is read from disk once per parent, after that the
// counter lives in memory, so dry runs number folders the same way.
func (r *run) nextFolderSeq(parent string) int64 {
	if _, ok := r.folderSeq[parent]; !ok {
		var max int64
		dir := filepath.Join(r.root, filepath.FromSlash(parent))
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() || e.Name() == removedDir {
				continue
			}
			doc, err := readYAML(filepath.Join(dir, e.Name(), "folder.yml"))
			if err != nil {
				continue
			}
			if s, ok := doc.Path("info.seq").(int64); ok && s > max {
				max = s
			}
		}
		r.folderSeq[parent] = max
	}
	r.folderSeq[parent]++
	return r.folderSeq[parent]
}

// removeStale handles requests that are no longer in any spec.
func (r *run) removeStale() error {
	keys := make([]string, 0, len(r.manifest.Requests))
	for k := range r.manifest.Requests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		st := r.manifest.Requests[key]
		if r.generated[key] || r.failed[st.Service] {
			continue
		}
		svcState := r.manifest.Services[st.Service]
		sr := r.report.service(st.Service, svcState.Source)
		base := r.readBase(key)
		cur := r.locate(st, base)
		_ = r.remove(basePath(key), false)
		if cur == nil {
			continue
		}
		switch r.cfg.Sync.Removed {
		case config.RemovedKeep:
			sr.Warnings = append(sr.Warnings, fmt.Sprintf("%s: no longer in the spec, kept and no longer managed", cur.rel))
		case config.RemovedDelete:
			if err := r.remove(cur.rel, true); err != nil {
				return err
			}
			sr.Deleted = append(sr.Deleted, cur.rel)
		default:
			if !merge.HasUserWork(base, cur.doc) {
				if err := r.remove(cur.rel, true); err != nil {
					return err
				}
				sr.Deleted = append(sr.Deleted, cur.rel)
				continue
			}
			to, err := r.archive(st.Service, cur)
			if err != nil {
				return err
			}
			sr.Archived = append(sr.Archived, cur.rel+" -> "+to)
		}
	}
	return nil
}

const removedDir = "_removed"

func (r *run) archive(service string, f *requestFile) (string, error) {
	if err := r.ensureArchiveFolder(removedDir, "_removed", "Requests whose endpoint disappeared from the spec but that had hand written work (tests, scripts or edits). Review and delete them, or move the work elsewhere."); err != nil {
		return "", err
	}
	dir := removedDir + "/" + gen.FileSafe(service)
	if err := r.ensureArchiveFolder(dir, service, ""); err != nil {
		return "", err
	}
	name := path.Base(f.rel)
	to := dir + "/" + name
	ext := path.Ext(name)
	for i := 2; r.exists(to); i++ {
		to = fmt.Sprintf("%s/%s-%d%s", dir, strings.TrimSuffix(name, ext), i, ext)
	}
	return to, r.move(f.rel, to)
}

func (r *run) ensureArchiveFolder(dir, name, docs string) error {
	if r.exists(dir+"/folder.yml") || r.folders[dir] {
		return nil
	}
	r.folders[dir] = true
	doc := ocyaml.M("info", ocyaml.M("name", name, "type", "folder", "seq", int64(9999)), "request", ocyaml.M("auth", "inherit"))
	if docs != "" {
		doc.Set("docs", ocyaml.M("content", docs, "type", "text/markdown"))
	}
	data, err := ocyaml.Marshal(doc)
	if err != nil {
		return err
	}
	return r.write(dir+"/folder.yml", data, true)
}

// cleanupFolders removes folders brunosync created that are now empty apart
// from their folder.yml (for example a tag that no longer has operations).
func (r *run) cleanupFolders(outputs []gen.Output) error {
	failedDirs := map[string]bool{}
	for name := range r.failed {
		if st, ok := r.manifest.Services[name]; ok {
			failedDirs[st.Dir] = true
		}
	}
	// Deepest first, so a service folder is checked after its tag folders.
	old := append([]string(nil), r.manifest.Folders...)
	sort.Slice(old, func(i, j int) bool { return strings.Count(old[i], "/") > strings.Count(old[j], "/") })
	for _, dir := range old {
		if containsString(r.next.Folders, dir) {
			continue
		}
		top, _, _ := strings.Cut(dir, "/")
		if failedDirs[top] {
			r.next.Folders = append(r.next.Folders, dir)
			continue
		}
		abs := filepath.Join(r.root, filepath.FromSlash(dir))
		entries, err := os.ReadDir(abs)
		if err != nil {
			continue
		}
		if len(entries) == 1 && entries[0].Name() == "folder.yml" {
			if err := r.remove(dir+"/folder.yml", true); err != nil {
				return err
			}
			if !r.opt.DryRun {
				_ = os.Remove(abs)
			}
		}
	}
	sort.Strings(r.next.Folders)
	return nil
}

// syncCollectionFile creates opencollection.yml if needed and merges the
// managed collection variables (base URL parts of every service).
func (r *run) syncCollectionFile(outputs []gen.Output) error {
	rel := "opencollection.yml"
	coll, err := readYAML(filepath.Join(r.root, rel))
	created := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s: %w", rel, err)
		}
		created = true
		coll = ocyaml.M(
			"opencollection", "1.0.0",
			"info", ocyaml.M("name", r.cfg.Collection.Name),
			"bundled", false,
			"extensions", ocyaml.M("bruno", ocyaml.M("ignore", []any{"node_modules", ".git"})),
		)
	}
	original := ocyaml.CloneMap(coll)

	// Bruno must not show the state folder as requests.
	bruno := coll.EnsureMap("extensions").EnsureMap("bruno")
	ignore := bruno.List("ignore")
	has := false
	for _, v := range ignore {
		if v == StateDir {
			has = true
		}
	}
	if !has {
		bruno.Set("ignore", append(ignore, StateDir))
	}

	var vars []any
	if coll.Map("request") != nil {
		vars = coll.Map("request").List("variables")
	}
	byName := map[string]*ocyaml.Map{}
	for _, v := range vars {
		if m, ok := v.(*ocyaml.Map); ok {
			byName[m.String("name")] = m
		}
	}

	generated := map[string]bool{}
	for _, out := range outputs {
		sr := r.report.service(out.Service.Name, out.Service.Source)
		for _, v := range out.Variables {
			generated[v.Name] = true
			r.next.Variables[v.Name] = v.Value
			baseVal, hadBase := r.manifest.Variables[v.Name]
			cur, exists := byName[v.Name]
			if !exists {
				e := ocyaml.M("name", v.Name, "value", v.Value)
				if v.Description != "" {
					e.Set("description", v.Description)
				}
				vars = append(vars, e)
				byName[v.Name] = e
				continue
			}
			var base any
			if hadBase {
				base = baseVal
			}
			val, conflict := merge.Pick(base, cur.String("value"), v.Value)
			if conflict {
				sr.Conflicts = append(sr.Conflicts, fmt.Sprintf(
					"collection variable %s: kept local value %q, spec now says %q (override per environment instead of editing collection variables)",
					v.Name, cur.String("value"), v.Value))
			}
			cur.Set("value", val)
		}
	}

	// Variables of services that disappeared.
	var kept []any
	for _, v := range vars {
		m, ok := v.(*ocyaml.Map)
		if !ok {
			kept = append(kept, v)
			continue
		}
		name := m.String("name")
		baseVal, managed := r.manifest.Variables[name]
		if !managed || generated[name] {
			kept = append(kept, v)
			continue
		}
		if owner := r.varOwner(name); owner != "" && r.failed[owner] {
			r.next.Variables[name] = baseVal
			kept = append(kept, v)
			continue
		}
		if m.String("value") == baseVal {
			continue // drop: removed from the spec and untouched
		}
		r.report.Warnings = append(r.report.Warnings, fmt.Sprintf("collection variable %s is no longer generated but was edited, kept", name))
		kept = append(kept, v)
	}

	if len(kept) > 0 {
		if coll.Map("request") == nil {
			coll.SetAfter("info", "request", ocyaml.NewMap())
		}
		coll.Map("request").Set("variables", kept)
	} else if req := coll.Map("request"); req != nil {
		req.Delete("variables")
		if req.Len() == 0 {
			coll.Delete("request")
		}
	}

	if !created && ocyaml.Equal(original, coll) {
		return nil
	}
	data, err := ocyaml.Marshal(coll)
	if err != nil {
		return err
	}
	return r.write(rel, data, true)
}

// varOwner returns the service a managed variable belongs to (longest prefix).
func (r *run) varOwner(name string) string {
	best := ""
	for svc := range r.manifest.Services {
		p := gen.VarPrefix(svc)
		if strings.HasPrefix(name, p) && len(p) > len(gen.VarPrefix(best)) {
			best = svc
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// file system helpers; every change goes through these so dry runs are exact

func (r *run) abs(rel string) string { return filepath.Join(r.root, filepath.FromSlash(rel)) }

func (r *run) exists(rel string) bool {
	_, err := os.Stat(r.abs(rel))
	return err == nil
}

// write writes a file if its content differs. visible marks collection files
// (as opposed to state files) for the change report.
func (r *run) write(rel string, data []byte, visible bool) error {
	p := r.abs(rel)
	old, err := os.ReadFile(p)
	if err == nil && bytes.Equal(old, data) {
		return nil
	}
	if visible {
		op := "update"
		if err != nil {
			op = "create"
		}
		r.report.Changes = append(r.report.Changes, Change{Op: op, Path: rel})
	}
	if r.opt.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (r *run) remove(rel string, visible bool) error {
	if !r.exists(rel) {
		return nil
	}
	if visible {
		r.report.Changes = append(r.report.Changes, Change{Op: "delete", Path: rel})
	}
	if r.opt.DryRun {
		return nil
	}
	return os.Remove(r.abs(rel))
}

func (r *run) move(from, to string) error {
	r.report.Changes = append(r.report.Changes, Change{Op: "move", Path: from, To: to})
	if r.opt.DryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.abs(to)), 0o755); err != nil {
		return err
	}
	return os.Rename(r.abs(from), r.abs(to))
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// urlPathOf returns the request URL without its query string. Moves are
// detected on method plus this, because toggling a query param in Bruno
// rewrites the query string.
func urlPathOf(doc *ocyaml.Map) string {
	s, _ := doc.Path("http.url").(string)
	p, _, _ := strings.Cut(s, "?")
	return p
}
