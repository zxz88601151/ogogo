'use strict';

const http = require('http');
const fs = require('fs');
const path = require('path');
const { buildBundle } = require('./lib/bundle');
const { verifyBundle } = require('./lib/sign');

const PORT = process.env.PORT || 8080;
const SECRET = process.env.EDUDESK_SECRET || 'dev-only-secret-change-me';
const SOURCE_PATH = path.join(__dirname, 'data', 'content-source.json');
const TERMINAL_DIR = path.join(__dirname, '..', 'terminal');
const POLL_NOTE = 'terminal polls every 60s (design 05)';

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'application/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml'
};

// 静态服务终端（同源，免 CORS，契合 kiosk 直接开 localhost:PORT）
function serveTerminal(req, res, url) {
  let rel = url.pathname === '/' ? '/index.html' : url.pathname;
  if (rel.startsWith('/terminal')) rel = rel.slice('/terminal'.length) || '/index.html';
  const filePath = path.normalize(path.join(TERMINAL_DIR, rel));
  if (!filePath.startsWith(TERMINAL_DIR)) { // 目录穿越防护
    res.writeHead(403); res.end('Forbidden'); return;
  }
  fs.readFile(filePath, (err, data) => {
    if (err) { res.writeHead(404); res.end('Not Found'); return; }
    res.writeHead(200, { 'Content-Type': MIME[path.extname(filePath)] || 'application/octet-stream' });
    res.end(data);
  });
}

let sourceCache = null;
let bundleCache = { classroom: null, corridor: null, at: 0 };

function loadSource() {
  const raw = fs.readFileSync(SOURCE_PATH, 'utf8');
  sourceCache = JSON.parse(raw);
  rebuildBundles();
}

function rebuildBundles() {
  for (const profile of ['classroom', 'corridor']) {
    const r = buildBundle({ profile, source: sourceCache, secret: SECRET });
    if (!r.ok) {
      console.error(`[bundle] ${profile} build FAILED: ${r.error}`, r.lint && r.lint.violations);
      continue;
    }
    bundleCache[profile] = r.bundle;
  }
  bundleCache.at = Date.now();
  console.log(`[bundle] rebuilt at ${new Date().toISOString()}`);
}

// 文件变更自动重建（服务端写方落盘后热重载，无入站写 API）
fs.watch(SOURCE_PATH, () => {
  try { loadSource(); } catch (e) { console.error('[watch] reload error', e.message); }
});

loadSource();

const server = http.createServer((req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const method = req.method.toUpperCase();

  // 只读 API：仅允许 GET / HEAD，其余一律 405 + Allow
  if (!['GET', 'HEAD'].includes(method)) {
    res.writeHead(405, {
      'Allow': 'GET, HEAD',
      'Content-Type': 'application/json; charset=utf-8'
    });
    res.end(JSON.stringify({ error: 'METHOD_NOT_ALLOWED', allow: ['GET', 'HEAD'] }));
    return;
  }

  if (url.pathname === '/health') {
    res.writeHead(200, { 'Content-Type': 'application/json; charset=utf-8' });
    if (method === 'HEAD') { res.end(); return; }
    res.end(JSON.stringify({ status: 'OK', bundles_at: bundleCache.at, poll: POLL_NOTE }));
    return;
  }

  if (url.pathname === '/bundle') {
    const profile = url.searchParams.get('profile') || 'classroom';
    if (!['classroom', 'corridor'].includes(profile)) {
      res.writeHead(400, { 'Content-Type': 'application/json; charset=utf-8' });
      res.end(JSON.stringify({ error: 'BAD_PROFILE', allowed: ['classroom', 'corridor'] }));
      return;
    }
    const bundle = bundleCache[profile];
    if (!bundle) {
      res.writeHead(503, { 'Content-Type': 'application/json; charset=utf-8' });
      res.end(JSON.stringify({ error: 'BUNDLE_UNAVAILABLE' }));
      return;
    }
    const body = JSON.stringify(bundle);
    res.writeHead(200, {
      'Content-Type': 'application/json; charset=utf-8',
      'Cache-Control': 'no-store',
      'X-Edudesk-Profile': profile,
      'X-Edudesk-Generated': bundle.generated_at
    });
    if (method === 'HEAD') { res.end(); return; }
    res.end(body);
    return;
  }

  // 未知路径：回退到终端静态资源（同源 kiosk）
  if (method === 'GET' || method === 'HEAD') {
    return serveTerminal(req, res, url);
  }

  res.writeHead(404, { 'Content-Type': 'application/json; charset=utf-8' });
  res.end(JSON.stringify({ error: 'NOT_FOUND' }));
});

server.listen(PORT, () => {
  console.log(`EduDesk server (readonly) listening on :${PORT}`);
  console.log(`  GET /bundle?profile=classroom|corridor`);
  console.log(`  GET /health`);
  console.log(`  All non-GET/HEAD -> 405 Allow: GET, HEAD`);
});

// 自检：启动时验一次签名，证明 bundle 可验证
if (bundleCache.classroom) {
  const v = verifyBundle(bundleCache.classroom, SECRET);
  console.log(`[selfcheck] classroom signature verify: ${v.ok ? 'PASS' : 'FAIL ' + v.reason}`);
}
