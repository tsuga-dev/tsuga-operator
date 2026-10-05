// Deliberately contains no OpenTelemetry code: if spans reach Tsuga, the
// operator's auto-instrumentation injection is what produced them.
const http = require('http');

const PORT = 8080;

const server = http.createServer((req, res) => {
  console.log(JSON.stringify({ level: 'info', msg: 'handled', path: req.url }));
  res.writeHead(200, { 'Content-Type': 'text/plain' });
  res.end('ok\n');
});

server.listen(PORT, () => {
  console.log(JSON.stringify({ level: 'info', msg: 'listening', port: PORT }));
  // Self-driving: no external load generator needed.
  setInterval(() => {
    http.get(`http://127.0.0.1:${PORT}/work`, (res) => res.resume())
      .on('error', (err) => console.log(JSON.stringify({ level: 'warn', msg: err.message })));
  }, 2000);
});
