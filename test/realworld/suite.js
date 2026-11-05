#!/usr/bin/env node
// Real world suite: syncs public OpenAPI repositories with brunosync and
// checks the result against independent counts and against Bruno itself.
//
//   node test/realworld/suite.js [--only a,b] [--no-run] [--refresh] [--bin path]
//
// Per repo it checks:
//   1. every spec loads, or fails with an error listed under `expect`
//   2. requests generated == operations counted independently from the spec
//   3. every request file survives Bruno's own parse + stringify unchanged
//   4. every JSON body and JSON example is valid JSON
//   5. every {{variable}} in URLs and headers is a defined collection variable
//   6. a second sync changes nothing
//   7. `bru run` sends every request to a local echo server (unless --no-run)
//
// Results go to test/realworld/RESULTS.md and results.json.
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const { spawnSync, spawn } = require('child_process');
const { createRequire } = require('module');

const here = __dirname;
const root = path.resolve(here, '..', '..');
const compat = path.join(root, 'test', 'bruno-compat');
const req = createRequire(path.join(compat, 'package.json'));
const yaml = req('js-yaml');
const fsx = req('@usebruno/filestore');
const bru = path.join(compat, 'node_modules', '.bin', 'bru');

const argv = process.argv.slice(2);
const flag = (n) => argv.includes(n);
const opt = (n, d) => {
  const i = argv.indexOf(n);
  return i >= 0 ? argv[i + 1] : d;
};
const only = opt('--only', '') ? new Set(opt('--only').split(',')) : null;
const runBru = !flag('--no-run');
const refresh = flag('--refresh');
const bin = path.resolve(opt('--bin', path.join(root, 'bin', 'brunosync')));
const cache = process.env.REALWORLD_CACHE || path.join(os.homedir(), '.cache', 'brunosync-realworld');
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'brunosync-realworld-'));

const CORE = { schema: yaml.CORE_SCHEMA };
const METHODS = ['get', 'put', 'post', 'delete', 'options', 'head', 'patch', 'trace'];

function sh(cmd, args, o = {}) {
  const r = spawnSync(cmd, args, { encoding: 'utf8', maxBuffer: 512 * 1024 * 1024, ...o });
  return { code: r.status, out: (r.stdout || '') + (r.stderr || ''), error: r.error };
}

function git(args, cwd) {
  const r = sh('git', args, { cwd, env: { ...process.env, GIT_TERMINAL_PROMPT: '0' } });
  if (r.code !== 0) throw new Error(`git ${args.join(' ')}: ${r.out.trim()}`);
  return r.out.trim();
}

function ensureRepo(r) {
  const dir = path.join(cache, r.name);
  const patterns = r.sparse.map((p) => (p.startsWith('/') ? p : '/' + p));
  if (!fs.existsSync(path.join(dir, '.git'))) {
    fs.mkdirSync(cache, { recursive: true });
    const args = ['clone', '--quiet', '--depth', '1', '--filter=blob:none', '--sparse', '--no-checkout'];
    if (r.ref) args.push('--branch', r.ref);
    git([...args, r.url, dir]);
    git(['sparse-checkout', 'set', '--no-cone', ...patterns], dir);
    git(['checkout', '--quiet'], dir);
  } else if (refresh) {
    git(['fetch', '--quiet', '--depth', '1', 'origin', r.ref || 'HEAD'], dir);
    git(['sparse-checkout', 'set', '--no-cone', ...patterns], dir);
    git(['reset', '--quiet', '--hard', 'FETCH_HEAD'], dir);
  }
  return { dir, commit: git(['rev-parse', '--short', 'HEAD'], dir) };
}

// ---------------------------------------------------------------------------
// independent operation count (js-yaml, own $ref resolution for path items)

const docs = new Map();
function loadDoc(file) {
  if (!docs.has(file)) docs.set(file, yaml.load(fs.readFileSync(file, 'utf8'), { json: true }));
  return docs.get(file);
}
function resolveRef(ref, base) {
  const [f, frag] = ref.split('#');
  const file = f ? path.resolve(path.dirname(base), f) : base;
  let node = loadDoc(file);
  for (const seg of (frag || '').split('/').slice(1)) {
    node = node && node[decodeURIComponent(seg).replace(/~1/g, '/').replace(/~0/g, '~')];
  }
  return { node, file };
}
function countOps(specFile) {
  const doc = loadDoc(specFile);
  let n = 0;
  for (const item0 of Object.values((doc && doc.paths) || {})) {
    let item = item0;
    let file = specFile;
    for (let i = 0; item && item.$ref && i < 10; i++) ({ node: item, file } = resolveRef(item.$ref, file));
    for (const m of METHODS) if (item && item[m]) n++;
  }
  return n;
}

// ---------------------------------------------------------------------------
// collection checks

function requestFiles(dir) {
  const out = [];
  (function walk(d) {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) {
        if (e.name !== '.brunosync') walk(p);
      } else if (e.name.endsWith('.yml') && e.name !== 'folder.yml' && e.name !== 'opencollection.yml') {
        out.push(p);
      }
    }
  })(dir);
  return out;
}

function checkCollection(coll, specText) {
  const res = { files: 0, roundtrip: [], json: [], vars: [] };
  const collDoc = yaml.load(fs.readFileSync(path.join(coll, 'opencollection.yml'), 'utf8'));
  const defined = new Set(((collDoc.request || {}).variables || []).map((v) => v.name));
  const jsonOK = (s, where) => {
    if (!s || !s.trim()) return;
    try {
      JSON.parse(s);
    } catch (e) {
      res.json.push(`${where}: ${e.message}`);
    }
  };
  for (const f of requestFiles(coll)) {
    res.files++;
    const rel = path.relative(coll, f);
    const src = fs.readFileSync(f, 'utf8');
    // Save the file the way the Bruno app does; compared later by brunosync.
    const saved = path.join(coll + '-saved', rel);
    fs.mkdirSync(path.dirname(saved), { recursive: true });
    try {
      fs.writeFileSync(saved, fsx.stringifyRequest(fsx.parseRequest(src, { format: 'yml' }), { format: 'yml' }));
    } catch (e) {
      res.roundtrip.push(`${rel}: Bruno cannot parse it: ${e.message}`);
    }
    const doc = yaml.load(src, CORE);
    const h = doc.http || {};
    if (h.body && h.body.type === 'json') jsonOK(h.body.data, rel + ' body');
    for (const ex of doc.examples || []) {
      const b = ex.response && ex.response.body;
      if (b && b.type === 'json') jsonOK(b.data, `${rel} example ${ex.name}`);
    }
    const texts = [h.url || '', ...(h.headers || []).map((x) => String(x.value))];
    for (const t of texts) {
      for (const m of t.matchAll(/\{\{([^}]+)\}\}/g)) {
        // placeholders that are literally in the spec (Linode does that) are fine
        if (!defined.has(m[1]) && !specText.includes(`{{${m[1]}}}`)) res.vars.push(`${rel}: {{${m[1]}}} is not a collection variable`);
      }
    }
  }
  // folder files go through Bruno too, the collection file is copied as is
  (function walk(d) {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) {
        if (e.name !== '.brunosync') walk(p);
      } else if (e.name === 'folder.yml' || e.name === 'opencollection.yml') {
        const saved = path.join(coll + '-saved', path.relative(coll, p));
        fs.mkdirSync(path.dirname(saved), { recursive: true });
        const src = fs.readFileSync(p, 'utf8');
        fs.writeFileSync(saved, e.name === 'folder.yml'
          ? fsx.stringifyFolder(fsx.parseFolder(src, { format: 'yml' }), { format: 'yml' })
          : src);
      }
    }
  })(coll);

  // brunosync's own view: does a Bruno save change anything for the merge?
  const cmp = sh(bin, ['debug', 'compare', coll, coll + '-saved']);
  for (const line of cmp.out.split('\n')) {
    if (line.trim() && !/files compared, 0 differ/.test(line)) res.roundtrip.push(line);
  }
  return res;
}

// Points every service at the echo server for `bru run`.
function retarget(coll, port) {
  const p = path.join(coll, 'opencollection.yml');
  const doc = yaml.load(fs.readFileSync(p, 'utf8'));
  const vars = (doc.request && doc.request.variables) || [];
  const names = new Set(vars.map((v) => v.name));
  for (const v of vars) {
    if (v.name.endsWith('_protocol')) v.value = 'http';
    if (v.name.endsWith('_port')) v.value = String(port);
    if (v.name.endsWith('_host')) {
      const prefix = v.name.slice(0, -'host'.length);
      v.value = names.has(prefix + 'port') ? '127.0.0.1' : `127.0.0.1:${port}`;
    }
  }
  fs.writeFileSync(p, yaml.dump(doc, { lineWidth: -1 }));
}

function startEcho() {
  let hits = 0;
  const server = http.createServer((rq, rs) => {
    rq.resume();
    rq.on('end', () => {
      hits++;
      rs.writeHead(200, { 'content-type': 'application/json' });
      rs.end('{}');
    });
  });
  return new Promise((resolve) =>
    server.listen(0, '127.0.0.1', () =>
      resolve({ port: server.address().port, hits: () => hits, reset: () => (hits = 0), close: () => server.close() })));
}

// Async on purpose: the echo server lives in this process, a blocking spawn
// would starve it and deadlock.
function shAsync(cmd, args, o = {}) {
  return new Promise((resolve) => {
    const p = spawn(cmd, args, o);
    let out = '';
    p.stdout.on('data', (d) => (out = (out + d).slice(-20000)));
    p.stderr.on('data', (d) => (out = (out + d).slice(-20000)));
    const timer = setTimeout(() => p.kill('SIGKILL'), o.timeout || 45 * 60 * 1000);
    p.on('close', (code) => {
      clearTimeout(timer);
      resolve({ code, out });
    });
  });
}

async function runBruno(coll, echo) {
  const runDir = coll + '-run';
  fs.cpSync(coll, runDir, { recursive: true });
  fs.rmSync(path.join(runDir, '.brunosync'), { recursive: true, force: true });
  retarget(runDir, echo.port);
  echo.reset();
  const out = path.join(runDir, '..', 'bru-results.json');
  const r = await shAsync(bru, ['run', '-r', '--reporter-json', out], { cwd: runDir });
  let results = [];
  try {
    const parsed = JSON.parse(fs.readFileSync(out, 'utf8'));
    results = (Array.isArray(parsed) ? parsed : [parsed]).flatMap((x) => x.results || []);
  } catch (e) {
    return { ran: 0, ok: 0, failed: [`no bru report (exit ${r.code}): ${r.out.slice(-300)}`], hits: echo.hits() };
  }
  const failed = results
    .filter((x) => x.error || !x.response || x.response.status !== 200)
    .map((x) => `${x.test && x.test.filename}: ${x.error || (x.response && x.response.status)}`);
  return { ran: results.length, ok: results.length - failed.length, failed, hits: echo.hits() };
}

// ---------------------------------------------------------------------------

async function main() {
  if (!fs.existsSync(bin)) throw new Error(`brunosync binary not found at ${bin}, run make build`);
  const { repos } = yaml.load(fs.readFileSync(path.join(here, 'repos.yml'), 'utf8'));
  const echo = runBru ? await startEcho() : null;
  const rows = [];

  for (const r of repos) {
    if (only && !only.has(r.name)) continue;
    const t0 = Date.now();
    const row = { name: r.name, url: r.url, problems: [] };
    rows.push(row);
    process.stdout.write(`${r.name.padEnd(22)} `);
    try {
      const { dir, commit } = ensureRepo(r);
      row.commit = commit;
      const wd = path.join(work, r.name);
      fs.mkdirSync(wd, { recursive: true });
      const cfg = path.join(wd, 'brunosync.yml');
      fs.writeFileSync(cfg, yaml.dump({
        version: 1,
        collection: { path: './collection', name: r.name },
        repos: [{ name: r.name, path: dir, specs: r.specs }],
      }));
      const reportFile = path.join(wd, 'report.json');
      const s1 = sh(bin, ['sync', '-c', cfg, '-q', '--report-json', reportFile]);
      const rep = JSON.parse(fs.readFileSync(reportFile, 'utf8'));
      const expected = (r.expect && r.expect.errors) || [];
      row.errors = rep.errors || [];
      row.unexpectedErrors = row.errors.filter((e) => !expected.some((x) => e.includes(x)));
      row.skipped = (rep.skipped || []).length;
      row.warnings = (rep.services || []).reduce((n, s) => n + (s.warnings || []).length, 0);
      if (s1.code !== 0 && row.unexpectedErrors.length === 0 && row.errors.length === 0) {
        row.problems.push(`sync exited ${s1.code}: ${s1.out.slice(-300)}`);
      }
      for (const e of row.unexpectedErrors) row.problems.push('load error: ' + e.split('\n')[0].slice(0, 300));

      const coll = path.join(wd, 'collection');
      const manifest = JSON.parse(fs.readFileSync(path.join(coll, '.brunosync', 'manifest.json'), 'utf8'));
      row.services = Object.keys(manifest.services).length;
      row.ops = 0;
      row.requests = Object.keys(manifest.requests).length;
      for (const [name, st] of Object.entries(manifest.services)) {
        const rel = st.source.slice(st.source.indexOf(':') + 1);
        let expectedOps;
        try {
          expectedOps = countOps(path.join(dir, rel));
        } catch (e) {
          row.problems.push(`${name}: independent count failed: ${e.message}`);
          continue;
        }
        const got = Object.values(manifest.requests).filter((x) => x.service === name).length;
        row.ops += expectedOps;
        if (got !== expectedOps) row.problems.push(`${name}: ${got} requests generated, spec has ${expectedOps} operations`);
      }

      const specText = Object.values(manifest.services)
        .map((st) => fs.readFileSync(path.join(dir, st.source.slice(st.source.indexOf(':') + 1)), 'utf8'))
        .join('\n');
      const c = checkCollection(coll, specText);
      row.files = c.files;
      row.roundtrip = c.roundtrip.length;
      row.jsonErrors = c.json.length;
      row.varErrors = c.vars.length;
      for (const x of c.roundtrip.slice(0, 5)) row.problems.push('round trip: ' + x);
      for (const x of c.json.slice(0, 5)) row.problems.push('invalid JSON: ' + x);
      for (const x of c.vars.slice(0, 5)) row.problems.push('variable: ' + x);

      const s2 = sh(bin, ['sync', '-c', cfg, '-q', '--exit-code']);
      row.idempotent = /^0 file\(s\) changed/m.test(s2.out);
      if (!row.idempotent) row.problems.push('second sync changed files: ' + s2.out.slice(-200));

      if (runBru && row.files > 0) {
        const b = await runBruno(coll, echo);
        row.bru = b;
        if (b.ran !== row.files) row.problems.push(`bru ran ${b.ran} of ${row.files} requests`);
        if (b.hits !== b.ran) row.problems.push(`echo server saw ${b.hits} of ${b.ran} requests`);
        for (const f of b.failed.slice(0, 5)) row.problems.push('bru: ' + f);
        if (b.failed.length > 5) row.problems.push(`bru: ... ${b.failed.length - 5} more failures`);
      }
    } catch (e) {
      row.problems.push('suite error: ' + e.message.slice(0, 400));
    }
    row.seconds = Math.round((Date.now() - t0) / 100) / 10;
    row.pass = row.problems.length === 0;
    console.log(`${row.pass ? 'PASS' : 'FAIL'}  services=${row.services ?? '-'} requests=${row.requests ?? '-'} ops=${row.ops ?? '-'} ${row.seconds}s`);
    for (const p of row.problems.slice(0, 8)) console.log('    ' + p);
  }
  if (echo) echo.close();

  fs.writeFileSync(path.join(here, 'results.json'), JSON.stringify(rows, null, 2) + '\n');
  writeMarkdown(rows);
  const failed = rows.filter((x) => !x.pass).length;
  console.log(`\n${rows.length - failed}/${rows.length} repos passed. Work dir: ${work}`);
  process.exit(failed ? 1 : 0);
}

function writeMarkdown(rows) {
  const tot = (k) => rows.reduce((n, r) => n + (typeof r[k] === 'number' ? r[k] : 0), 0);
  const bruTot = rows.reduce((n, r) => n + ((r.bru && r.bru.ok) || 0), 0);
  let md = '# Real world suite results\n\n';
  md += `Generated by \`test/realworld/suite.js\` on ${new Date().toISOString().slice(0, 10)} with Bruno CLI from \`test/bruno-compat/package.json\`.\n\n`;
  md += `**${rows.filter((r) => r.pass).length} of ${rows.length} repos pass.** ${tot('services')} specs synced into ${tot('requests')} requests (${tot('ops')} operations counted independently), ${bruTot} requests sent by \`bru run\`.\n\n`;
  md += '| Repo | Commit | Specs | Skipped | Load errors | Operations | Requests | Round trip diffs | Bad JSON | Bad vars | Idempotent | bru run ok | Time | Result |\n';
  md += '|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---:|---|\n';
  for (const r of rows) {
    const bruCell = r.bru ? `${r.bru.ok}/${r.bru.ran}` : 'skipped';
    md += `| [${r.name}](${r.url}) | \`${r.commit || '-'}\` | ${r.services ?? '-'} | ${r.skipped ?? '-'} | ${(r.errors || []).length} | ${r.ops ?? '-'} | ${r.requests ?? '-'} | ${r.roundtrip ?? '-'} | ${r.jsonErrors ?? '-'} | ${r.varErrors ?? '-'} | ${r.idempotent ? 'yes' : 'no'} | ${bruCell} | ${r.seconds}s | ${r.pass ? 'PASS' : 'FAIL'} |\n`;
  }
  const withErrors = rows.filter((r) => (r.errors || []).length || r.problems.length);
  if (withErrors.length) {
    md += '\n## Details\n';
    for (const r of withErrors) {
      md += `\n### ${r.name}\n\n`;
      for (const e of r.errors || []) md += `* load error${r.unexpectedErrors.includes(e) ? '' : ' (expected)'}: ${e.split('\n').slice(0, 3).join(' ').slice(0, 400)}\n`;
      for (const p of r.problems) if (!p.startsWith('load error')) md += `* ${p}\n`;
    }
  }
  fs.writeFileSync(path.join(here, 'RESULTS.md'), md);
}

main().catch((e) => {
  console.error(e);
  process.exit(2);
});
