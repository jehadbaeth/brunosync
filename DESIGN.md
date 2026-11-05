# brunosync design

Status: v0.1 implemented, local mode. Last updated 2026-11-05.

This document explains why brunosync works the way it does. For usage see README.md.

## 1. Goal and constraints

Keep **one** Bruno collection in sync with **many** OpenAPI specs across **many** GitLab repos, where a repo holds several service specs plus a `common` spec they reference through relative `$ref`s.

Constraints from the team:

* Distributed as a single binary. Users must not install runtimes or build anything.
* One collection, one section (folder) per service.
* Host, port and protocol must be variables, even though specs hard code `http://` today.
* Generate example request bodies.
* Where it finally runs (local, CI, elsewhere) is still open. v0.1 is local, and the design must not block CI later.

## 2. Facts verified with the real tools

Checked with `@usebruno/cli` 4.3.0, `@usebruno/filestore` from the same install, and `@redocly/cli` 2.76.0. The harness in `test/bruno-compat/` repeats checks 3 to 7 automatically.

1. **Bruno's own OpenAPI import silently drops everything behind a cross file `$ref`.** A header parameter vanished and a body field became `""`, without any warning. This is the main reason the tool exists.
2. Without an `operationId`, Bruno names files after method and path, so a path change looks like a deleted request plus a new one.
3. **The Bruno CLI executes every `.yml` request file under the collection, including folders listed in `extensions.bruno.ignore`.** State files therefore must not be YAML request lookalikes. brunosync stores its snapshots as JSON.
4. **The Bruno CLI only sends query parameters that appear in the URL.** The `params` list alone isn't enough, and Bruno's own importer gets this wrong. brunosync writes enabled query params into the URL, the way the Bruno app does.
5. Bruno resolves nested variables, so `baseUrl = {{protocol}}://{{host}}:{{port}}/api` works. Variable precedence is collection < environment < folder < request < runtime.
6. Every file brunosync writes, including files rewritten after a merge with user scripts, survives Bruno's parse and stringify (what the app does on save) without semantic change. A user saving a request in Bruno therefore never looks like an edit to the merge.
7. Generated requests run with `bru run`: base URLs resolve from the variables, headers from the common spec are sent, and JSON, form and multipart bodies go out.

## 3. Architecture

```
brunosync.yml ──► source ──► spec ──► gen ──► merge ──► collection on disk
                  (path or    (libopenapi,   (OpenCollection   (three way, per
                   git clone,  refs resolved, YAML documents)    field)
                   discovery)  examples)
```

| Package | Job |
|---|---|
| `internal/config` | load and validate `brunosync.yml`, unknown keys are errors |
| `internal/source` | local paths, git clones in the user cache dir, spec discovery, service naming |
| `internal/spec` | load a spec with relative `$ref`s resolved (libopenapi), server URL to variables, deterministic example generation |
| `internal/gen` | service model to request, folder and variable documents |
| `internal/merge` | three way merge rules |
| `internal/syncer` | orchestration, manifest, base snapshots, moves, removals, collection variables, report |
| `internal/ocyaml` | ordered YAML values, so files keep their key order and unknown keys |

### Why Go and our own generator, not Bruno's converter

The first draft reused Bruno's JavaScript converter (`openApiToBruno`). The single binary requirement rules that out in practice: bundling a JS runtime gives 60 to 100 MB per platform plus a dependency on Bruno's internal packages. Facts 1 and 4 also showed the converter needs fixing anyway. The Go generator mirrors the converter's output format, and fact 6 is the safety net that this stays compatible. Binaries are about 12 MB.

libopenapi was chosen over kin-openapi because it keeps property order (examples come out in schema order), supports OpenAPI 3.1, and resolves relative file references.

## 4. Decisions

### 4.1 Identity

A request is identified by `(service, operationId)`. `.brunosync/manifest.json` maps each identity to its current file. When the file isn't where the manifest says, brunosync looks for exactly one unclaimed request with the same method and URL path as the base snapshot, so requests moved in the Bruno UI are followed. Without an operationId the identity falls back to `METHOD path` with a warning. `sync.requireOperationIds` makes that an error.

### 4.2 Three way merge

For every generator owned field: if current equals base, take incoming. If incoming equals base, or current equals incoming, keep current. Otherwise keep current and report a conflict. Conflict markers are never written, because they would break YAML.

| Field | Owner | Rule |
|---|---|---|
| `info.name`, `info.tags`, `info.description`, `docs`, `http.method`, `http.body`, `http.auth` | generator | three way |
| URL path (before `?`) | generator | three way |
| URL query string | derived | rebuilt from the merged params, so toggling a param in Bruno never conflicts |
| `http.params`, `http.headers` | shared | merged by name, then field by field. New entries added, removed entries dropped unless edited locally |
| `examples` | shared | generated ones three way by name, user saved ones kept |
| `runtime` (scripts, tests, assertions, variables, actions), `settings`, `info.seq`, unknown keys | user | never touched |
| files not in the manifest | user | never touched |
| `folder.yml` | user | created once, never modified |

Base snapshots (`.brunosync/base/<service>/<id>.json`) hold what was generated last time. They're JSON because of fact 3.

A conflict is reported once. Afterwards base equals incoming, so the local value simply stays. Runs that change something write their report to `.brunosync/last-report.md` so conflicts don't get lost in a terminal.

### 4.3 Removed endpoints

A request whose endpoint left the spec is archived to `_removed/<service>/` if it has user work (runtime block, user examples, any owned field differing from base, or no base at all), otherwise deleted. `sync.removed: delete | keep` changes that. Tag folders brunosync created and that are now empty get removed.

### 4.4 Failure isolation

A repo or spec that fails to load (missing directory, clone error, unresolved `$ref`, duplicate operationId) is reported, and its manifest entries, files and variables are left exactly as they were. Without this, one broken `$ref` would archive a whole service.

### 4.5 Variables

From the first `servers` entry: `<service>_protocol`, `<service>_host`, `<service>_port` (when the URL has a port), any other server variables, and `<service>_baseUrl` composed from them (fact 5). They live in `opencollection.yml` under `request.variables` and are merged three way against the values stored in the manifest. Environments are left to the user, or to the later wizard, and override these values (fact 5).

### 4.6 Config first

`brunosync.yml` is the source of truth. `brunosync init` writes a starter. The planned wizard will only write this file.

## 5. Roadmap

1. **Setup wizard** (`brunosync init --interactive`): pick repos, review discovered services and names, define environments and generate Bruno environment files with per service host and port.
2. **CI mode**, if the team chooses it: a pipeline in a collection repo that runs `brunosync sync --exit-code --report report.md` and opens a merge request with the report as description. Nothing in the tool blocks this. It needs a project token and a small job definition.
3. **GitLab artifact source**, if CI bundling is wanted: `source: gitlab-artifact` reading pre bundled specs instead of cloning.
4. **`brunosync status`**: list requests whose owned fields diverge from the spec on purpose (resolved conflicts), so divergence stays visible after the one time report.
5. **Adoption** of an existing hand imported collection, by matching files on method and URL path.

## 6. Out of scope for now

Swagger 2.0, remote (`http://`) `$ref`s, per operation servers, generating auth from `securitySchemes`, GraphQL and gRPC requests.
