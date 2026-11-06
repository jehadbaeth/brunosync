package syncer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jehadbaeth/brunosync/internal/config"
	"github.com/jehadbaeth/brunosync/internal/ocyaml"
	"github.com/jehadbaeth/brunosync/internal/source"
)

// fixture copies test/fixtures/sample-repo into a temp dir and returns a
// config pointing at it plus the repo dir.
func fixture(t *testing.T) (*config.Config, string) {
	t.Helper()
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	copyDir(t, filepath.Join("..", "..", "test", "fixtures", "sample-repo"), repo)
	cfgPath := filepath.Join(tmp, "brunosync.yml")
	writeFile(t, cfgPath, "version: 1\ncollection:\n  path: ./collection\n  name: Test\nrepos:\n  - path: ./repo\n")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, repo
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// replace edits a file and fails the test if the old text is missing, so a
// fixture change can never silently turn a test into a no-op.
func replace(t *testing.T, p, old, new string) {
	t.Helper()
	s := readFile(t, p)
	if !strings.Contains(s, old) {
		t.Fatalf("%s does not contain %q", p, old)
	}
	writeFile(t, p, strings.Replace(s, old, new, 1))
}

func load(t *testing.T, p string) *ocyaml.Map {
	t.Helper()
	m, err := readYAML(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustSync(t *testing.T, cfg *config.Config) *Report {
	t.Helper()
	rep, err := Run(cfg, Options{Source: source.Options{}})
	if err != nil {
		t.Fatalf("sync failed: %v (errors: %v)", err, rep.Errors)
	}
	if len(rep.Errors) > 0 {
		t.Fatalf("sync reported errors: %v", rep.Errors)
	}
	return rep
}

func TestFirstSyncThenNoop(t *testing.T) {
	cfg, _ := fixture(t)
	rep := mustSync(t, cfg)
	if !rep.HasChanges() {
		t.Fatal("first sync wrote nothing")
	}
	coll := cfg.CollectionDir()
	for _, f := range []string{
		"opencollection.yml",
		"orders-service/folder.yml",
		"orders-service/orders/createOrder.yml",
		"orders-service/attachments/uploadAttachment.yml",
		"billing/getCostTree.yml",
		".brunosync/manifest.json",
		".brunosync/base/orders-service/createOrder.json",
	} {
		if !source.Exists(filepath.Join(coll, f)) {
			t.Errorf("missing %s", f)
		}
	}
	if source.Exists(filepath.Join(coll, "common")) {
		t.Error("the common module must not become a service")
	}

	create := load(t, filepath.Join(coll, "orders-service/orders/createOrder.yml"))
	if got := create.Path("http.url"); got != "{{orders_service_baseUrl}}/orders" {
		t.Errorf("url = %v", got)
	}
	body, _ := create.Path("http.body.data").(string)
	for _, want := range []string{`"sku": "SAT-BUS-01"`, `"currency": "EUR"`, `"amount": 12.5`} {
		if !strings.Contains(body, want) {
			t.Errorf("body misses %s (ref into common not resolved?):\n%s", want, body)
		}
	}
	if strings.Contains(body, `"id"`) {
		t.Error("readOnly id must not be in the request body")
	}
	headers := create.Path("http.headers").([]any)
	if headers[0].(*ocyaml.Map).String("name") != "X-Tenant" {
		t.Error("X-Tenant header from the common module is missing")
	}

	tree := load(t, filepath.Join(coll, "billing/getCostTree.yml"))
	if got := tree.Path("http.url"); got != "{{billing_baseUrl}}/cost-tree?depth=2" {
		t.Errorf("required query param must be in the url, got %v", got)
	}

	collDoc := readFile(t, filepath.Join(coll, "opencollection.yml"))
	for _, want := range []string{"orders_service_port", `value: "8081"`, "orders_service_baseUrl", "billing_host", "- .brunosync"} {
		if !strings.Contains(collDoc, want) {
			t.Errorf("opencollection.yml misses %q:\n%s", want, collDoc)
		}
	}

	rep = mustSync(t, cfg)
	if rep.HasChanges() {
		t.Fatalf("second sync must be a no-op, got %+v", rep.Changes)
	}
}

// TestMergeScenario runs the full life cycle: generate, humans edit the
// collection, the spec changes, sync again.
func TestMergeScenario(t *testing.T) {
	cfg, repo := fixture(t)
	mustSync(t, cfg)
	coll := cfg.CollectionDir()
	ordersSpec := filepath.Join(repo, "orders-service/src/main/resources/openapi.yml")
	commonSpec := filepath.Join(repo, "common/openapi.yml")
	billingSpec := filepath.Join(repo, "billing/openapi.yml")

	// --- humans edit the collection --------------------------------------
	createPath := filepath.Join(coll, "orders-service/orders/createOrder.yml")
	create := load(t, createPath)
	create.Set("runtime", ocyaml.M("scripts", []any{ocyaml.M("type", "tests", "code", "test('created', () => expect(res.status).to.equal(201));")}))
	create.Map("http").Set("body", ocyaml.M("type", "json", "data", `{"customerEmail": "ops@example.com"}`))
	headers := create.Path("http.headers").([]any)
	headers[0].(*ocyaml.Map).Set("value", "my-tenant")
	writeDoc(t, createPath, create)

	// enable the optional status query param like Bruno does (param + url)
	listPath := filepath.Join(coll, "orders-service/orders/listOrders.yml")
	list := load(t, listPath)
	for _, p := range list.Path("http.params").([]any) {
		if m := p.(*ocyaml.Map); m.String("name") == "status" {
			m.Delete("disabled")
		}
	}
	list.Map("http").Set("url", "{{orders_service_baseUrl}}/orders?status=OPEN")
	writeDoc(t, listPath, list)

	// move getOrder into another folder, delete deleteOrder
	moved := filepath.Join(coll, "orders-service/mine/getOrder.yml")
	if err := os.MkdirAll(filepath.Dir(moved), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(coll, "orders-service/orders/getOrder.yml"), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(coll, "orders-service/orders/deleteOrder.yml")); err != nil {
		t.Fatal(err)
	}

	// a test on uploadAttachment, whose endpoint will disappear
	uploadPath := filepath.Join(coll, "orders-service/attachments/uploadAttachment.yml")
	upload := load(t, uploadPath)
	upload.Set("runtime", ocyaml.M("scripts", []any{ocyaml.M("type", "tests", "code", "test('ok', () => {});")}))
	writeDoc(t, uploadPath, upload)

	// a hand made request brunosync must never touch
	writeFile(t, filepath.Join(coll, "orders-service/orders/handmade.yml"), "info:\n  name: Handmade\n  type: http\n  seq: 99\nhttp:\n  method: GET\n  url: http://example.com\n  auth: inherit\nsettings:\n  encodeUrl: true\n  timeout: 0\n  followRedirects: true\n  maxRedirects: 5\n  forwardAuthorizationHeader: true\n")

	// --- the specs change --------------------------------------------------
	replace(t, ordersSpec, "summary: Create an order", "summary: Place an order")
	replace(t, ordersSpec, "            customerEmail: {type: string, format: email}\n", "            customerEmail: {type: string, format: email}\n            priority: {type: string, example: HIGH}\n")
	replace(t, ordersSpec, `port: {default: "8081"}`, `port: {default: "9091"}`)
	replace(t, commonSpec, "schema: {type: string, example: okapi}", "schema: {type: string, example: acme}")
	replace(t, ordersSpec, "  /orders/{orderId}/attachments:\n    post:\n      operationId: uploadAttachment", "  /orders/{orderId}/files:\n    post:\n      operationId: uploadFile")
	replace(t, ordersSpec, "operationId: uploadFile\n      summary: Upload an attachment\n      tags: [attachments]", "operationId: uploadFile\n      summary: Upload a file\n      tags: [files]")
	replace(t, ordersSpec, "    delete:\n      operationId: deleteOrder", "    patch:\n      operationId: cancelOrder\n      summary: Cancel an order\n      tags: [orders]\n      responses:\n        '200': {description: Cancelled}\n    delete:\n      operationId: deleteOrder")
	replace(t, billingSpec, "  /cost-tree:\n    get:\n      operationId: getCostTree", "  /cost-tree:\n    get:\n      operationId: getCostTreeV2")

	rep := mustSync(t, cfg)
	orders := findService(t, rep, "orders-service")

	// createOrder: name follows the spec, the test script, the body and the
	// header value a human changed are all kept, both conflicts reported.
	create = load(t, createPath)
	if got := create.Path("info.name"); got != "Place an order" {
		t.Errorf("summary change not applied: %v", got)
	}
	if create.Map("runtime") == nil || !strings.Contains(ocyaml.MustMarshalString(create.Map("runtime")), "created") {
		t.Error("user test script lost")
	}
	if got := create.Path("http.body.data"); got != `{"customerEmail": "ops@example.com"}` {
		t.Errorf("user body overwritten: %v", got)
	}
	if v := create.Path("http.headers").([]any)[0].(*ocyaml.Map).String("value"); v != "my-tenant" {
		t.Errorf("user header value overwritten: %v", v)
	}
	wantConflicts := []string{"http.body", "X-Tenant"}
	for _, w := range wantConflicts {
		if !containsSub(orders.Conflicts, w) {
			t.Errorf("expected a conflict mentioning %s, got %v", w, orders.Conflicts)
		}
	}
	// the untouched response example picked up the new property
	if !strings.Contains(ocyaml.MustMarshalString(create.Path("examples")), "priority") {
		t.Error("examples were not updated from the spec")
	}

	// listOrders: enabled query param and url survive, header updated
	list = load(t, listPath)
	if got := list.Path("http.url"); got != "{{orders_service_baseUrl}}/orders?status=OPEN" {
		t.Errorf("query param toggle lost: %v", got)
	}
	if v := list.Path("http.headers").([]any)[0].(*ocyaml.Map).String("value"); v != "acme" {
		t.Errorf("untouched header should follow the spec, got %v", v)
	}

	// getOrder was found in its new folder, deleteOrder recreated
	if !source.Exists(moved) || source.Exists(filepath.Join(coll, "orders-service/orders/getOrder.yml")) {
		t.Error("moved request was not followed")
	}
	if !containsSub(orders.Moved, "mine/getOrder.yml") {
		t.Errorf("move not reported: %v", orders.Moved)
	}
	if !containsSub(orders.Recreated, "deleteOrder.yml") {
		t.Errorf("deleted request not recreated: %v", orders.Recreated)
	}

	// added, archived and deleted
	if !containsSub(orders.Added, "cancelOrder.yml") || !containsSub(orders.Added, "uploadFile.yml") {
		t.Errorf("new operations not added: %v", orders.Added)
	}
	if !source.Exists(filepath.Join(coll, "_removed/orders-service/uploadAttachment.yml")) {
		t.Error("removed request with a test should be archived")
	}
	billing := findService(t, rep, "billing")
	if !containsSub(billing.Deleted, "getCostTree.yml") || source.Exists(filepath.Join(coll, "billing/getCostTree.yml")) {
		t.Errorf("removed request without user work should be deleted: %v", billing.Deleted)
	}
	if !source.Exists(filepath.Join(coll, "orders-service/orders/handmade.yml")) {
		t.Error("hand made request was touched")
	}
	if source.Exists(filepath.Join(coll, "orders-service/attachments")) {
		t.Error("the attachments tag folder is empty now and should be removed")
	}

	// collection variable followed the spec
	if !strings.Contains(readFile(t, filepath.Join(coll, "opencollection.yml")), `value: "9091"`) {
		t.Error("port variable not updated")
	}

	// conflicts are kept in the last report so they do not just scroll past
	if r := readFile(t, filepath.Join(coll, ".brunosync/last-report.md")); !strings.Contains(r, "Conflicts") || !strings.Contains(r, "X-Tenant") {
		t.Errorf("last-report.md misses the conflicts:\n%s", r)
	}

	// The Bruno compatibility harness (test/bruno-compat) round trips this
	// merged collection through Bruno's own parser.
	if keep := os.Getenv("BRUNOSYNC_KEEP_COLLECTION"); keep != "" {
		copyDir(t, coll, keep)
	}

	// the next sync is quiet: conflicts are reported once, then the local
	// value simply stays
	rep = mustSync(t, cfg)
	if rep.HasChanges() || rep.ConflictCount() > 0 {
		t.Fatalf("third sync should be a no-op, changes %+v conflicts %d", rep.Changes, rep.ConflictCount())
	}
}

func TestFailedSpecLeavesRequestsAlone(t *testing.T) {
	cfg, repo := fixture(t)
	mustSync(t, cfg)
	// break a $ref; the service's requests must not be treated as removed
	replace(t, filepath.Join(repo, "billing/openapi.yml"), "../common/openapi.yml#/components/schemas/Node", "../common/missing.yml#/components/schemas/Node")
	rep, err := Run(cfg, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) == 0 || !strings.Contains(strings.Join(rep.Errors, "\n"), "billing") {
		t.Fatalf("broken ref must be reported, got %v", rep.Errors)
	}
	if !source.Exists(filepath.Join(cfg.CollectionDir(), "billing/getCostTree.yml")) {
		t.Fatal("requests of a failing spec were removed")
	}
	if rep.HasChanges() {
		t.Fatalf("nothing should change, got %+v", rep.Changes)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	cfg, _ := fixture(t)
	rep, err := Run(cfg, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.HasChanges() {
		t.Fatal("dry run should report changes")
	}
	if source.Exists(cfg.CollectionDir()) {
		t.Fatal("dry run created the collection")
	}
}

func TestUnmanagedFileAtTargetIsNotOverwritten(t *testing.T) {
	cfg, _ := fixture(t)
	coll := cfg.CollectionDir()
	target := filepath.Join(coll, "billing/createInvoice.yml")
	writeFile(t, target, "info:\n  name: Mine\n  type: http\nhttp:\n  method: POST\n  url: http://x\n")
	rep := mustSync(t, cfg)
	if !strings.Contains(readFile(t, target), "Mine") {
		t.Fatal("unmanaged file overwritten")
	}
	if !containsSub(findService(t, rep, "billing").Conflicts, "does not manage") {
		t.Fatal("collision not reported")
	}
}

func writeDoc(t *testing.T, p string, m *ocyaml.Map) {
	t.Helper()
	data, err := ocyaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, p, string(data))
}

func findService(t *testing.T, rep *Report, name string) *ServiceReport {
	t.Helper()
	for _, s := range rep.Services {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("service %s not in report", name)
	return nil
}

func containsSub(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Found in the real world suite (cloudflare, openai): tags differing only in
// case and query strings inside path keys must still give a stable second sync.
func TestCaseOnlyTagsAndEmbeddedQueryAreStable(t *testing.T) {
	cfg, repo := fixture(t)
	writeFile(t, filepath.Join(repo, "hooks", "openapi.yml"), `openapi: 3.0.3
info: {title: Hooks, version: "1"}
servers: [{url: "http://localhost:9000"}]
paths:
  /webhooks:
    get: {operationId: listHooks, tags: [Webhooks], responses: {'200': {description: ok}}}
  /webhooks/{id}:
    delete:
      operationId: deleteHook
      tags: [webhooks]
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses: {'204': {description: gone}}
  /responses?beta=true:
    post: {operationId: betaCreate, tags: [webhooks], responses: {'200': {description: ok}}}
`)
	mustSync(t, cfg)
	coll := cfg.CollectionDir()
	if !source.Exists(filepath.Join(coll, "hooks", "Webhooks", "deleteHook.yml")) {
		t.Error("case variants of a tag should share the first spelling's folder")
	}
	beta := load(t, filepath.Join(coll, "hooks", "Webhooks", "betaCreate.yml"))
	if got := beta.Path("http.url"); got != "{{hooks_baseUrl}}/responses?beta=true" {
		t.Errorf("url = %v", got)
	}
	if rep := mustSync(t, cfg); rep.HasChanges() {
		t.Fatalf("second sync changed files: %+v", rep.Changes)
	}
}

func TestLoadAllSpecStatus(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.Repos[0].Overrides = map[string]config.Override{"billing/openapi.yml": {Name: "orders-service"}}
	res := LoadAll(cfg, source.Options{})
	got := map[string]string{}
	for _, s := range res.Specs {
		got[s.Rel] = s.Name + "/" + s.Status
	}
	want := map[string]string{
		"common/openapi.yml":                            "common/library",
		"billing/openapi.yml":                           "orders-service/duplicate",
		"orders-service/src/main/resources/openapi.yml": "orders-service/duplicate",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	if len(res.Services) != 0 {
		t.Errorf("ambiguous services must not load: %d", len(res.Services))
	}
}
