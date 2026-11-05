const http = require('http'); const fs = require('fs');
const log = process.argv[2];
for (const port of [8081, 8082]) http.createServer((req, res) => {
  let body = ''; req.on('data', c => body += c); req.on('end', () => {
    fs.appendFileSync(log, JSON.stringify({port, method: req.method, url: req.url, tenant: req.headers['x-tenant'], ct: (req.headers['content-type']||'').split(';')[0], body: body.slice(0, 120)}) + '\n');
    res.writeHead(200, {'content-type': 'application/json'}); res.end('{}');
  });
}).listen(port);
