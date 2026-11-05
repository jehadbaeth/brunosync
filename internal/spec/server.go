package spec

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	schemeRe = regexp.MustCompile(`^([^:/]+)://(.*)$`)
	tmplVar  = regexp.MustCompile(`\{([^{}]+)\}`)
)

// ServerVariable is an OpenAPI server variable.
type ServerVariable struct {
	Default     string
	Enum        []string
	Description string
}

// ParseServer turns an OpenAPI server URL into collection variables.
//
// The URL is always split into protocol, host and port variables so they can
// be changed per environment later, even when the spec hard codes them
// (for example "http://localhost:8080/api"). Server variables used anywhere
// else in the URL become variables of their own.
func ParseServer(rawURL string, vars map[string]ServerVariable, order []string) (Server, []string) {
	var warnings []string
	resolve := func(s string) string {
		return tmplVar.ReplaceAllStringFunc(s, func(m string) string {
			name := m[1 : len(m)-1]
			v, ok := vars[name]
			if !ok {
				warnings = append(warnings, fmt.Sprintf("server URL uses {%s} but the spec does not define that variable", name))
				return ""
			}
			return v.Default
		})
	}
	describe := func(component, fallback string) string {
		if m := tmplVar.FindStringSubmatch(component); m != nil && m[0] == component {
			v := vars[m[1]]
			d := v.Description
			if len(v.Enum) > 0 {
				if d != "" {
					d += " "
				}
				d += "(one of: " + strings.Join(v.Enum, ", ") + ")"
			}
			if d != "" {
				return d
			}
		}
		return fallback
	}
	used := map[string]bool{}
	markUsed := func(component string) {
		if m := tmplVar.FindStringSubmatch(component); m != nil && m[0] == component {
			used[m[1]] = true
		}
	}

	var srv Server
	u := strings.TrimSpace(rawURL)
	m := schemeRe.FindStringSubmatch(u)
	if m == nil {
		// Relative server URL ("/api" or empty): nothing says where the service
		// runs, so default to a local service.
		path := strings.TrimRight(u, "/")
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		srv.Vars = []Var{
			{Name: "protocol", Value: "http", Description: "not set in the spec"},
			{Name: "host", Value: "localhost", Description: "not set in the spec"},
		}
		srv.BaseURL = "{{protocol}}://{{host}}" + convertPath(path, vars, used, &srv, &warnings)
		return srv, warnings
	}

	scheme, rest := m[1], m[2]
	authority, path := rest, ""
	if i := strings.Index(rest, "/"); i >= 0 {
		authority, path = rest[:i], strings.TrimRight(rest[i:], "/")
	}
	host, port := splitHostPort(authority)

	markUsed(scheme)
	markUsed(host)
	srv.Vars = append(srv.Vars,
		Var{Name: "protocol", Value: resolve(scheme), Description: describe(scheme, "")},
		Var{Name: "host", Value: resolve(host), Description: describe(host, "")},
	)
	srv.BaseURL = "{{protocol}}://{{host}}"
	if port != "" {
		markUsed(port)
		srv.Vars = append(srv.Vars, Var{Name: "port", Value: resolve(port), Description: describe(port, "")})
		srv.BaseURL += ":{{port}}"
	}
	srv.BaseURL += convertPath(path, vars, used, &srv, &warnings)

	// Variables defined but never used in the URL are still exposed, since
	// people sometimes reference them from scripts.
	for _, name := range order {
		if used[name] || name == "protocol" || name == "host" || name == "port" {
			continue
		}
		v := vars[name]
		srv.Vars = append(srv.Vars, Var{Name: name, Value: v.Default, Description: v.Description})
		used[name] = true
	}
	return srv, warnings
}

func convertPath(path string, vars map[string]ServerVariable, used map[string]bool, srv *Server, warnings *[]string) string {
	return tmplVar.ReplaceAllStringFunc(path, func(m string) string {
		name := m[1 : len(m)-1]
		if !used[name] {
			v, ok := vars[name]
			if !ok {
				*warnings = append(*warnings, fmt.Sprintf("server URL uses {%s} but the spec does not define that variable", name))
			}
			srv.Vars = append(srv.Vars, Var{Name: name, Value: v.Default, Description: v.Description})
			used[name] = true
		}
		return "{{" + name + "}}"
	})
}

// splitHostPort splits "host:port", "{host}:{port}" and "[::1]:8080".
func splitHostPort(a string) (string, string) {
	if strings.HasPrefix(a, "[") {
		if i := strings.Index(a, "]"); i >= 0 {
			host := a[:i+1]
			rest := a[i+1:]
			return host, strings.TrimPrefix(rest, ":")
		}
	}
	// A colon inside a {var} must not split.
	depth := 0
	last := -1
	for i, r := range a {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case ':':
			if depth == 0 {
				last = i
			}
		}
	}
	if last < 0 {
		return a, ""
	}
	return a[:last], a[last+1:]
}
