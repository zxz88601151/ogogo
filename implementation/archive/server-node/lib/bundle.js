'use strict';

const { lintItems } = require('./privacylint');
const { attachSignature } = require('./sign');

/**
 * bundle —— Display Bundle 构建与生命周期（对齐设计 03 / ADR-007 撤回硬删）
 *
 * 生命周期六态（仅作内容项 state 标注，不新增正式状态机状态）：
 *   DRAFT -> PUBLISHED -> (UPDATING) -> WITHDRAWN(tombstone) | EXPIRED | ERROR
 *
 * 关键规则（继承自设计，不可更改语义）：
 *  - 服务端是唯一 Bundle 写入方（ADR-016）。
 *  - 撤回 = 硬删除：从 items 移除 + 写入 tombstones（含 withdrawn_at）。
 *    终端重连后「先清除 tombstone 命中项，再恢复展示」（pending-withdraw 语义）。
 *  - FULL-only：每次下发完整 Bundle（设计 D-05 FULL-only）。
 *  - 不下发完整学生档案/成绩/考勤/教师备注（由 privacylint 兜底）。
 */

const SCHEMA_VERSION = '1.0';
const SERVER_TZ = 'Asia/Shanghai';

// 走廊屏允许的 type；考试相关（is_exam）已在黑名单拦截，本 MVP 走廊不展示考试。
const CORRIDOR_TYPES = new Set(['announcement', 'event', 'utility', 'weather']);
const CLASSROOM_TYPES = new Set(['announcement', 'homework', 'schedule', 'weather']);

function profileTypeSet(profile) {
  return profile === 'corridor' ? CORRIDOR_TYPES : CLASSROOM_TYPES;
}

/**
 * 构建单个 profile 的 Bundle。
 * @param {object} opts
 * @param {string} opts.profile  'classroom' | 'corridor'
 * @param {Array}  opts.source   内容源（含 items / tombstones / meta）
 * @param {string} opts.secret   签名密钥
 * @returns {{ok:boolean, bundle?:object, error?:string, lint?:object}}
 */
function buildBundle({ profile, source, secret }) {
  const allowed = profileTypeSet(profile);
  const items = (source.items || []).filter((it) => {
    if (it.state && it.state === 'WITHDRAWN') return false; // 硬删除：不随包下发
    return allowed.has(it.type);
  });

  // 数据防火墙：任何违规整包拒绝
  const lint = lintItems(items);
  if (!lint.ok) {
    return { ok: false, error: 'PRIVACYLINT_BLOCKED', lint };
  }

  const tombstones = (source.tombstones || []).map((t) => ({
    id: t.id,
    withdrawn_at: t.withdrawn_at,
    reason: t.reason || 'withdrawn'
  }));

  const base = {
    schema_version: SCHEMA_VERSION,
    profile,
    generated_at: new Date().toISOString(),
    server_timezone: SERVER_TZ,
    server_epoch: Date.now(),
    lifecycle: {
      model: '6-state',
      states: ['DRAFT', 'PUBLISHED', 'UPDATING', 'WITHDRAWN', 'EXPIRED', 'ERROR'],
      note: 'WITHDRAWN=hard-delete+tombstone; terminal must purge on reconnect'
    },
    items,
    tombstones
  };

  const bundle = attachSignature(base, secret);
  return { ok: true, bundle, lint };
}

/**
 * 撤回（服务端写方工具）：硬删 item + 追加 tombstone。
 * 直接修改 source 对象（调用方负责持久化到文件）。
 * @returns {boolean} 是否成功撤回
 */
function withdraw(source, itemId, reason) {
  const idx = (source.items || []).findIndex((it) => it.id === itemId);
  if (idx === -1) return false;
  source.items.splice(idx, 1);
  source.tombstones = source.tombstones || [];
  source.tombstones.push({
    id: itemId,
    withdrawn_at: new Date().toISOString(),
    reason: reason || 'withdrawn'
  });
  return true;
}

module.exports = { buildBundle, withdraw, SCHEMA_VERSION, SERVER_TZ, CORRIDOR_TYPES, CLASSROOM_TYPES };
