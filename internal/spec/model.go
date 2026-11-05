// Package spec loads OpenAPI documents (with relative $refs into other files
// resolved) and turns them into a small model that the generator consumes.
package spec

// Service is one OpenAPI spec that has operations.
type Service struct {
	Name        string // identity namespace, e.g. "orders"
	Folder      string // display name of the top level folder
	Title       string
	Version     string
	Description string
	Source      string // repo:relative/path, for reports
	Server      Server
	Operations  []Operation
	Tags        []Tag
	Warnings    []string
}

// Tag is an OpenAPI tag with its description.
type Tag struct {
	Name        string
	Description string
}

// Server describes how the base URL is built from collection variables.
type Server struct {
	// Vars in the order they should appear: protocol, host, port, then others.
	// Names are unqualified ("host"), the generator prefixes the service.
	Vars []Var
	// BaseURL is the base URL expressed with unqualified {{var}} placeholders,
	// for example "{{protocol}}://{{host}}:{{port}}/api".
	BaseURL string
}

// Var is one collection variable with its default value.
type Var struct {
	Name        string
	Value       string
	Description string
}

// Operation is one HTTP operation.
type Operation struct {
	ID             string // operationId, or "METHOD path" when missing
	HasOperationID bool
	Method         string // upper case
	Path           string // OpenAPI path, e.g. /orders/{id}
	Summary        string
	Description    string
	Tags           []string
	Deprecated     bool
	Params         []Param
	Body           *Body
	Responses      []Response
}

// Param is a path, query or header parameter.
type Param struct {
	Name        string
	In          string // path | query | header
	Required    bool
	Value       string // example value rendered as text
	Description string
}

// Body modes, matching the OpenCollection body types.
const (
	BodyJSON          = "json"
	BodyXML           = "xml"
	BodyText          = "text"
	BodyFormURL       = "form-urlencoded"
	BodyMultipartForm = "multipart-form"
)

// Body is a request body example.
type Body struct {
	Mode        string
	ContentType string
	// Raw holds the example text for json, xml and text bodies.
	Raw string
	// Fields holds the entries for form bodies.
	Fields []FormField
}

// FormField is one entry of a form body.
type FormField struct {
	Name     string
	Value    string
	IsFile   bool
	Required bool
}

// Response is one documented response, used for Bruno examples.
type Response struct {
	Status      int
	Description string
	ContentType string
	BodyType    string // json | xml | text | html
	Body        string
}
