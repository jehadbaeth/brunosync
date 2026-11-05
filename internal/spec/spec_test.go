package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseServer(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		vars    map[string]ServerVariable
		order   []string
		wantURL string
		want    map[string]string
	}{
		{
			name:    "host and port variables, hard coded protocol",
			url:     "http://{host}:{port}/orders",
			vars:    map[string]ServerVariable{"host": {Default: "localhost"}, "port": {Default: "8081"}},
			order:   []string{"host", "port"},
			wantURL: "{{protocol}}://{{host}}:{{port}}/orders",
			want:    map[string]string{"protocol": "http", "host": "localhost", "port": "8081"},
		},
		{
			name:    "fully literal",
			url:     "http://localhost:8082/api/v1/",
			wantURL: "{{protocol}}://{{host}}:{{port}}/api/v1",
			want:    map[string]string{"protocol": "http", "host": "localhost", "port": "8082"},
		},
		{
			name:    "no port",
			url:     "https://api.example.com",
			wantURL: "{{protocol}}://{{host}}",
			want:    map[string]string{"protocol": "https", "host": "api.example.com"},
		},
		{
			name:    "differently named variables and a path variable",
			url:     "{scheme}://{server}:{p}/{basePath}",
			vars:    map[string]ServerVariable{"scheme": {Default: "http", Enum: []string{"http", "https"}}, "server": {Default: "svc"}, "p": {Default: "80"}, "basePath": {Default: "v2"}},
			order:   []string{"scheme", "server", "p", "basePath"},
			wantURL: "{{protocol}}://{{host}}:{{port}}/{{basePath}}",
			want:    map[string]string{"protocol": "http", "host": "svc", "port": "80", "basePath": "v2"},
		},
		{
			name:    "relative url",
			url:     "/api",
			wantURL: "{{protocol}}://{{host}}/api",
			want:    map[string]string{"protocol": "http", "host": "localhost"},
		},
		{
			name:    "ipv6",
			url:     "http://[::1]:9000",
			wantURL: "{{protocol}}://{{host}}:{{port}}",
			want:    map[string]string{"protocol": "http", "host": "[::1]", "port": "9000"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, warnings := ParseServer(tt.url, tt.vars, tt.order)
			if len(warnings) > 0 {
				t.Errorf("unexpected warnings %v", warnings)
			}
			if srv.BaseURL != tt.wantURL {
				t.Errorf("BaseURL = %q, want %q", srv.BaseURL, tt.wantURL)
			}
			got := map[string]string{}
			for _, v := range srv.Vars {
				got[v.Name] = v.Value
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("var %s = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("vars = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseServerUndefinedVariable(t *testing.T) {
	_, warnings := ParseServer("http://{host}:8080", nil, nil)
	if len(warnings) == 0 {
		t.Fatal("expected a warning for an undefined server variable")
	}
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "openapi.yml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExampleGeneration(t *testing.T) {
	p := writeSpec(t, `openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /things:
    post:
      operationId: createThing
      requestBody:
        content:
          application/json:
            schema: {$ref: '#/components/schemas/Thing'}
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Thing'}
components:
  schemas:
    Thing:
      type: object
      properties:
        id: {type: string, format: uuid, readOnly: true}
        secret: {type: string, writeOnly: true}
        when: {type: string, format: date-time}
        day: {type: string, format: date}
        mail: {type: string, format: email}
        count: {type: integer, minimum: 3}
        ratio: {type: number}
        flag: {type: boolean}
        kind: {type: string, enum: [A, B]}
        nullableName: {type: [string, "null"]}
        tags: {type: array, items: {type: string}}
        shape:
          oneOf:
            - {type: object, properties: {radius: {type: number, example: 2}}}
            - {type: object, properties: {side: {type: number}}}
        meta:
          type: object
          additionalProperties: {type: integer}
        withDefault: {type: string, default: fallback}
        parent: {$ref: '#/components/schemas/Thing'}
`)
	svc, err := Load(p, LoadOptions{Name: "t", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	op := svc.Operations[0]
	req := op.Body.Raw
	for _, want := range []string{
		`"secret": "string"`, `"when": "2024-01-01T00:00:00Z"`, `"day": "2024-01-01"`,
		`"mail": "user@example.com"`, `"count": 3`, `"ratio": 0`, `"flag": true`, `"kind": "A"`,
		`"nullableName": "string"`, `"tags": [`, `"radius": 2`, `"additionalProp1": 0`, `"withDefault": "fallback"`,
		`"parent": {}`,
	} {
		if !strings.Contains(req, want) {
			t.Errorf("request example misses %s:\n%s", want, req)
		}
	}
	if strings.Contains(req, `"id"`) {
		t.Errorf("readOnly property in request example:\n%s", req)
	}
	// property order follows the schema
	if strings.Index(req, `"secret"`) > strings.Index(req, `"when"`) {
		t.Error("property order not preserved")
	}
	resp := op.Responses[0].Body
	if !strings.Contains(resp, `"id": "3fa85f64-5717-4562-b3fc-2c963f66afa6"`) || strings.Contains(resp, `"secret"`) {
		t.Errorf("response example should have id and no writeOnly secret:\n%s", resp)
	}

	// determinism: the same spec gives byte identical output
	svc2, _ := Load(p, LoadOptions{Name: "t", Source: "test"})
	if svc2.Operations[0].Body.Raw != req {
		t.Error("example generation is not deterministic")
	}
}

func TestMediaTypeExampleWins(t *testing.T) {
	p := writeSpec(t, `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /x:
    post:
      operationId: x
      requestBody:
        content:
          application/json:
            schema: {type: object, properties: {a: {type: string}}}
            examples:
              first: {value: {a: from-examples}}
      responses: {'204': {description: none}}
`)
	svc, err := Load(p, LoadOptions{Name: "t", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svc.Operations[0].Body.Raw, "from-examples") {
		t.Errorf("media type example ignored: %s", svc.Operations[0].Body.Raw)
	}
}

func TestCrossFileRefsAndMissingOperationIDs(t *testing.T) {
	fixtures := filepath.Join("..", "..", "test", "fixtures")

	svc, err := Load(filepath.Join(fixtures, "cross-file-refs", "svc-orders", "openapi.yml"), LoadOptions{Name: "orders", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	get := svc.Operations[0]
	if get.ID != "getOrderById" || len(get.Params) != 2 || get.Params[0].Name != "X-Tenant" || get.Params[0].Value != "okapi" {
		t.Errorf("header from the common file not resolved: %+v", get.Params)
	}

	svc, err = Load(filepath.Join(fixtures, "missing-operationid", "svc-orders", "openapi.yml"), LoadOptions{Name: "orders", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Operations[0].ID != "GET /orders/{id}" || svc.Operations[0].HasOperationID {
		t.Errorf("fallback identity wrong: %q", svc.Operations[0].ID)
	}
	if len(svc.Warnings) != 2 {
		t.Errorf("expected one warning per operation without operationId, got %v", svc.Warnings)
	}
	_, err = Load(filepath.Join(fixtures, "missing-operationid", "svc-orders", "openapi.yml"), LoadOptions{Name: "orders", Source: "test", RequireOperationIDs: true})
	if err == nil {
		t.Error("requireOperationIds should turn a missing operationId into an error")
	}
}

func TestLibrarySpecAndBrokenRef(t *testing.T) {
	_, err := Load(filepath.Join("..", "..", "test", "fixtures", "cross-file-refs", "common", "openapi.yml"), LoadOptions{Name: "common", Source: "test"})
	if err != ErrNoOperations {
		t.Errorf("common spec should be ErrNoOperations, got %v", err)
	}
	p := writeSpec(t, `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /x:
    get:
      operationId: x
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: './nope.yml#/components/schemas/Gone'}
`)
	if _, err := Load(p, LoadOptions{Name: "t", Source: "test"}); err == nil {
		t.Error("a $ref to a missing file must be an error, not silently dropped")
	}
}

func TestSwagger2Rejected(t *testing.T) {
	p := writeSpec(t, "swagger: '2.0'\ninfo: {title: t, version: '1'}\npaths: {}\n")
	_, err := Load(p, LoadOptions{Name: "t", Source: "test"})
	if err == nil || !strings.Contains(err.Error(), "Swagger 2.0") {
		t.Errorf("expected a clear Swagger 2.0 error, got %v", err)
	}
}

// Found in the real world suite (openai, temporal).
func TestEmbeddedQueryAndDuplicateOperationIDs(t *testing.T) {
	p := writeSpec(t, `openapi: 3.0.3
info: {title: t, version: "1"}
paths:
  /responses?beta=true:
    post:
      operationId: betaCreate
      responses: {'200': {description: ok}}
  /a/{id}/complete:
    post:
      operationId: complete
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses: {'200': {description: ok}}
  /b/{id}/complete:
    post:
      operationId: complete
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses: {'200': {description: ok}}
`)
	svc, err := Load(p, LoadOptions{Name: "t", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	beta := svc.Operations[0]
	if beta.Path != "/responses" || len(beta.Params) != 1 || beta.Params[0].Name != "beta" || beta.Params[0].Value != "true" || !beta.Params[0].Required {
		t.Errorf("embedded query not turned into a param: %q %+v", beta.Path, beta.Params)
	}
	if svc.Operations[1].ID != "complete" || svc.Operations[2].ID != "POST /b/{id}/complete" {
		t.Errorf("duplicate operationId fallback wrong: %q %q", svc.Operations[1].ID, svc.Operations[2].ID)
	}
	if _, err := Load(p, LoadOptions{Name: "t", Source: "test", RequireOperationIDs: true}); err == nil {
		t.Error("requireOperationIds should reject duplicate operationIds")
	}
}
