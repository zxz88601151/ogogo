'use strict';

/**
 * admin-withdraw —— 服务端唯一写入方工具（演示撤回=硬删+tombstone）
 *
 * 用法: node admin-withdraw.js <itemId> [reason]
 * 注意：这是服务端管理工具，不是终端 API。终端永远只读（405）。
 * 修改内容源文件后，server.js 通过 fs.watch 自动重建并重新签名 Bundle。
 */

const fs = require('fs');
const path = require('path');
const { withdraw } = require('./lib/bundle');

const SOURCE_PATH = path.join(__dirname, 'data', 'content-source.json');
const itemId = process.argv[2];
const reason = process.argv[3] || 'withdrawn';

if (!itemId) {
  console.error('用法: node admin-withdraw.js <itemId> [reason]');
  process.exit(2);
}

const raw = fs.readFileSync(SOURCE_PATH, 'utf8');
const source = JSON.parse(raw);
const ok = withdraw(source, itemId, reason);
if (!ok) {
  console.error(`未找到内容项: ${itemId}`);
  process.exit(1);
}
fs.writeFileSync(SOURCE_PATH, JSON.stringify(source, null, 2), 'utf8');
console.log(`已撤回 ${itemId}（硬删 + tombstone）。server 将自动重建 Bundle。`);
