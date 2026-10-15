'use strict';

/**
 * privacylint —— 数据防火墙（对齐设计文档 02 / ADR-016 / ADR-017）
 *
 * 职责：
 *  1. 黑名单硬拒绝：任何内容项出现 PII / 敏感字段即整包拒绝（BLOCK）。
 *  2. 白名单严格校验：内容项对象的键必须是白名单子集，未知键一律拒绝。
 *  3. 仅校验"内容项"对象；Bundle 顶层固定结构由 bundle.js 控制。
 *
 * 设计约束（不可绕过）：
 *  - 不创建任何绕过白名单的例外。
 *  - 教师默认只下发 surname 级别或匿名标识（本 MVP 不下发学生实名）。
 *  - 走廊屏不新增考试展示字段（is_exam 标记在黑名单内）。
 */

// 内容项允许出现的字段（白名单）。与设计 02 一致。
const ITEM_WHITELIST = new Set([
  'id',          // 内容唯一 ID（非学生 ID）
  'type',        // announcement | homework | schedule | event | utility | weather
  'title',       // 标题
  'summary',     // 摘要（作业/公告正文仅摘要，≤24 字）
  'audience',    // 受众范围：{ grade?, class? } 匿名聚合，不含个人
  'location',    // 位置（走廊屏 events/utility 用，公共区域）
  'priority',    // 展示优先级 1-3
  'published_at',// 发布时间 ISO8601
  'effective_from',
  'effective_until',
  'updated_at',
  'state'        // 生命周期标注（DRAFT/PUBLISHED/UPDATING/WITHDRAWN/EXPIRED/ERROR）
]);

// 嵌套对象允许的键（容器键 -> 允许的子键）。audience 仅允许匿名聚合范围。
const NESTED_WHITELIST = {
  audience: new Set(['grade', 'class'])
};

// 黑名单：命中即整包拒绝（PII / 成绩 / 考勤 / 联系方式 / 教师备注 / 考试）
const ITEM_BLACKLIST = new Set([
  'student_id', 'student_full_name', 'student_name', 'name',
  'grade_score', 'score', 'exam', 'exam_score', 'grades', 'rank',
  'attendance', 'attend', 'phone', 'mobile', 'tel', 'parent', 'parent_contact',
  'teacher_notes', 'note', 'remark', 'address', 'birth', 'birthday',
  'id_card', 'face', 'photo', 'avatar', 'is_exam'
]);

// 递归扫描对象，返回违规键数组（空数组表示通过）
// parentKey 用于识别嵌套容器（如 audience）以应用其专属白名单。
function scanObject(node, path, parentKey) {
  const violations = [];
  if (Array.isArray(node)) {
    node.forEach((child, i) => {
      violations.push(...scanObject(child, `${path}[${i}]`, parentKey));
    });
    return violations;
  }
  if (node && typeof node === 'object') {
    const subWhitelist = parentKey && NESTED_WHITELIST[parentKey]
      ? NESTED_WHITELIST[parentKey]
      : ITEM_WHITELIST;
    for (const key of Object.keys(node)) {
      const here = path ? `${path}.${key}` : key;
      if (ITEM_BLACKLIST.has(key)) {
        violations.push(`BLACKLIST:${here}`);
        // 命中黑名单仍继续扫描，报告全部违规
      } else if (!subWhitelist.has(key)) {
        violations.push(`UNKNOWN:${here}`);
      }
      violations.push(...scanObject(node[key], here, key));
    }
  }
  return violations;
}

/**
 * 校验一个内容项对象。
 * @returns {{ok:boolean, violations:string[]}}
 */
function lintItem(item) {
  const violations = scanObject(item, 'item');
  return { ok: violations.length === 0, violations };
}

/**
 * 校验整包内容项数组（items）。
 * @returns {{ok:boolean, violations:string[], checked:number}}
 */
function lintItems(items) {
  if (!Array.isArray(items)) {
    return { ok: false, violations: ['ITEMS_NOT_ARRAY'], checked: 0 };
  }
  let all = [];
  items.forEach((it, i) => {
    const r = lintItem(it);
    all.push(...r.violations.map((v) => `#${i} ${v}`));
  });
  return { ok: all.length === 0, violations: all, checked: items.length };
}

module.exports = { ITEM_WHITELIST, ITEM_BLACKLIST, lintItem, lintItems, scanObject };
