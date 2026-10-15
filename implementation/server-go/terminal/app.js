'use strict';

/**
 * EduDesk 终端渲染器（对齐设计 03/04/05 + G-4 + F-2/F-3）
 *
 * 状态机严格对齐 03 §5 的 S0–S9：
 *   S0 BOOT / S1 NET_PROBE / S2 FETCH / S3 VERIFY / S4 APPLY /
 *   S5 LIVE / S6 STALE / S7 OFFLINE_SERVING / S8 BLOCKED / S9 DEGRADED
 *
 *  - 双 Profile：?profile=classroom|corridor（默认 classroom）
 *  - S1 探活 GET /health（3s 超时），读取 server_time 做时钟漂移检测（05 §3）
 *  - 60s 轮询；S3 同时校验 signature 与 content_hash（03 §4/§S3）
 *  - 本地 effective_from / effective_until 过滤（core.js，与网络状态无关）
 *  - S6 STALE：ttl 15min 起去饱和；≥4h 叠加软提示；S8 BLOCKED：≥72h 零业务内容
 *
 * F-2：撤回记忆跨重启持久化（只增不减） + 陈旧度以 verifiedAt 为基准跨重启延续。
 *
 * 安全说明：HMAC 验签在真实部署应使用非对称公钥（Ed25519）验签（D-09 后期项），
 *          此处沿用服务端 HMAC 密钥，仅供 MVP 演示。
 *
 * 已知取舍（需 Owner 复核）：05 §3 规定时钟漂移 > 15min 时"转 S9"。S9 的语义是
 *          "保留上一 good bundle + 本地告警"，因此该分支仍会显示已验证的历史内容，
 *          只是叠加漂移徽标并标记状态。此行为严格照 05 原文实现，未自行加严或放宽。
 */

const Core = (typeof window !== 'undefined' && window.EduDeskCore) ? window.EduDeskCore : require('./core.js');

const CONFIG = {
  bundleUrl: () => `/bundle?profile=${getProfile()}`,
  healthUrl: '/health',
  pollIntervalMs: 60_000,          // 设计 05：60s 轮询
  healthTimeoutMs: 3_000,          // 设计 03 S1：探活超时 3s
  driftWarnMs: 60_000,             // 05 §3：漂移 > 60s → 徽标
  driftBlockMs: 15 * 60_000,       // 05 §3：漂移 > 15min → 转 S9
  secret: 'dev-only-secret-change-me',
  storeKeys: {
    lastGood: 'edudesk.lastgood.v1',
    withdrawn: 'edudesk.withdrawn.v1'
  }
};

// ---- 状态机（对齐 03 §5 S0–S9）----
const S = {
  S0_BOOT: 'S0 BOOT',
  S1_NET_PROBE: 'S1 NET_PROBE',
  S2_FETCH: 'S2 FETCH',
  S3_VERIFY: 'S3 VERIFY',
  S4_APPLY: 'S4 APPLY',
  S5_LIVE: 'S5 LIVE',
  S6_STALE: 'S6 STALE',
  S7_OFFLINE: 'S7 OFFLINE_SERVING',
  S8_BLOCKED: 'S8 BLOCKED',
  S9_DEGRADED: 'S9 DEGRADED'
};
let currentState = S.S0_BOOT;
let lastVerifiedAt = 0;        // 最近一次「验签通过」的时刻（跨重启延续，陈旧度基准）
let lastBundle = null;         // 最近一次已验证 Bundle（last-good，内存引用）
let restoredFromCache = false; // 当前渲染内容是否来自跨重启缓存
let probeFailures = 0;         // 连续探活失败次数（S7 触发：连续 3 次）

// 撤回记忆：跨重启持久化，只增不减
let withdrawnIds = new Set();

const TYPE_LABEL = {
  announcement: '公告', homework: '作业', schedule: '课表',
  event: '活动', utility: '服务', weather: '天气'
};
const WEEKDAY = ['日', '一', '二', '三', '四', '五', '六'];

function getProfile() {
  const p = new URLSearchParams(location.search).get('profile');
  return p === 'corridor' ? 'corridor' : 'classroom';
}
function setState(s) { currentState = s; document.getElementById('stateInfo').textContent = s; }
function setNet(level, text) {
  const b = document.getElementById('netBadge');
  b.dataset.level = level; b.textContent = text;
}
function setStale(on) { document.body.dataset.stale = on ? 'true' : 'false'; }

// ---- 持久化（F-2）----
function lsGet(key) {
  try { return window.localStorage.getItem(key); } catch (e) { return null; }
}
function lsSet(key, val) {
  try { window.localStorage.setItem(key, val); } catch (e) { /* 隐私模式/配额：降级为内存态 */ }
}

/** 载入跨重启缓存：仅接受曾经验签通过的内容。 */
function loadCache() {
  const rawGood = lsGet(CONFIG.storeKeys.lastGood);
  const rawWd = lsGet(CONFIG.storeKeys.withdrawn);
  if (rawWd) {
    try { withdrawnIds = new Set(JSON.parse(rawWd) || []); } catch (e) { withdrawnIds = new Set(); }
  }
  if (!rawGood) return null;
  try {
    const cached = JSON.parse(rawGood);
    if (!Core.canRenderFromCache(cached)) return null;   // 未验证缓存 → 拒绝续显
    return cached;
  } catch (e) { return null; }
}

/** 验签通过后落盘：last-good + 撤回记忆（合并，只增不减）。 */
function persistVerified(bundle) {
  lsSet(CONFIG.storeKeys.lastGood, JSON.stringify({
    verified: true,
    verifiedAt: Date.now(),
    profile: getProfile(),
    bundle
  }));
  mergeWithdrawn(bundle.tombstones);
}

/**
 * 合并撤回记忆：只增不减。
 * 依据 ADR-007 —— 撤回是硬删除，一旦知悉某 ID 已撤回，
 * 本地不得因后续 Bundle 未再携带该 tombstone 而"忘记"撤回。
 */
function mergeWithdrawn(tombstones) {
  const incoming = Core.tombstoneSet(tombstones);
  let changed = false;
  incoming.forEach((id) => { if (!withdrawnIds.has(id)) { withdrawnIds.add(id); changed = true; } });
  if (changed) lsSet(CONFIG.storeKeys.withdrawn, JSON.stringify(Array.from(withdrawnIds)));
}

// ---- 验签与完整性（signature + content_hash）----
function bufToB64(buf) {
  let s = ''; const bytes = new Uint8Array(buf);
  for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
  return btoa(s);
}
function bufToHex(buf) {
  const bytes = new Uint8Array(buf); let s = '';
  for (let i = 0; i < bytes.length; i++) s += bytes[i].toString(16).padStart(2, '0');
  return s;
}
async function hmacSha256(message, secret) {
  const enc = new TextEncoder();
  const key = await crypto.subtle.importKey('raw', enc.encode(secret),
    { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const sig = await crypto.subtle.sign('HMAC', key, enc.encode(message));
  return bufToB64(sig);
}
async function sha256Hex(message) {
  const enc = new TextEncoder();
  const d = await crypto.subtle.digest('SHA-256', enc.encode(message));
  return bufToHex(d);
}

/**
 * 校验 Bundle：签名 + 内容摘要（03 §S3）。
 * 任一失败都返回 ok:false —— 未验证内容绝不渲染。
 */
async function verifyBundle(bundle, secret) {
  if (!bundle || !bundle.signature) return { ok: false, reason: 'SIGNATURE_MISSING' };

  // 1) 整体签名（排除 signature / sign_alg，二者为签发后附加字段）
  const { signature, sign_alg, ...rest } = bundle;
  const expected = await hmacSha256(Core.canonicalize(rest), secret);
  if (expected.length !== signature.length) return { ok: false, reason: 'SIGNATURE_MISMATCH' };
  let diff = 0;
  for (let i = 0; i < expected.length; i++) if (expected[i] !== signature[i]) diff++;
  if (diff !== 0) return { ok: false, reason: 'SIGNATURE_MISMATCH' };

  // 2) content_hash（覆盖 items + tombstones）
  if (bundle.content_hash) {
    const got = 'sha256:' + await sha256Hex(Core.contentHashPayload(bundle));
    if (got !== bundle.content_hash) return { ok: false, reason: 'CONTENT_HASH_MISMATCH' };
  } else {
    // 设计 03 要求存在 content_hash；缺失视为不可信（不降级放行）。
    return { ok: false, reason: 'CONTENT_HASH_MISSING' };
  }
  return { ok: true, reason: 'OK' };
}

// ---- 空屏文案（G-4 §5.7）----
const EMPTY_COPY = {
  none: { icon: '📭', title: '暂无信息发布', text: '当前没有可展示的内容，敬请期待。' },
  loadfail: { icon: '🔒', title: '内容校验未通过', text: '为确保信息安全，已停止显示。请联系管理员处理。' },
  offline: { icon: '📡', title: '网络中断', text: '正在显示已缓存的已验证内容，恢复网络后将自动更新。' },
  stalehard: { icon: '⏱', title: '信息已超时', text: '已超过 72 小时未更新，按规范停止显示。请联系管理员。' },
  maintenance: { icon: '🛠', title: '设备维护中', text: '本屏暂停显示，敬请谅解。' }
};
function showOverlay(kind) {
  const c = EMPTY_COPY[kind]; const ov = document.getElementById('overlay');
  document.getElementById('ovIcon').textContent = c.icon;
  document.getElementById('ovTitle').textContent = c.title;
  document.getElementById('ovText').textContent = c.text;
  ov.classList.remove('hidden');
}
function hideOverlay() { document.getElementById('overlay').classList.add('hidden'); }

// ---- 渲染 ----
function renderCards(bundle, now) {
  // 撤回过滤同时应用：本次 Bundle 的 tombstones ∪ 跨重启持久化的撤回记忆（F-2）
  const tomb = new Set(Core.tombstoneSet(bundle.tombstones));
  withdrawnIds.forEach((id) => tomb.add(id));

  // 时效过滤与网络状态无关 —— STALE / OFFLINE 续显同样执行（F-3）
  const sel = Core.selectVisibleItems(bundle.items || [], tomb, now, {
    expireGraceMs: Core.DEFAULTS.expireGraceMs
  });
  const items = sel.visible;
  const cardsEl = document.getElementById('cards');
  cardsEl.innerHTML = '';

  if (items.length === 0) {
    // 空态折叠（T-A11）：仍属 S5 LIVE，只是无内容可展示
    setState(S.S5_LIVE); showOverlay('none'); return;
  }
  hideOverlay();
  items.sort((a, b) => (a.priority || 9) - (b.priority || 9));
  for (const it of items) {
    const el = document.createElement('article');
    el.className = 'card'; el.dataset.type = it.type;
    const expWarn = Core.expireWarn(it, now);
    el.innerHTML = `
      <div class="ctype">${TYPE_LABEL[it.type] || it.type}${it.audience && it.audience.grade ? ' · ' + gradeText(it.audience) : ''}</div>
      <div class="ctitle">${escapeHtml(it.title || '')}</div>
      <div class="csummary">${escapeHtml(it.summary || '')}</div>
      ${expWarn ? '<div class="cexpire">⏰ 即将过期</div>' : ''}
      <div class="cmeta">${escapeHtml(it.location || '')}</div>`;
    cardsEl.appendChild(el);
  }
}
function gradeText(a) {
  if (a.class) return `${a.grade || ''}${a.class}班`;
  return a.grade || '';
}
function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// ---- 时钟（本地时钟驱动；与数据时效戳物理分离，05 §3）----
function tickClock() {
  const d = new Date();
  const p = (n) => String(n).padStart(2, '0');
  const showSec = getProfile() === 'classroom'; // 走廊屏不显示秒（减少闪动）
  document.getElementById('clock').textContent = showSec
    ? `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
    : `${p(d.getHours())}:${p(d.getMinutes())}`;
  document.getElementById('clockDate').textContent =
    `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} 星期${WEEKDAY[d.getDay()]}`;
}

// ---- 同步循环 ----
/**
 * S1 探活 + 时钟漂移检测（05 §3）。
 * 返回 true 表示可继续拉取；false 表示已转 S7/S9 不应继续。
 */
async function probeHealth() {
  setState(S.S1_NET_PROBE);
  let health;
  try {
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), CONFIG.healthTimeoutMs);
    const r = await fetch(CONFIG.healthUrl, { cache: 'no-store', signal: ctrl.signal });
    clearTimeout(timer);
    if (!r.ok) throw new Error('health not ok');
    health = await r.json();
  } catch (e) {
    return { ok: false, reason: 'PROBE_FAIL' };
  }

  // 时钟漂移：本地 vs 服务器
  let skew = 0;
  if (health && health.time) {
    const st = Date.parse(health.time);
    if (!Number.isNaN(st)) skew = Math.abs(Date.now() - st);
  }
  const driftEl = document.getElementById('driftBadge');
  if (skew > CONFIG.driftWarnMs) {
    driftEl.classList.remove('hidden');
    driftEl.textContent = skew > CONFIG.driftBlockMs ? '🔕 时钟严重偏差' : '🔕 时钟校准中';
  } else {
    driftEl.classList.add('hidden');
  }
  if (skew > CONFIG.driftBlockMs) {
    // 05 §3：>15min 无法校正 → 转 S9（所有时效判定不可信）。S9 保留 last-good + 告警。
    return { ok: false, reason: 'CLOCK_DRIFT', skew };
  }
  return { ok: true, reason: 'OK', skew };
}

async function poll() {
  // ---- S1 探活 ----
  const probe = await probeHealth();
  if (!probe.ok) {
    if (probe.reason === 'CLOCK_DRIFT') {
      setState(S.S9_DEGRADED);
      setNet('degraded', '时钟异常');
      if (lastBundle) { renderWithStaleness(true); } else { showOverlay('loadfail'); }
      return;
    }
    probeFailures++;
    // S7 OFFLINE_SERVING：连续 3 次探活失败
    if (probeFailures >= 3 || lastBundle) {
      onOffline();
    }
    return;
  }
  probeFailures = 0;

  // ---- S2 FETCH ----
  setState(S.S2_FETCH);
  let resp;
  try {
    resp = await fetch(CONFIG.bundleUrl(), { cache: 'no-store' });
  } catch (e) { return onOffline(); }
  if (!resp.ok) return onOffline();

  // ---- S3 VERIFY ----
  setState(S.S3_VERIFY);
  let bundle;
  try { bundle = await resp.json(); } catch (e) { return onDegraded('PARSE_ERROR'); }

  const v = await verifyBundle(bundle, CONFIG.secret);
  if (!v.ok) { lastBundle = null; return onDegraded(v.reason); }

  // ---- S4 APPLY ----
  setState(S.S4_APPLY);
  lastBundle = bundle;
  lastVerifiedAt = Date.now();
  restoredFromCache = false;
  persistVerified(bundle);

  // ---- S5/S6/S8 ----
  renderWithStaleness();
}

function onOffline() {
  setState(S.S7_OFFLINE);
  if (lastBundle) {
    setNet('offline', '离线·缓存');
    renderWithStaleness(true);
  } else {
    setNet('offline', '离线');
    showOverlay('offline');
  }
}
function onDegraded(reason) {
  lastBundle = null; // 未验证内容绝不显示
  setState(S.S9_DEGRADED);
  setNet('degraded', '校验失败');
  setStale(false);
  showOverlay('loadfail');
  console.warn('[EduDesk] degraded:', reason);
}

// 渲染并依据 staleness 决定续显/停止
function renderWithStaleness(offline) {
  if (!lastBundle) { showOverlay('loadfail'); return; }
  const now = Date.now();
  const age = now - lastVerifiedAt;
  const level = Core.computeStaleness(age);

  if (level === 'BLOCKED') {
    // S8 BLOCKED：零业务内容（03 §5.2 / ADR-004）
    setState(S.S8_BLOCKED);
    setNet('blocked', '已超时');
    setStale(false);
    showOverlay('stalehard');
    document.getElementById('cards').innerHTML = '';
    return;
  }
  if (level === 'STALE_SOFT') {
    setState(S.S6_STALE);
    setNet('stale-soft', '可能不是最新');
    setStale(true);
  } else if (level === 'STALE') {
    setState(S.S6_STALE);
    setNet('stale', '数据截至');
    setStale(true);
  } else {
    setState(S.S5_LIVE);
    setNet(offline ? 'offline' : 'ok', offline ? '离线·缓存' : '已同步');
    setStale(false);
  }
  renderCards(lastBundle, now);
  document.getElementById('syncInfo').textContent =
    `最近同步 ${fmtAge(age)}${restoredFromCache ? ' · 本次为重启前缓存' : ''}`;
}
function fmtAge(ms) {
  const m = Math.floor(ms / 60000);
  if (m < 60) return `${m} 分钟前`;
  const h = Math.floor(m / 60); return `${h} 小时前`;
}

// ---- 启动 ----
function start() {
  document.body.dataset.profile = getProfile();
  document.querySelector('.brand').textContent = getProfile() === 'corridor' ? '校园信息屏 · 走廊' : '校园信息屏 · 教室';
  tickClock(); setInterval(tickClock, 1000);
  setState(S.S0_BOOT); setNet('boot', '初始化'); showOverlay('maintenance');

  // F-2：先恢复跨重启缓存（曾经验签 + 未超 72h），再尝试联网
  const cached = loadCache();
  if (cached) {
    lastBundle = cached.bundle;
    lastVerifiedAt = cached.verifiedAt || Date.now();
    restoredFromCache = true;
    renderWithStaleness(true);   // 离线续显路径，同样执行 effective_until 过滤
  }

  // 每 1s 重算 staleness（时钟/超时推进，含离线态下 72h 硬 BLOCKED）
  setInterval(() => {
    if (!lastBundle || currentState === S.S9_DEGRADED || currentState === S.S8_BLOCKED) return;
    const offline = currentState === S.S7_OFFLINE;
    renderWithStaleness(offline);
  }, 1000);

  poll();
  setInterval(poll, CONFIG.pollIntervalMs);
}
start();
