'use strict';

/**
 * EduDesk 终端核心纯逻辑（对齐设计 03 / 05 / G-4）
 *
 * 本模块不触碰 DOM 与网络，可在浏览器与 Node 中同时运行，
 * 因此 F-3（effective_until 边界）可以对其做真正的单元测试。
 *
 * 覆盖的已冻结规则：
 *  - 03 §1.3 时效：effective_from 未到不展示；now ≥ effective_until 立即移出；
 *                  宽限 300s 后清盘（GRACE）。
 *  - 03 §1.5 撤回：tombstone 命中项一律不展示；撤回不可逆（ID 集合只增不减）。
 *  - 05 §陈旧：4h 软提示 / 72h 硬 BLOCKED。
 *  - 04 签名：规范化 JSON 时排除 signature 与 sign_alg（签发后附加字段）。
 */

(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) { module.exports = api; }
  if (root) { root.EduDeskCore = api; }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {

  /** 与设计 05 / 03 一致的默认阈值（毫秒）。 */
  const DEFAULTS = {
    expireGraceMs: 300000,      // 300s 宽限
    expireWarnMs: 3600000,      // 到期前 1h 提示
    ttlMs: 900000,              // 03 §4 ttl_sec=900 → 15min 进入 STALE（S6）
    staleSoftMs: 4 * 3600 * 1000,   // 4h 软提示子级（03 §5.1 / 05 §4 ②）
    staleHardMs: 72 * 3600 * 1000   // 72h 硬 BLOCKED（S8）
  };

  /**
   * 规范化 JSON（与服务端 sign 实现逐字节一致）。
   * 键按 UTF-16 码元升序（JS 默认 sort），排除 signature / sign_alg。
   */
  function canonicalize(obj) {
    if (obj === null || typeof obj !== 'object') return JSON.stringify(obj);
    if (Array.isArray(obj)) return '[' + obj.map(canonicalize).join(',') + ']';
    const keys = Object.keys(obj).sort();
    const parts = [];
    for (const k of keys) {
      if (k === 'signature' || k === 'sign_alg') continue;
      parts.push(JSON.stringify(k) + ':' + canonicalize(obj[k]));
    }
    return '{' + parts.join(',') + '}';
  }

  /** 解析 ISO8601 时间戳；不可解析返回 null（不猜测、不兜底成合法）。 */
  function ts(v) {
    if (!v) return null;
    const t = Date.parse(v);
    return Number.isNaN(t) ? null : t;
  }

  /**
   * 是否已过期（03 §1.3）。
   * 无 effective_until → 永不过期。
   * 时间戳不可解析 → 视为不过期，但由调用方决定是否告警。
   * now ≥ until + grace → 过期（宽限期内仍保留展示）。
   */
  function isExpired(item, now, graceMs) {
    const grace = graceMs === undefined ? DEFAULTS.expireGraceMs : graceMs;
    const until = ts(item && item.effective_until);
    if (until === null) return false;
    return now >= until + grace;
  }

  /** 是否尚未生效（effective_from 在未来）。03 §1.3：不展示未生效内容。 */
  function isNotYetEffective(item, now) {
    const from = ts(item && item.effective_from);
    if (from === null) return false;
    return now < from;
  }

  /** 是否处于到期预警窗口（剩余 ≤ warnMs 且仍为正）。 */
  function expireWarn(item, now, warnMs) {
    const warn = warnMs === undefined ? DEFAULTS.expireWarnMs : warnMs;
    const until = ts(item && item.effective_until);
    if (until === null) return false;
    const remain = until - now;
    return remain <= warn && remain > 0;
  }

  /** 把 tombstone 列表（对象或字符串）归一化为 ID 集合。 */
  function tombstoneSet(tombstones) {
    const s = new Set();
    if (!Array.isArray(tombstones)) return s;
    for (const t of tombstones) {
      if (typeof t === 'string') { if (t) s.add(t); }
      else if (t && typeof t === 'object' && t.id) { s.add(t.id); }
    }
    return s;
  }

  /**
   * 撤回合并（F-2 核心）：已撤回 ID 集合只增不减。
   * 撤回是硬删除 + tombstone，语义不可逆，因此本地缓存的撤回记忆
   * 不得因为一次新的（可能被服务端裁剪过的）Bundle 而丢失。
   */
  function mergeTombstoneIds(persisted, incoming) {
    // persisted：本地持久化的字符串 ID 数组（localStorage）
    const out = normalizeIds(persisted);
    // incoming：Bundle 内 tombstones（对象数组，含 id/reason/withdrawn_at）
    tombstoneSet(incoming).forEach((id) => out.add(id));
    return Array.from(out);
  }

  function normalizeIds(arr) {
    const s = new Set();
    if (Array.isArray(arr)) {
      for (const v of arr) { if (typeof v === 'string' && v) s.add(v); }
    } else if (arr instanceof Set) {
      for (const v of arr) { if (typeof v === 'string' && v) s.add(v); }
    }
    return s;
  }

  /** 应用撤回：移除命中 tombstone 的条目（ADR-007 硬删除的本地体现）。 */
  function applyTombstones(items, tombstones) {
    const ids = tombstones instanceof Set ? tombstones : tombstoneSet(tombstones);
    return (items || []).filter((it) => it && !ids.has(it.id));
  }

  /**
   * 可见条目筛选：先撤回应Hard删除，再剔除未生效与已过期。
   * 该函数与网络状态无关 —— STALE / OFFLINE / DEGRADED 等任意续显路径
   * 都必须经过它，从而保证 effective_until 在离线时同样被本地执行（F-3）。
   *
   * @returns {{visible:Array, removedWithdrawn:Array, removedNotYet:Array, removedExpired:Array}}
   */
  function selectVisibleItems(items, tombstones, now, opts) {
    const o = opts || {};
    const graceMs = o.expireGraceMs === undefined ? DEFAULTS.expireGraceMs : o.expireGraceMs;
    const out = { visible: [], removedWithdrawn: [], removedNotYet: [], removedExpired: [] };
    const ids = tombstones instanceof Set ? tombstones : tombstoneSet(tombstones);
    for (const it of items || []) {
      if (!it) continue;
      if (ids.has(it.id)) { out.removedWithdrawn.push(it.id); continue; }
      if (isNotYetEffective(it, now)) { out.removedNotYet.push(it.id); continue; }
      if (isExpired(it, now, graceMs)) { out.removedExpired.push(it.id); continue; }
      out.visible.push(it);
    }
    return out;
  }

  /**
   * 陈旧度分级（对齐 03 §5 的 S0–S9 语义）。
   *
   *   FRESH      age < ttl(15min)            → 03 S5 LIVE   （无标记）
   *   STALE      ttl ≤ age < 4h              → 03 S6 STALE  （去饱和 + "数据截至"）
   *   STALE_SOFT 4h ≤ age < 72h              → 03 S6 STALE + 软提示（05 §4 ②）
   *   BLOCKED    age ≥ 72h                   → 03 S8 BLOCKED（零业务内容）
   *
   * 注：返回 BLOCKED 而非 STALE_HARD，与 03 文档的 S8 命名保持一致。
   */
  function computeStaleness(ageMs, opts) {
    const o = opts || {};
    const ttl = o.ttlMs === undefined ? DEFAULTS.ttlMs : o.ttlMs;
    const soft = o.staleSoftMs === undefined ? DEFAULTS.staleSoftMs : o.staleSoftMs;
    const hard = o.staleHardMs === undefined ? DEFAULTS.staleHardMs : o.staleHardMs;
    if (ageMs >= hard) return 'BLOCKED';
    if (ageMs >= soft) return 'STALE_SOFT';
    if (ageMs >= ttl) return 'STALE';
    return 'FRESH';
  }

  /**
   * content_hash 载荷（03 §4 / §S3）。
   *
   * 契约：content_hash = "sha256:" + hex(sha256(canonicalize({items, tombstones})))
   * 该函数只负责产出**待哈希的规范化字符串**，实际哈希由调用方完成
   * （浏览器用 Web Crypto，Node 用 node:crypto），从而保持本模块纯同步。
   */
  function contentHashPayload(bundle) {
    const items = (bundle && bundle.items) || [];
    const tombstones = (bundle && bundle.tombstones) || [];
    return canonicalize({ items: items, tombstones: tombstones });
  }

  /**
   * F-2 判定：从本地缓存恢复 last-good 时是否可以续显。
   * 只有「验签过的历史 Bundle」才允许续显；未验证内容一律拒绝。
   */
  function canRenderFromCache(cached) {
    if (!cached || typeof cached !== 'object') return false;
    if (!cached.verified) return false;              // 必须曾经验签通过
    if (!Array.isArray(cached.bundle && cached.bundle.items)) return false;
    return true;
  }

  return {
    DEFAULTS,
    canonicalize,
    ts,
    isExpired,
    isNotYetEffective,
    expireWarn,
    tombstoneSet,
    mergeTombstoneIds,
    normalizeIds,
    applyTombstones,
    selectVisibleItems,
    computeStaleness,
    contentHashPayload,
    canRenderFromCache
  };
});