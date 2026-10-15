'use strict';

const crypto = require('crypto');

/**
 * sign —— Display Bundle 整体签名（对齐设计 04 / ADR-xxx 双签名默认项 D-09）
 *
 * 默认算法：HMAC-SHA256（D-09 默认值；Ed25519 为后期可选项，不阻塞 MVP）。
 * 规范：对「去除 signature 字段后的 Bundle」做键排序规范化 JSON，
 *       再计算 HMAC，结果 base64 编码写入 signature。
 *
 * 终端必须验签通过才渲染（S3 VERIFYING → S4 VALID）。
 */

function canonicalize(obj) {
  if (obj === null || typeof obj !== 'object') {
    return JSON.stringify(obj);
  }
  if (Array.isArray(obj)) {
    return '[' + obj.map(canonicalize).join(',') + ']';
  }
  const keys = Object.keys(obj).sort();
  const parts = keys.map((k) => {
    if (k === 'signature') return null; // 排除签名自身
    return JSON.stringify(k) + ':' + canonicalize(obj[k]);
  }).filter((p) => p !== null);
  return '{' + parts.join(',') + '}';
}

function signBundle(bundle, secret) {
  const payload = canonicalize(bundle);
  return crypto.createHmac('sha256', secret).update(payload, 'utf8').digest('base64');
}

/**
 * 在 bundle 上附加签名（返回新对象，不修改入参）。
 */
function attachSignature(bundle, secret) {
  const { signature, ...rest } = bundle;
  const sig = signBundle(rest, secret);
  return { ...rest, signature: sig, sign_alg: 'HMAC-SHA256' };
}

/**
 * 验签。返回 { ok, reason }。
 */
function verifyBundle(bundle, secret) {
  if (!bundle || typeof bundle !== 'object') {
    return { ok: false, reason: 'BUNDLE_MISSING' };
  }
  const provided = bundle.signature;
  if (!provided) {
    return { ok: false, reason: 'SIGNATURE_MISSING' };
  }
  // 验签时排除 signature 与 sign_alg（二者均为签发后附加，不计入签名载荷）
  const { signature, sign_alg, ...rest } = bundle;
  const expected = signBundle(rest, secret);
  const a = Buffer.from(provided);
  const b = Buffer.from(expected);
  if (a.length !== b.length) {
    return { ok: false, reason: 'SIGNATURE_MISMATCH' };
  }
  if (!crypto.timingSafeEqual(a, b)) {
    return { ok: false, reason: 'SIGNATURE_MISMATCH' };
  }
  return { ok: true, reason: 'OK' };
}

module.exports = { canonicalize, signBundle, attachSignature, verifyBundle };
