package syncer

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jehadbaeth/brunosync/internal/merge"
	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

// Report summarises one sync run.
type Report struct {
	DryRun   bool             `json:"dryRun"`
	Services []*ServiceReport `json:"services"`
	Skipped  []string         `json:"skipped"`  // specs skipped on purpose (library specs, overrides)
	Errors   []string         `json:"errors"`   // specs or repos that failed; their requests were left alone
	Warnings []string         `json:"warnings"` // collection level warnings
	Changes  []Change         `json:"changes"`  // file level changes inside the collection
}

// ServiceReport holds the outcome for one service.
type ServiceReport struct {
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Added     []string `json:"added"`
	Updated   []string `json:"updated"`
	Recreated []string `json:"recreated"`
	Moved     []string `json:"moved"`
	Deleted   []string `json:"deleted"`
	Archived  []string `json:"archived"`
	Unchanged int      `json:"unchanged"`
	Conflicts []string `json:"conflicts"`
	Warnings  []string `json:"warnings"`
}

// Change is one file system change.
type Change struct {
	Op   string `json:"op"` // create | update | delete | move
	Path string `json:"path"`
	To   string `json:"to,omitempty"`
}

// WriteJSON writes the report as JSON for scripts and dashboards.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// HasChanges reports whether anything in the collection changed.
func (r *Report) HasChanges() bool { return len(r.Changes) > 0 }

// ConflictCount returns the number of conflicts over all services.
func (r *Report) ConflictCount() int {
	n := 0
	for _, s := range r.Services {
		n += len(s.Conflicts)
	}
	return n
}

func (r *Report) service(name, source string) *ServiceReport {
	for _, s := range r.Services {
		if s.Name == name {
			return s
		}
	}
	s := &ServiceReport{Name: name, Source: source}
	r.Services = append(r.Services, s)
	return s
}

func describeConflict(file string, c merge.Conflict) string {
	short := func(v any) string {
		if v == nil {
			return "(removed)"
		}
		s := ocyaml.MustMarshalString(v)
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 120 {
			s = s[:117] + "..."
		}
		return s
	}
	return fmt.Sprintf("%s: %s changed locally and in the spec, kept local value %s, spec now says %s",
		file, c.Field, short(c.Current), short(c.Incoming))
}

// WriteText prints a human readable summary.
func (r *Report) WriteText(w io.Writer) {
	if r.DryRun {
		fmt.Fprintln(w, "Dry run, nothing was written.")
	}
	for _, s := range r.Services {
		fmt.Fprintf(w, "\n%s  (%s)\n", s.Name, s.Source)
		line := func(label string, items []string) {
			if len(items) == 0 {
				return
			}
			fmt.Fprintf(w, "  %s %d\n", label, len(items))
			for _, i := range items {
				fmt.Fprintf(w, "      %s\n", i)
			}
		}
		line("added    ", s.Added)
		line("updated  ", s.Updated)
		line("recreated", s.Recreated)
		line("moved    ", s.Moved)
		line("archived ", s.Archived)
		line("deleted  ", s.Deleted)
		if s.Unchanged > 0 {
			fmt.Fprintf(w, "  unchanged %d\n", s.Unchanged)
		}
		for _, c := range s.Conflicts {
			fmt.Fprintf(w, "  CONFLICT  %s\n", c)
		}
		for _, x := range s.Warnings {
			fmt.Fprintf(w, "  warning   %s\n", x)
		}
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintln(w, "\nSkipped:")
		for _, s := range r.Skipped {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
	if len(r.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, s := range r.Warnings {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
	if len(r.Errors) > 0 {
		fmt.Fprintln(w, "\nErrors (requests of these services were left untouched):")
		for _, s := range r.Errors {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
	verb := "changed"
	if r.DryRun {
		verb = "would change"
	}
	fmt.Fprintf(w, "\n%d file(s) %s, %d conflict(s), %d error(s)\n", len(r.Changes), verb, r.ConflictCount(), len(r.Errors))
}

// WriteMarkdown renders the report for a merge request description.
func (r *Report) WriteMarkdown(w io.Writer) {
	fmt.Fprintln(w, "## brunosync report")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%d file(s) changed, %d conflict(s), %d error(s).\n", len(r.Changes), r.ConflictCount(), len(r.Errors))
	for _, s := range r.Services {
		total := len(s.Added) + len(s.Updated) + len(s.Recreated) + len(s.Moved) + len(s.Archived) + len(s.Deleted) + len(s.Conflicts) + len(s.Warnings)
		if total == 0 {
			continue
		}
		fmt.Fprintf(w, "\n### %s\n\n`%s`\n\n", s.Name, s.Source)
		list := func(label string, items []string) {
			if len(items) == 0 {
				return
			}
			fmt.Fprintf(w, "**%s**\n\n", label)
			sorted := append([]string(nil), items...)
			sort.Strings(sorted)
			for _, i := range sorted {
				fmt.Fprintf(w, "* `%s`\n", i)
			}
			fmt.Fprintln(w)
		}
		list("Added", s.Added)
		list("Updated", s.Updated)
		list("Recreated", s.Recreated)
		list("Moved", s.Moved)
		list("Archived to _removed", s.Archived)
		list("Deleted", s.Deleted)
		if len(s.Conflicts) > 0 {
			fmt.Fprintln(w, "**Conflicts** (local value kept, please review)")
			fmt.Fprintln(w)
			for _, c := range s.Conflicts {
				fmt.Fprintf(w, "* %s\n", c)
			}
			fmt.Fprintln(w)
		}
		if len(s.Warnings) > 0 {
			fmt.Fprintln(w, "**Warnings**")
			fmt.Fprintln(w)
			for _, x := range s.Warnings {
				fmt.Fprintf(w, "* %s\n", x)
			}
			fmt.Fprintln(w)
		}
	}
	if len(r.Errors) > 0 {
		fmt.Fprint(w, "\n### Errors\n\n")
		for _, e := range r.Errors {
			fmt.Fprintf(w, "* %s\n", e)
		}
	}
}
