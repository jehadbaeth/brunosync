# brunosync

brunosync keeps one Bruno collection in sync with the OpenAPI specs of many repositories. Each spec becomes its own folder in the collection, request bodies get generated examples, and anything people add by hand (tests, scripts, assertions, filled in values, their own requests) survives every sync.

It replaces Bruno's built in OpenAPI Sync, which links one collection to one spec, has no CLI and loses everything behind a cross file `$ref`.

* One static binary per platform. No Node, no Java, no build step for users.
* Specs that reference a shared `common` spec through relative `$ref`s work out of the box.
* Runs locally or in CI: write a folder, a zip artifact, or commit to a collection repo.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/jehadbaeth/brunosync/main/install.sh | sh
```

That downloads the right binary for your machine from the latest GitHub release, verifies its checksum and puts it in `~/.local/bin`. Windows, manual downloads, pinned versions and the macOS Gatekeeper note are in [INSTALL.md](INSTALL.md).

## Quick start

```sh
mkdir api-collection && cd api-collection
brunosync init            # the setup wizard writes brunosync.yml and offers a first sync
brunosync validate        # loads every spec, shows services and warnings, writes nothing
brunosync sync            # creates or updates ./collection
```

Then open the `collection` folder in Bruno (v3 or newer, which reads the OpenCollection YAML format). Run `brunosync sync` again whenever the specs change. `brunosync sync --dry-run` shows what would change first.

### The wizard

`brunosync init` asks for the collection name, the repositories (local folders or git URLs), then shows every spec it found with the service name, the number of requests and any problem, exactly as `sync` will see them. Every answer is checked as soon as you press Enter, with a ✓ or a ✗ right below it: folders must exist, git URLs are probed with `git ls-remote` using your credentials, branches must exist, a repository is cloned and scanned for specs right away (and if nothing matches, you're asked for file patterns, each one tried on the spot), service names must be valid and free, and hosts, ports and URLs are checked for the usual mistakes. Menus use the arrow keys, Enter keeps the grey default and Tab lets you edit it. Esc goes back one step without changing anything: out of a half filled form, out of a menu, and in the first run to the previous step. Ctrl+C quits without saving. Add as many repositories and environments as you need, one at a time. Under Services you pick a service from the list, then rename it (it insists when two specs end up with the same name), skip it or set its folder name. Environments set hosts and ports, and Output chooses folder, zip or git. It only ever writes `brunosync.yml`, so everything it does can also be done by hand.

`brunosync configure` opens the wizard on an existing config and starts with a summary of it: repositories, every service with its request count, environments and output, marked when there are unsaved changes. The summary reads git repositories from the local clone cache, so opening `configure` doesn't fetch anything. The first step that needs the current specs (Services, Environments, adding a repository) fetches them. Saving rewrites the file without its comments and keeps the previous version as `brunosync.yml.bak`. The wizard needs a terminal. In CI or with piped input it refuses to start, and `brunosync init` writes a commented template instead (also available with `init --starter`).

## Config

The full reference is [`brunosync.config.example.yml`](brunosync.config.example.yml). A minimal one:

```yaml
version: 1
collection:
  path: ./collection
  name: Platform APIs
repos:
  - path: ../backend                 # a folder on this machine, as it is on disk
  - git: ../orders-service           # a repository on this machine, at a branch
    ref: release
  - git: git@gitlab.example.com:platform/flight-dynamics.git
    ref: main
```

A repository can come from three places. `path:` reads a folder on this machine exactly as it is, uncommitted changes included, which is what you want while working on a spec. `git:` with a path reads a local repository at a branch or tag without touching your working copy. `git:` with a URL clones it with your own credentials. Relative paths start at the config file and `~/` works. The wizard asks which of the two you want when you give it a git checkout, and accepts folders dragged into the terminal.

Every `openapi.yml` / `openapi.yaml` in a repo is picked up. Specs without any paths (your `common` modules) are skipped on their own, and their schemas still arrive through the `$ref`s of the specs that use them. The service name comes from the folder (`orders-service/src/main/resources/openapi.yml` becomes `orders-service`). Use `overrides` in the config when that guess is wrong.

## What you get

```
collection/
  opencollection.yml            collection variables per service live here
  orders-service/               one folder per spec, named after info.title in Bruno
    orders/                     one subfolder per OpenAPI tag
      createOrder.yml           file name = operationId
  billing/
  _removed/                     requests whose endpoint disappeared but had your work on them
  .brunosync/                   brunosync's state, keep it next to the collection
```

### Base URLs, hosts and ports

Each service gets collection variables built from its spec's first `servers` entry, and the URL is always split into protocol, host and port, even when the spec hard codes `http://`:

```
orders_service_protocol = http
orders_service_host     = localhost
orders_service_port     = 8081
orders_service_baseUrl  = {{orders_service_protocol}}://{{orders_service_host}}:{{orders_service_port}}/orders
```

Requests use `{{orders_service_baseUrl}}/orders/:orderId`. To point at another machine, set `orders_service_host` (or port, protocol) in a Bruno environment. Environment variables beat collection variables in Bruno. If you edit the collection variable itself instead, brunosync keeps your value, but you'll get a conflict when the spec changes it.

### Environments

Environments can come from the config, so the whole team gets the same `local`, `staging` and `prod` without clicking them together:

```yaml
environments:
  - name: local                  # empty: the spec defaults apply
  - name: staging
    defaults:                    # every service that has the variable
      protocol: https
      host: "{service}.staging.example.com"
    services:                    # per service, wins over defaults
      orders-service: { port: "8443" }
      billing: { baseUrl: "https://gateway.staging.example.com/billing" }
    variables:                   # extra variables, written as they are
      tenant: okapi
```

Each one becomes `environments/<name>.yml` in the collection. The keys are the variable names without the service prefix: `protocol`, `host`, `port`, any other server variable of the spec, and `baseUrl` to replace the whole base URL (handy behind a gateway). brunosync only manages the variables it writes. Tokens and other variables you add in Bruno stay, a variable you mark as secret is never touched, and a managed value you change in Bruno is kept (with a conflict in the report if the config changes it too). An environment you remove from the config is deleted if nobody changed it, and kept with a warning otherwise. `brunosync validate` warns about service names or keys that match nothing.

### Example bodies

JSON bodies come from, in order: the media type `example`/`examples`, the schema's `example`, `default`, `const` or first `enum` value, and otherwise a placeholder by type and format (`"2024-01-01T00:00:00Z"` for date-time, `0` for numbers, and so on). `allOf` is merged, `oneOf`/`anyOf` use the first option, `readOnly` fields are left out of requests and `writeOnly` fields out of responses, and recursive schemas stop after one level. Form and multipart bodies get one field per property. Every documented response becomes a Bruno example. The output is deterministic, so a sync without spec changes changes nothing.

Path and query parameters only get a value when the spec gives an example. An empty `:orderId` is easier to notice than a fake one. Optional query params are added but disabled.

## What a sync keeps

brunosync remembers what it generated last time (`.brunosync/base/`) and does a three way merge for every request:

* **Spec changed, you didn't touch the field:** the spec wins.
* **You changed the field, the spec didn't:** your value stays.
* **Both changed:** your value stays and it's reported as a conflict.

Never touched at all: tests, scripts, assertions, request variables, settings, the order of requests, examples you saved yourself, folder settings, and requests you created by hand. Requests you moved to another folder are followed. A generated request you deleted comes back on the next sync.

Parameters and headers merge by name, so a value you filled in survives while a new parameter from the spec is still added. Toggling a query param in Bruno is not treated as a URL edit.

### Conflicts

A conflict is printed once, at the sync where it happens. After that your value just stays, and that field stops following the spec for that request. Every run that changes something also writes its report to `collection/.brunosync/last-report.md`, so you can read it again later.

To take the spec's version after all, fix the field by hand. Deleting the request file also works (the next sync regenerates it), but that throws away its tests and scripts too.

### Removed endpoints

When an endpoint disappears from its spec:

* if the request has your work on it (tests, scripts, edits), it moves to `_removed/<service>/` so nothing is lost,
* otherwise it's deleted.

Set `sync.removed` to `delete` or `keep` to change that. If a spec fails to load (for example a broken `$ref`), its requests are left completely alone. A broken spec never wipes its requests.

## Where the collection goes

The same binary works locally and in CI. Choose with `output.type` in the config, or per run with flags.

| Output | Use it for | What happens |
|---|---|---|
| `dir` (default) | local use | syncs into `collection.path` |
| `zip` | a CI artifact people download | syncs into `collection.path`, then writes a zip with one top folder people unzip and open in Bruno |
| `git` | a shared collection repo | clones the collection repo, syncs inside it (merging with what teammates committed there), commits with the report as the message body, pushes |

```sh
brunosync sync                               # dir
brunosync sync --zip dist/apis.zip           # zip, path from the flag
brunosync sync --output git                  # git, settings from the config
```

```yaml
output:
  type: git
  git:
    url: git@github.com:your-org/api-collection.git
    branch: main            # created if it does not exist
    path: collection        # folder inside that repo, default "."
    message: Update API collection (brunosync)
    push: true              # false: commit only, useful for a dry rehearsal
```

Notes:

* The zip is reproducible: the same collection always gives a byte identical file. It includes `.brunosync/`, so a downloaded collection can be synced further locally.
* In `git` mode the remote is the source of truth. The cached clone is reset to the remote branch before every run, so tests people committed to the collection repo are merged, never overwritten. If someone pushes while a sync runs, the push fails cleanly and the next run picks it up.
* Credentials are whatever `git` already uses (SSH agent, credential helper, or a token in the URL in CI). The commit author is `authorName`/`authorEmail` from the config, else your git identity, else `brunosync`.
* With `--dry-run` nothing is zipped, committed or pushed.
* If some specs fail to load, the others are still published (the failing ones keep their previous requests) and the exit code is 1.

### In GitHub Actions, for example

```yaml
- run: curl -fsSL https://raw.githubusercontent.com/jehadbaeth/brunosync/main/install.sh | sh
- run: ~/.local/bin/brunosync sync --zip apis.zip --report report.md
- uses: actions/upload-artifact@v4
  with: { name: bruno-collection, path: apis.zip }
```

## Commands

```
brunosync init       create brunosync.yml with the wizard (--starter: a commented template)
brunosync configure  change brunosync.yml with the wizard
brunosync validate   load every spec and report problems, writes nothing
brunosync sync       generate and merge into the collection
brunosync version
```

Flags for `sync`:

| Flag | |
|---|---|
| `-c, --config` | config file, default `brunosync.yml` |
| `--dry-run` | show what would change, write nothing |
| `--report file.md` | also write the report as Markdown |
| `--report-json file.json` | also write the report as JSON |
| `--offline` | use cached git clones, don't fetch |
| `--exit-code` | exit 10 when files changed, 20 on conflicts (for scripts and CI) |
| `-q` | only print the summary |
| `--output dir\|zip\|git` | override `output.type` |
| `--zip file.zip` | write a zip (implies `--output zip`) |

Exit code 1 means an error. Specs that loaded fine are still synced, and the failing ones are listed.

## Tested against real APIs

`test/realworld/` syncs 28 collections from 27 public repositories (GitHub, Stripe, Cloudflare, OpenAI, Twilio, Adyen, DigitalOcean, Elasticsearch and more, several thousand operations in total) and checks for every one:

* every spec loads, or fails with a known upstream reason
* the number of generated requests equals the operations counted independently from the spec
* every file saved through Bruno's own parser and serializer is unchanged for brunosync
* every JSON body and JSON example is valid JSON
* every `{{variable}}` in URLs and headers exists
* a second sync changes nothing
* `bru run` sends every single request to a local echo server

Latest results: [test/realworld/RESULTS.md](test/realworld/RESULTS.md). It runs weekly in GitHub Actions and locally with `test/realworld/run.sh`.

## Known limitations

* OpenAPI 3.0 and 3.1 only. Swagger 2.0 is rejected with a clear error.
* `$ref`s to other files are followed, `$ref`s to URLs are not.
* Only the first `servers` entry is used, and operation level `servers` are ignored (with a warning).
* Request auth is always `inherit`. Set auth once on the collection or a folder in Bruno.
* If a request file already exists at a path brunosync wants to create and brunosync doesn't manage it, brunosync leaves it alone and reports it. Adopting an existing hand imported collection isn't supported yet, so start from a fresh folder.
* Renaming a generated service or tag folder in Bruno makes the next sync create the folder again for new requests. Existing requests are followed.
* Operations without an `operationId` work, but they're identified by method and path, so renaming the path loses that request's tests. brunosync warns for each one. `sync.requireOperationIds: true` makes it an error.
* The wizard's terminal UI asks the terminal for its colors once at startup. Real terminals answer instantly. In a pseudo terminal that never answers (some CI log viewers, `script`), any brunosync command waits 5 seconds first; `TERM=dumb` avoids that.
* Verified against Bruno's file library and CLI 4.3.0 (see below), not by clicking through the desktop app.

## Development

```sh
make build              # bin/brunosync
make test               # go vet + tests with the race detector
test/bruno-compat/run.sh   # checks output against Bruno itself (needs Node)
test/realworld/run.sh      # the real world suite (needs Node, downloads specs)
make dist               # binaries for all platforms in dist/
```

`test/bruno-compat/run.sh` syncs the sample fixture, round trips every file through Bruno's own parser and serializer (what the app does on save), does the same for a collection that went through a merge with user edits, then sends every request with the Bruno CLI to a local echo server and checks headers, query strings and bodies. Run it after changing the generator, and when the team updates Bruno (bump the versions in `test/bruno-compat/package.json` first).

Releases: push a tag like `v0.1.0`. `.github/workflows/release.yml` runs the tests, builds every platform and publishes a GitHub release with the binaries and `SHA256SUMS`, which is what `install.sh` downloads. `.github/workflows/ci.yml` runs tests, the Bruno compatibility harness and a smoke test of the built binary on Linux, macOS and Windows for every push. `.gitlab-ci.yml` is kept for a GitLab mirror: it tests, builds and publishes to the GitLab package registry.

The design and the reasons behind it are in [DESIGN.md](DESIGN.md).
