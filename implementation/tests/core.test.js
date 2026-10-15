'use strict';

/**
 * F-3 单元测试：effective_until 边界 + 三态（STALE/OFFLINE/DEGRADED）约束
 * F-2 单元测试：撤回记忆只增不减 + 跨重启缓存可续显性
 *
 * 零依赖，直接 `node tests/core.test.js` 运行。
 * 被测对象为 terminal/core.js（浏览器与 Node 共用的纯逻辑）。
 */

const assert = require('assert');
const path = require('path');
const Core = require(path.join(__dirname, '..', 'terminal', 'core.js'));

let passed = 0;
let failed = 0;
const failures = [];

function test(name, fn) {
  try { fn(); passed++; console.log('  PASS  ' + name); }
  catch (e) { failed++; failures.push({ name, err: e }); console.log('  FAIL  ' + name + '\n        ' + e.message); }
}

function section(title) { console.log('\n=== ' + title + ' ==='); }

const H = 3600 * 1000;
const NOW = Date.parse('2026-10-15T12:00:00+08:00');
const iso = (ms) => new Date(ms).toISOString();

// ------------------------------------------------------------------
section('F-3.1 effective_until 基础边界');

test('无 effective_until → 永不过期', () => {
  assert.strictEqual(Core.isExpired({ id: 'a' }, NOW), false);
});

test('effective_until 已过（含宽限）→ 过期', () => {
  const it = { id: 'a', effective_until: iso(NOW - 400 * 1000) };
  assert.strictEqual(Core.isExpired(it, NOW), true);
});

test('effective_until 尚未到达 → 未过期', () => {
  const it = { id: 'a', effective_until: iso(NOW + 400 * 1000) };
  assert.strictEqual(Core.isExpired(it, NOW), false);
});

test('宽限 300s 内仍保留（NOW = until + 299s → 未过期）', () => {
  const until = NOW - 299 * 1000;
  const it = { id: 'a', effective_until: iso(until) };
  assert.strictEqual(Core.isExpired(it, NOW), false);
});

test('宽限边界：NOW = until + 300s → 过期（>= 判定）', () => {
  const until = NOW - 300 * 1000;
  const it = { id: 'a', effective_until: iso(until) };
  assert.strictEqual(Core.isExpired(it, NOW), true);
});

test('effective_until 时间戳不可解析 → 视为未过期（不猜测）', () => {
  const it = { id: 'a', effective_until: 'not-a-date' };
  assert.strictEqual(Core.isExpired(it, NOW), false);
});

test('带 +08:00 时区偏移的 ISO 时间可正确解析', () => {
  const it = { id: 'a', effective_until: '2026-10-15T11:00:00+08:00' }; // = 10:00Z
  assert.strictEqual(Core.isExpired(it, NOW), true);   // 12:00+08 已过 11:00
});

// ------------------------------------------------------------------
section('F-3.2 effective_from 未生效边界');

test('effective_from 在未来 → 未生效', () => {
  const it = { id: 'a', effective_from: iso(NOW + 60 * 1000) };
  assert.strictEqual(Core.isNotYetEffective(it, NOW), true);
});

test('effective_from 已到 → 生效', () => {
  const it = { id: 'a', effective_from: iso(NOW - 60 * 1000) };
  assert.strictEqual(Core.isNotYetEffective(it, NOW), false);
});

test('无 effective_from → 视为已生效', () => {
  assert.strictEqual(Core.isNotYetEffective({ id: 'a' }, NOW), false);
});

// ------------------------------------------------------------------
section('F-3.3 到期预警窗口');

test('距到期 30min → 预警', () => {
  const it = { id: 'a', effective_until: iso(NOW + 30 * 60 * 1000) };
  assert.strictEqual(Core.expireWarn(it, NOW), true);
});

test('距到期 2h → 不预警', () => {
  const it = { id: 'a', effective_until: iso(NOW + 2 * H) };
  assert.strictEqual(Core.expireWarn(it, NOW), false);
});

test('已过期 → 不再预警（避免同时显示"即将过期"）', () => {
  const it = { id: 'a', effective_until: iso(NOW - 10 * 1000) };
  assert.strictEqual(Core.expireWarn(it, NOW), false);
});

// ------------------------------------------------------------------
section('F-3.4 selectVisibleItems 组合过滤');

test('同时剔除：已撤回 / 未生效 / 已过期，仅保留有效项', () => {
  const items = [
    { id: 'ok',      effective_from: iso(NOW - H), effective_until: iso(NOW + H) },
    { id: 'wd',      effective_from: iso(NOW - H), effective_until: iso(NOW + H) }, // 撤回
    { id: 'future',  effective_from: iso(NOW + H), effective_until: iso(NOW + 2 * H) },
    { id: 'expired', effective_from: iso(NOW - 2 * H), effective_until: iso(NOW - 400 * 1000) }
  ];
  const sel = Core.selectVisibleItems(items, [{ id: 'wd' }], NOW);
  assert.deepStrictEqual(sel.visible.map((i) => i.id), ['ok']);
  assert.deepStrictEqual(sel.removedWithdrawn, ['wd']);
  assert.deepStrictEqual(sel.removedNotYet, ['future']);
  assert.deepStrictEqual(sel.removedExpired, ['expired']);
});

test('过滤顺序：撤回优先于过期（撤回项不再计入过期统计）', () => {
  const items = [{ id: 'x', effective_from: iso(NOW - 2 * H), effective_until: iso(NOW - 400 * 1000) }];
  const sel = Core.selectVisibleItems(items, [{ id: 'x' }], NOW);
  assert.deepStrictEqual(sel.removedWithdrawn, ['x']);
  assert.deepStrictEqual(sel.removedExpired, []);
});

test('全部条目被过滤 → visible 为空（触发 EMPTY 空屏）', () => {
  const items = [{ id: 'x', effective_until: iso(NOW - 1 * H) }];
  const sel = Core.selectVisibleItems(items, [], NOW);
  assert.strictEqual(sel.visible.length, 0);
});

// ------------------------------------------------------------------
section('F-3.5 三态一致性：STALE / OFFLINE / DEGRADED 同样执行时效过滤');

test('OFFLINE 续显时，过期条目仍被本地剔除', () => {
  // 离线场景：now 已远超 effective_until，即便续显也不得展示过期内容
  const items = [
    { id: 'fresh', effective_until: iso(NOW + H) },
    { id: 'stale', effective_until: iso(NOW - 10 * 60 * 1000) }
  ];
  const offlineNow = NOW + 10 * 60 * 1000; // 离线 10 分钟后渲染
  const sel = Core.selectVisibleItems(items, [], offlineNow);
  assert.deepStrictEqual(sel.visible.map((i) => i.id), ['fresh']);
});

test('STALE_SOFT（4h~72h）续显时仍执行 effective_until 过滤', () => {
  const items = [{ id: 'a', effective_until: iso(NOW + 2 * H) }];
  const later = NOW + 6 * H; // 6h 后：内容已过 3h → 应被剔除
  const sel = Core.selectVisibleItems(items, [], later);
  assert.strictEqual(sel.visible.length, 0);
});

test('DEGRADED（未验签）路径由 canRenderFromCache 拒绝，不进入渲染', () => {
  assert.strictEqual(Core.canRenderFromCache({ verified: false, bundle: { items: [] } }), false);
  assert.strictEqual(Core.canRenderFromCache(null), false);
  assert.strictEqual(Core.canRenderFromCache({ verified: true, bundle: {} }), false);
  assert.strictEqual(Core.canRenderFromCache({ verified: true, bundle: { items: [] } }), true);
});

test('三态均使用同一 selectVisibleItems（函数无网络状态入参，语义恒定）', () => {
  // 同一函数被 S4_VALID / S5_STALE_SOFT / S7_OFFLINE 共用，此处锁定签名契约
  assert.strictEqual(typeof Core.selectVisibleItems, 'function');
  const items = [{ id: 'a', effective_until: iso(NOW + H) }];
  const r1 = Core.selectVisibleItems(items, [], NOW);
  const r2 = Core.selectVisibleItems(items, [], NOW);
  assert.deepStrictEqual(r1.visible.map(i => i.id), r2.visible.map(i => i.id));
});

// ------------------------------------------------------------------
section('F-3.6 陈旧度分级（对齐 03 S0–S9：ttl 15min / 4h 软 / 72h 硬）');

test('age < 15min → FRESH（03 S5 LIVE）', () => {
  assert.strictEqual(Core.computeStaleness(14 * 60 * 1000), 'FRESH');
});
test('age ≥ 15min → STALE（03 S6，去饱和 + 数据截至）', () => {
  assert.strictEqual(Core.computeStaleness(15 * 60 * 1000), 'STALE');
});
test('age ≥ 4h → STALE_SOFT（叠加软提示）', () => {
  assert.strictEqual(Core.computeStaleness(4 * H), 'STALE_SOFT');
});
test('age ≥ 72h → BLOCKED（03 S8，零业务内容）', () => {
  assert.strictEqual(Core.computeStaleness(72 * H), 'BLOCKED');
});
test('ttl 边界：14m59s → FRESH；15m00s → STALE（>= 判定）', () => {
  assert.strictEqual(Core.computeStaleness(15 * 60 * 1000 - 1), 'FRESH');
  assert.strictEqual(Core.computeStaleness(15 * 60 * 1000), 'STALE');
});
test('72h 边界 → BLOCKED（>= 判定，硬 BLOCKED 优先）', () => {
  assert.strictEqual(Core.computeStaleness(72 * H - 1), 'STALE_SOFT');
  assert.strictEqual(Core.computeStaleness(72 * H), 'BLOCKED');
});

// ------------------------------------------------------------------
section('F-3.7 content_hash 载荷（03 §4/§S3 完整性校验）');

test('contentHashPayload 仅覆盖 items + tombstones', () => {
  const p = Core.contentHashPayload({ items: [{ id: 'a' }], tombstones: [], profile: 'classroom', signature: 'x' });
  assert.strictEqual(p, Core.canonicalize({ items: [{ id: 'a' }], tombstones: [] }));
});
test('contentHashPayload 对键序不敏感（可跨语言对齐服务端）', () => {
  const a = Core.contentHashPayload({ items: [{ id: 'a', title: 't' }], tombstones: [] });
  const b = Core.contentHashPayload({ tombstones: [], items: [{ title: 't', id: 'a' }] });
  assert.strictEqual(a, b);
});
test('contentHashPayload 对条目顺序敏感（顺序改变 → 摘要改变）', () => {
  const a = Core.contentHashPayload({ items: [{ id: 'a' }, { id: 'b' }], tombstones: [] });
  const b = Core.contentHashPayload({ items: [{ id: 'b' }, { id: 'a' }], tombstones: [] });
  assert.notStrictEqual(a, b);
});
test('contentHashPayload 空 Bundle → 稳定空载荷（不抛异常）', () => {
  assert.strictEqual(Core.contentHashPayload({}), Core.canonicalize({ items: [], tombstones: [] }));
  assert.strictEqual(Core.contentHashPayload(null), Core.canonicalize({ items: [], tombstones: [] }));
});

// ------------------------------------------------------------------
section('F-2 撤回记忆跨重启：只增不减');

test('mergeWithdrawn：并集且去重', () => {
  const out = Core.mergeTombstoneIds(['a', 'b'], [{ id: 'b' }, { id: 'c' }]);
  assert.deepStrictEqual(out.sort(), ['a', 'b', 'c']);
});

test('撤回记忆不因后续 Bundle 缺少该 tombstone 而丢失（不可逆）', () => {
  // 服务端裁剪 tombstone 后再次下发，终端仍须记得已撤回
  const persisted = ['ann-001'];
  const incoming = []; // 新 Bundle 不再携带 ann-001
  const out = Core.mergeTombstoneIds(persisted, incoming);
  assert.deepStrictEqual(out, ['ann-001']);
});

test('tombstoneSet 支持对象与字符串两种形态', () => {
  const s = Core.tombstoneSet([{ id: 'a' }, 'b', null, { noId: 1 }]);
  assert.deepStrictEqual(Array.from(s).sort(), ['a', 'b']);
});

test('applyTombstones 移除命中项（ADR-007 硬删除的本地体现）', () => {
  const items = [{ id: 'a' }, { id: 'b' }];
  assert.deepStrictEqual(Core.applyTombstones(items, [{ id: 'a' }]).map((i) => i.id), ['b']);
});

test('跨重启场景：缓存含已撤回项 + 持久化撤回记忆 → 重启后不再展示', () => {
  // 模拟：上次运行验签通过并落盘；此后离线期间服务端已撤回 w1
  const cachedBundle = { items: [{ id: 'w1' }, { id: 'keep' }], tombstones: [] };
  const persistedWithdrawn = ['w1'];                       // 撤回记忆已在本地
  const merged = new Set(Core.mergeTombstoneIds(persistedWithdrawn, cachedBundle.tombstones));
  const sel = Core.selectVisibleItems(cachedBundle.items, merged, NOW);
  assert.deepStrictEqual(sel.visible.map((i) => i.id), ['keep']); // w1 不再出现
});

// ------------------------------------------------------------------
section('签名规范化一致性（回归保护）');

test('canonicalize 排除 signature 与 sign_alg', () => {
  const a = Core.canonicalize({ b: 1, a: 2 });
  const b = Core.canonicalize({ a: 2, b: 1, signature: 'x', sign_alg: 'HMAC-SHA256' });
  assert.strictEqual(a, b);
});
test('canonicalize 键序无关（对象键升序）', () => {
  assert.strictEqual(
    Core.canonicalize({ z: 1, a: { y: 2, x: 3 } }),
    Core.canonicalize({ a: { x: 3, y: 2 }, z: 1 })
  );
});
test('canonicalize 数组保持顺序（顺序敏感）', () => {
  assert.notStrictEqual(Core.canonicalize([1, 2]), Core.canonicalize([2, 1]));
});

// ------------------------------------------------------------------
console.log('\n' + '='.repeat(46));
console.log(`结果：${passed} 通过 / ${failed} 失败 / 共 ${passed + failed}`);
if (failed > 0) {
  console.log('\n失败明细：');
  failures.forEach((f, i) => console.log(`${i + 1}. ${f.name}\n   ${f.err.stack.split('\n')[0]}`));
  process.exit(1);
}
console.log('全部通过 ✅');