// Saves every collection file the way the Bruno app does (Bruno's own parse
// followed by its own stringify) into a copy of the collection:
//
//   node roundtrip.js <collection> <copy>
//
// Then `brunosync debug compare <collection> <copy>` checks that brunosync
// sees no difference, i.e. a save in Bruno never looks like a user edit.
const fs = require('fs'), path = require('path');
const fsx = require('@usebruno/filestore');
const [root, out] = process.argv.slice(2);
if (!root || !out) {
  console.error('usage: node roundtrip.js <collection> <copy>');
  process.exit(2);
}
let n = 0, failed = 0;
(function walk(d) {
  for (const e of fs.readdirSync(d, { withFileTypes: true })) {
    const p = path.join(d, e.name);
    const target = path.join(out, path.relative(root, p));
    if (e.isDirectory()) {
      if (e.name !== '.brunosync') walk(p);
      continue;
    }
    if (!e.name.endsWith('.yml')) continue;
    fs.mkdirSync(path.dirname(target), { recursive: true });
    const src = fs.readFileSync(p, 'utf8');
    try {
      let saved;
      if (e.name === 'opencollection.yml') saved = src; // collection settings: not merged per field
      else if (e.name === 'folder.yml') saved = fsx.stringifyFolder(fsx.parseFolder(src, { format: 'yml' }), { format: 'yml' });
      else saved = fsx.stringifyRequest(fsx.parseRequest(src, { format: 'yml' }), { format: 'yml' });
      fs.writeFileSync(target, saved);
      n++;
    } catch (err) {
      failed++;
      console.log('BRUNO CANNOT PARSE', path.relative(root, p), err.message);
    }
  }
})(root);
console.log(`${n} files saved through Bruno, ${failed} unparseable`);
process.exit(failed ? 1 : 0);
