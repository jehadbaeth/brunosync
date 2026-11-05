package spec

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/index"
	"github.com/pb33f/libopenapi/orderedmap"
	"github.com/pb33f/libopenapi/utils"

	"github.com/jehadbaeth/brunosync/internal/ocyaml"
)

// ErrNoOperations marks a spec without paths. These are shared "common"
// modules that other specs reference, and they are skipped.
var ErrNoOperations = errors.New("spec has no operations")

// LoadOptions identify the spec being loaded.
type LoadOptions struct {
	Name   string // service name
	Folder string // folder display name, defaults to the spec title
	Source string // for messages
	// RequireOperationIDs turns a missing operationId into an error.
	RequireOperationIDs bool
}

// Load parses a spec file, resolving relative $refs into other files, and
// builds the service model.
func Load(path string, opt LoadOptions) (*Service, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	cfg := datamodel.NewDocumentConfiguration()
	cfg.BasePath = filepath.Dir(abs)
	cfg.SpecFilePath = filepath.Base(abs)
	cfg.AllowFileReferences = true
	cfg.AllowRemoteReferences = false
	cfg.IgnorePolymorphicCircularReferences = true
	cfg.IgnoreArrayCircularReferences = true
	// libopenapi logs resolution problems itself; they come back as errors
	// and are reported once by brunosync, so keep its logger quiet.
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	doc, err := libopenapi.NewDocumentWithConfiguration(data, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", opt.Source, err)
	}
	info := doc.GetSpecInfo()
	if info != nil && info.SpecFormat == datamodel.OAS2 {
		return nil, fmt.Errorf("%s: Swagger 2.0 is not supported, only OpenAPI 3.x", opt.Source)
	}
	model, buildErr := doc.BuildV3Model()
	if model == nil {
		return nil, fmt.Errorf("%s: %w", opt.Source, buildErr)
	}

	svc := &Service{Name: opt.Name, Folder: opt.Folder, Source: opt.Source}
	if buildErr != nil {
		// Circular references are fine (the example generator breaks cycles).
		// Anything else, such as a $ref to a file that does not exist, would
		// silently drop data, so it is fatal.
		var fatal []string
		for _, e := range utils.UnwrapErrors(buildErr) {
			var re *index.ResolvingError
			if errors.As(e, &re) && re.CircularReference != nil {
				continue
			}
			fatal = append(fatal, e.Error())
		}
		if len(fatal) > 0 {
			return nil, fmt.Errorf("%s: unresolved references:\n    %s", opt.Source, strings.Join(dedupe(fatal), "\n    "))
		}
	}
	d := &model.Model

	if d.Info != nil {
		svc.Title = d.Info.Title
		svc.Version = d.Info.Version
		svc.Description = d.Info.Description
	}
	if svc.Folder == "" {
		svc.Folder = svc.Title
	}
	if svc.Folder == "" {
		svc.Folder = svc.Name
	}
	for _, t := range d.Tags {
		if t != nil {
			svc.Tags = append(svc.Tags, Tag{Name: t.Name, Description: t.Description})
		}
	}

	if d.Paths == nil || d.Paths.PathItems == nil || d.Paths.PathItems.Len() == 0 {
		return svc, ErrNoOperations
	}

	svc.Server, svc.Warnings = serverFrom(d.Servers)

	seenIDs := map[string]string{}
	for p, item := range d.Paths.PathItems.FromOldest() {
		if item == nil {
			continue
		}
		for method, op := range item.GetOperations().FromOldest() {
			if op == nil {
				continue
			}
			o := buildOperation(strings.ToUpper(method), p, item, op)
			if !o.HasOperationID {
				msg := fmt.Sprintf("%s %s has no operationId, identity falls back to %q (renaming the path will lose hand written tests)", o.Method, p, o.ID)
				if opt.RequireOperationIDs {
					return nil, fmt.Errorf("%s: %s", opt.Source, msg)
				}
				svc.Warnings = append(svc.Warnings, msg)
			}
			if prev, dup := seenIDs[o.ID]; dup {
				return nil, fmt.Errorf("%s: operationId %q is used by both %s and %s %s", opt.Source, o.ID, prev, o.Method, p)
			}
			seenIDs[o.ID] = o.Method + " " + p
			if len(op.Servers) > 0 || len(item.Servers) > 0 {
				svc.Warnings = append(svc.Warnings, fmt.Sprintf("%s %s overrides servers, the service level server is used instead", o.Method, p))
			}
			svc.Operations = append(svc.Operations, o)
		}
	}
	if len(svc.Operations) == 0 {
		return svc, ErrNoOperations
	}
	return svc, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func serverFrom(servers []*v3.Server) (Server, []string) {
	if len(servers) == 0 || servers[0] == nil {
		return ParseServer("", nil, nil)
	}
	s := servers[0]
	vars := map[string]ServerVariable{}
	var order []string
	for name, v := range s.Variables.FromOldest() {
		if v == nil {
			continue
		}
		vars[name] = ServerVariable{Default: v.Default, Enum: v.Enum, Description: v.Description}
		order = append(order, name)
	}
	srv, warnings := ParseServer(s.URL, vars, order)
	if len(servers) > 1 {
		warnings = append(warnings, fmt.Sprintf("spec lists %d servers, using the first one (%s)", len(servers), s.URL))
	}
	return srv, warnings
}

func buildOperation(method, path string, item *v3.PathItem, op *v3.Operation) Operation {
	o := Operation{
		ID:             op.OperationId,
		HasOperationID: op.OperationId != "",
		Method:         method,
		Path:           path,
		Summary:        strings.TrimSpace(op.Summary),
		Description:    strings.TrimSpace(op.Description),
		Tags:           op.Tags,
		Deprecated:     op.Deprecated != nil && *op.Deprecated,
	}
	if !o.HasOperationID {
		o.ID = method + " " + path
	}

	// Path level parameters apply unless the operation redefines them.
	var params []*v3.Parameter
	overridden := map[string]bool{}
	for _, p := range op.Parameters {
		if p != nil {
			overridden[p.In+":"+p.Name] = true
		}
	}
	for _, p := range item.Parameters {
		if p != nil && !overridden[p.In+":"+p.Name] {
			params = append(params, p)
		}
	}
	params = append(params, op.Parameters...)
	for _, p := range params {
		if p == nil {
			continue
		}
		switch p.In {
		case "path", "query":
		case "header":
			switch strings.ToLower(p.Name) {
			case "accept", "content-type", "authorization":
				continue // OpenAPI says these are ignored as parameters
			}
		default:
			continue // cookie
		}
		o.Params = append(o.Params, Param{
			Name:        p.Name,
			In:          p.In,
			Required:    p.In == "path" || (p.Required != nil && *p.Required),
			Value:       paramValue(p),
			Description: strings.TrimSpace(p.Description),
		})
	}

	if op.RequestBody != nil {
		o.Body = requestBody(op.RequestBody)
	}
	if op.Responses != nil {
		for code, r := range op.Responses.Codes.FromOldest() {
			status, err := strconv.Atoi(code)
			if err != nil || r == nil {
				continue // "2XX" style ranges have no single status to show
			}
			o.Responses = append(o.Responses, response(status, r))
		}
	}
	return o
}

// paramValue only uses explicit examples. A generated placeholder such as
// "string" in a path parameter is worse than an empty field the user notices.
func paramValue(p *v3.Parameter) string {
	if p.Example != nil {
		return TextValue(NodeValue(p.Example))
	}
	if v, ok := firstExample(p.Examples); ok {
		return TextValue(v)
	}
	if p.Schema != nil {
		if s := p.Schema.Schema(); s != nil {
			if v, ok := explicitExample(s); ok {
				return TextValue(v)
			}
			if hasType(s, "array") && s.Items != nil && s.Items.IsA() {
				if is := s.Items.A.Schema(); is != nil {
					if v, ok := explicitExample(is); ok {
						return TextValue(v)
					}
				}
			}
		}
	}
	return ""
}

func firstExample(m *orderedmap.Map[string, *base.Example]) (any, bool) {
	for _, ex := range m.FromOldest() {
		if ex != nil && ex.Value != nil {
			return NodeValue(ex.Value), true
		}
	}
	return nil, false
}

// pickMedia chooses the content type Bruno can represent best.
func pickMedia(content *orderedmap.Map[string, *v3.MediaType]) (string, *v3.MediaType) {
	if content == nil || content.Len() == 0 {
		return "", nil
	}
	rank := func(ct string) int {
		c := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
		switch {
		case c == "application/json" || strings.HasSuffix(c, "+json"):
			return 0
		case c == "application/x-www-form-urlencoded":
			return 1
		case c == "multipart/form-data":
			return 2
		case c == "*/*":
			return 3
		case strings.HasSuffix(c, "/xml") || strings.HasSuffix(c, "+xml"):
			return 4
		case strings.HasPrefix(c, "text/"):
			return 5
		}
		return 9
	}
	best, bestRank := "", 100
	var bestMT *v3.MediaType
	for ct, mt := range content.FromOldest() {
		if r := rank(ct); r < bestRank {
			best, bestRank, bestMT = ct, r, mt
		}
	}
	return best, bestMT
}

func mediaExample(mt *v3.MediaType, dir Direction) any {
	if mt == nil {
		return nil
	}
	if mt.Example != nil {
		return NodeValue(mt.Example)
	}
	if v, ok := firstExample(mt.Examples); ok {
		return v
	}
	if mt.Schema != nil {
		return Example(mt.Schema, dir)
	}
	return nil
}

func requestBody(rb *v3.RequestBody) *Body {
	ct, mt := pickMedia(rb.Content)
	if mt == nil {
		return nil
	}
	c := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch {
	case c == "application/x-www-form-urlencoded" || c == "multipart/form-data":
		mode := BodyFormURL
		if c == "multipart/form-data" {
			mode = BodyMultipartForm
		}
		return &Body{Mode: mode, ContentType: ct, Fields: formFields(mt, mode)}
	case strings.HasSuffix(c, "/xml") || strings.HasSuffix(c, "+xml"):
		v := mediaExample(mt, DirRequest)
		s, _ := v.(string)
		return &Body{Mode: BodyXML, ContentType: ct, Raw: s}
	case strings.HasPrefix(c, "text/"):
		return &Body{Mode: BodyText, ContentType: ct, Raw: TextValue(mediaExample(mt, DirRequest))}
	case c == "application/json" || strings.HasSuffix(c, "+json") || c == "*/*":
		v := mediaExample(mt, DirRequest)
		if v == nil {
			return &Body{Mode: BodyJSON, ContentType: ct, Raw: ""}
		}
		return &Body{Mode: BodyJSON, ContentType: ct, Raw: ocyaml.JSON(v)}
	}
	return nil
}

func formFields(mt *v3.MediaType, mode string) []FormField {
	if mt.Schema == nil {
		return nil
	}
	s := mt.Schema.Schema()
	if s == nil || s.Properties == nil {
		return nil
	}
	example, _ := mediaExample(mt, DirRequest).(*ocyaml.Map)
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	var out []FormField
	for name, prop := range s.Properties.FromOldest() {
		ps := prop.Schema()
		if ps != nil && ps.ReadOnly != nil && *ps.ReadOnly {
			continue
		}
		f := FormField{Name: name, Required: required[name]}
		if mode == BodyMultipartForm && ps != nil && isFileSchema(ps) {
			f.IsFile = true
		} else if example != nil {
			f.Value = TextValue(example.Value(name))
		}
		out = append(out, f)
	}
	return out
}

func isFileSchema(s *base.Schema) bool {
	if s.Format == "binary" || s.ContentMediaType != "" {
		return true
	}
	if hasType(s, "array") && s.Items != nil && s.Items.IsA() {
		if is := s.Items.A.Schema(); is != nil {
			return is.Format == "binary" || is.ContentMediaType != ""
		}
	}
	return false
}

func response(status int, r *v3.Response) Response {
	out := Response{Status: status, Description: strings.TrimSpace(r.Description)}
	ct, mt := pickMedia(r.Content)
	if mt == nil {
		out.BodyType = "text"
		return out
	}
	out.ContentType = ct
	c := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	v := mediaExample(mt, DirResponse)
	switch {
	case c == "application/json" || strings.HasSuffix(c, "+json") || c == "*/*":
		out.BodyType = "json"
		if v != nil {
			out.Body = ocyaml.JSON(v)
		}
	case strings.HasSuffix(c, "/xml") || strings.HasSuffix(c, "+xml"):
		out.BodyType = "xml"
		out.Body, _ = v.(string)
	case c == "text/html":
		out.BodyType = "html"
		out.Body = TextValue(v)
	default:
		out.BodyType = "text"
		out.Body = TextValue(v)
	}
	return out
}

// StatusText returns the reason phrase for a status code.
func StatusText(code int) string {
	if t := http.StatusText(code); t != "" {
		return t
	}
	return strconv.Itoa(code)
}
