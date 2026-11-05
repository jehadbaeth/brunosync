// Round trips every collection file through Bruno's own filestore (what the
// app does on save) and reports semantic differences.
const fs = require('fs'), path = require('path');
const fsx = require('@usebruno/filestore');
const yaml = require('js-yaml');
const root = process.argv[2];
let bad = 0, n = 0;
function diff(a, b, p = '') {
  if (JSON.stringify(a) === JSON.stringify(b)) return [];
  if (a && b && typeof a === 'object' && typeof b === 'object' && !Array.isArray(a) && !Array.isArray(b)) {
    const keys = new Set([...Object.keys(a), ...Object.keys(b)]);
    return [...keys].flatMap(k => diff(a[k], b[k], p + '.' + k));
  }
  if (Array.isArray(a) && Array.isArray(b) && a.length === b.length) return a.flatMap((x, i) => diff(x, b[i], `${p}[${i}]`));
  return [`${p}: ${JSON.stringify(a)?.slice(0,80)} => ${JSON.stringify(b)?.slice(0,80)}`];
}
function walk(d) {
  for (const e of fs.readdirSync(d, { withFileTypes: true })) {
    const p = path.join(d, e.name);
    if (e.isDirectory()) { if (e.name !== '.brunosync') walk(p); continue; }
    if (!e.name.endsWith('.yml')) continue;
    const src = fs.readFileSync(p, 'utf8');
    let out;
    try {
      if (e.name === 'folder.yml') out = fsx.stringifyFolder(fsx.parseFolder(src, { format: 'yml' }), { format: 'yml' });
      else if (e.name === 'opencollection.yml') continue;
      else out = fsx.stringifyRequest(fsx.parseRequest(src, { format: 'yml' }), { format: 'yml' });
    } catch (err) { console.log('PARSE FAIL', p, err.message); bad++; continue; }
    n++;
    const d1 = diff(yaml.load(src, { schema: yaml.CORE_SCHEMA }), yaml.load(out, { schema: yaml.CORE_SCHEMA }));
    if (d1.length) { bad++; console.log('DIFF', path.relative(root, p)); d1.forEach(x => console.log('   ', x)); }
  }
}
walk(root);
const coll = fs.readFileSync(path.join(root, 'opencollection.yml'), 'utf8');
const parsed = fsx.parseCollection(coll, { format: 'yml' });
console.log('collection vars parsed by Bruno:', JSON.stringify((parsed.collectionRoot||parsed.root||parsed)?.request?.vars?.req?.map(v => v.name + '=' + v.value) ?? Object.keys(parsed)));
console.log(`${n} files round tripped, ${bad} with differences`);
if (bad > 0) process.exit(1);
