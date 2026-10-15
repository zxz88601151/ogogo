'use strict';

/**
 * EduDesk 教师内容后台（G-1）
 *
 * 对齐设计：08 教师内容生产链路（服务端唯一写入方）、G-4 内容运营制度、
 *          ADR-016（唯一写入方）、ADR-017（privacylint 前置）、ADR-018（教师最高权限）。
 *
 * 前端不做任何权限判定与内容安全判定 —— 仅负责呈现服务端返回的校验结果，
 * 所有拒绝以服务端为准（前端校验仅为体验优化，绝不作为安全边界）。
 */

const API = '/admin/api';
const TOKEN_KEY = 'edudesk.admin.token';
const SESSION_KEY = 'edudesk.admin.session';

const state = {
  token: localStorage.getItem(TOKEN_KEY) || '',
  session: JSON.parse(localStorage.getItem(SESSION_KEY) || 'null'),
  items: [],
  tombstones: [],
  meta: null,
  editingId: null,
  withdrawingId: null
};

const $ = (id) => document.getElementById(id);

// ---------------- 基础工具 ----------------
async function api(path, opts = {}) {
  const headers = Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {});
  if (state.token) headers['Authorization'] = 'Bearer ' + state.token;
  const resp = await fetch(API + path, Object.assign({}, opts, { headers }));
  let body = null;
  try { body = await resp.json(); } catch (e) { /* 无 body */ }
  if (!resp.ok) {
    if (resp.status === 401) { logout(); }
    const err = new Error((body && body.error) || ('请求失败（HTTP ' + resp.status + '）'));
    err.status = resp.status;
    err.body = body;
    throw err;
  }
  return body;
}

let toastTimer = null;
function toast(msg, isErr) {
  const el = $('toast');
  el.textContent = msg;
  el.className = 'toast' + (isErr ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add('hidden'), 3200);
}

function fmtTime(s) {
  if (!s) return '—';
  const d = new Date(s);
  if (isNaN(d.getTime())) return s;
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}
// datetime-local 需要本地时区的 "YYYY-MM-DDTHH:mm"
function toLocalInput(s) {
  if (!s) return '';
  const d = new Date(s);
  if (isNaN(d.getTime())) return '';
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}
function fromLocalInput(v) {
  if (!v) return '';
  const d = new Date(v);
  return isNaN(d.getTime()) ? '' : d.toISOString();
}

const TYPE_LABEL = {
  announcement: '公告', homework: '作业', schedule: '课表',
  event: '活动', utility: '服务', weather: '天气'
};

function escapeHtml(s) {
  return String(s == null ? '' : s).replace(/[&<>"']/g, (c) =>
    ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

// ---------------- 登录（TP-02）----------------
async function doLogin() {
  const username = $('username').value.trim();
  const password = $('password').value;
  const errBox = $('loginError');
  errBox.classList.add('hidden');
  if (!username || !password) {
    errBox.textContent = '请输入账号与密码';
    errBox.classList.remove('hidden');
    return;
  }
  try {
    const res = await api('/login', { method: 'POST', body: JSON.stringify({ username, password }) });
    state.token = res.token;
    state.session = res.session;
    localStorage.setItem(TOKEN_KEY, state.token);
    localStorage.setItem(SESSION_KEY, JSON.stringify(state.session));
    await enterApp();
  } catch (e) {
    errBox.textContent = e.message;
    errBox.classList.remove('hidden');
  }
}

function logout() {
  state.token = '';
  state.session = null;
  state.items = [];
  localStorage.removeItem(TOKEN_KEY);
  localStorage.removeItem(SESSION_KEY);
  $('appView').classList.add('hidden');
  $('loginView').classList.remove('hidden');
}

// ---------------- 主流程 ----------------
async function enterApp() {
  $('loginView').classList.add('hidden');
  $('appView').classList.remove('hidden');
  $('whoami').innerHTML = `教师 <b>${escapeHtml(state.session.display_name || state.session.username)}</b>　·　${escapeHtml(state.session.username)}`;
  await Promise.all([loadMeta(), loadItems(), loadAudit()]);
}

async function loadMeta() {
  try { state.meta = await api('/meta'); } catch (e) { /* 已处理 */ }
  const sel = $('fType');
  if (state.meta && sel) {
    sel.innerHTML = (state.meta.classroom_types || [])
      .map((t) => `<option value="${t}">${TYPE_LABEL[t] || t}（${t}）</option>`).join('');
  }
  updateLimits();
}

function updateLimits() {
  if (!state.meta) return;
  const L = state.meta.limits || {};
  $('titleCount').textContent = `${$('fTitle').value.length}/${L.title || 40}`;
  $('summaryCount').textContent = `${$('fSummary').value.length}/${L.summary || 120}`;
}

async function loadItems() {
  try {
    const res = await api('/items');
    state.items = res.items || [];
    state.tombstones = res.tombstones || [];
    renderTable();
  } catch (e) { toast(e.message, true); }
}

function renderTable() {
  const q = $('searchInput').value.trim().toLowerCase();
  const st = $('stateFilter').value;
  let rows = state.items.filter((it) => {
    if (st && it.state !== st) return false;
    if (!q) return true;
    return ((it.title || '') + ' ' + (it.id || '')).toLowerCase().includes(q);
  });
  rows = rows.slice().sort((a, b) => (a.priority || 9) - (b.priority || 9));

  const published = state.items.filter((i) => i.state === 'PUBLISHED').length;
  const drafts = state.items.filter((i) => i.state === 'DRAFT').length;
  $('stats').innerHTML = `
    <div class="stat-card"><div class="stat-num">${state.items.length}</div><div class="stat-label">内容总数</div></div>
    <div class="stat-card"><div class="stat-num">${published}</div><div class="stat-label">已发布（上屏）</div></div>
    <div class="stat-card"><div class="stat-num">${drafts}</div><div class="stat-label">草稿</div></div>
    <div class="stat-card"><div class="stat-num">${state.tombstones.length}</div><div class="stat-label">累计撤回</div></div>`;

  $('emptyHint').classList.toggle('hidden', rows.length > 0);
  $('itemRows').innerHTML = rows.map((it) => {
    const aud = it.audience ? (it.audience.class ? `${it.audience.grade || ''}${it.audience.class}班` : (it.audience.grade || '')) : '';
    return `<tr data-id="${escapeHtml(it.id)}">
      <td>
        <div class="cell-title">${escapeHtml(it.title)}</div>
        <div class="cell-sub">${escapeHtml(it.summary || '')}</div>
        <div class="cell-mono">${escapeHtml(it.id)}</div>
      </td>
      <td><span class="tag">${TYPE_LABEL[it.type] || escapeHtml(it.type)}</span></td>
      <td>${escapeHtml(aud || '—')}</td>
      <td>${it.priority || '—'}</td>
      <td class="cell-mono">${fmtTime(it.effective_from)}</td>
      <td class="cell-mono">${fmtTime(it.effective_until)}</td>
      <td><span class="tag ${it.state === 'PUBLISHED' ? 'ok' : 'warn'}">${escapeHtml(it.state)}</span></td>
      <td class="ops">
        <button class="btn link" data-act="edit">编辑</button>
        <button class="btn link danger" data-act="withdraw">撤回</button>
      </td>
    </tr>`;
  }).join('');
}

// ---------------- 编辑抽屉 ----------------
function openDrawer(item) {
  state.editingId = item ? item.id : null;
  $('drawerTitle').textContent = item ? '编辑内容' : '新建内容';
  $('violations').classList.add('hidden');
  $('fType').value = item ? item.type : (state.meta ? state.meta.classroom_types[0] : 'announcement');
  $('fTitle').value = item ? (item.title || '') : '';
  $('fSummary').value = item ? (item.summary || '') : '';
  $('fGrade').value = item && item.audience ? (item.audience.grade || 'all') : 'all';
  $('fClass').value = item && item.audience ? (item.audience.class || '') : '';
  $('fLocation').value = item ? (item.location || '') : '';
  $('fPriority').value = item && item.priority ? String(item.priority) : '2';
  $('fFrom').value = item ? toLocalInput(item.effective_from) : '';
  $('fUntil').value = item ? toLocalInput(item.effective_until) : '';
  $('fState').value = item ? item.state : 'PUBLISHED';
  $('saveBtn').textContent = item ? '保存修改' : '保存并发布';
  updateLimits();
  $('drawerMask').classList.remove('hidden');
  $('drawer').classList.remove('hidden');
  $('fTitle').focus();
}

function closeDrawer() {
  $('drawerMask').classList.add('hidden');
  $('drawer').classList.add('hidden');
  state.editingId = null;
}

async function saveItem() {
  const payload = {
    type: $('fType').value,
    title: $('fTitle').value.trim(),
    summary: $('fSummary').value.trim(),
    audience: {
      grade: $('fGrade').value,
      ...($('fClass').value.trim() ? { class: $('fClass').value.trim() } : {})
    },
    location: $('fLocation').value.trim(),
    priority: parseInt($('fPriority').value, 10),
    effective_from: fromLocalInput($('fFrom').value),
    effective_until: fromLocalInput($('fUntil').value),
    state: $('fState').value
  };
  const vbox = $('violations');
  vbox.classList.add('hidden');
  try {
    if (state.editingId) {
      await api('/items/' + encodeURIComponent(state.editingId), { method: 'PUT', body: JSON.stringify(payload) });
      toast('内容已更新');
    } else {
      await api('/items', { method: 'POST', body: JSON.stringify(payload) });
      toast('内容已发布到展示屏');
    }
    closeDrawer();
    await Promise.all([loadItems(), loadAudit()]);
  } catch (e) {
    if (e.body && e.body.violations) {
      vbox.innerHTML = '<div class="v-title">内容规范校验未通过：</div><ul>' +
        e.body.violations.map((v) => `<li>${escapeHtml(v.detail)}（${escapeHtml(v.field)}）</li>`).join('') + '</ul>';
      vbox.classList.remove('hidden');
    } else {
      toast(e.message, true);
    }
  }
}

// ---------------- 撤回（硬删除 + tombstone）----------------
function openWithdraw(id) {
  state.withdrawingId = id;
  $('wdReason').value = '';
  $('confirmMask').classList.remove('hidden');
}
function closeWithdraw() {
  $('confirmMask').classList.add('hidden');
  state.withdrawingId = null;
}

async function confirmWithdraw() {
  const id = state.withdrawingId;
  if (!id) return;
  try {
    const res = await api('/items/' + encodeURIComponent(id) + '/withdraw', {
      method: 'POST',
      body: JSON.stringify({ reason: $('wdReason').value.trim() })
    });
    toast('已撤回：' + (res.note || '内容已从所有屏幕移除'));
    closeWithdraw();
    await Promise.all([loadItems(), loadAudit()]);
  } catch (e) { toast(e.message, true); }
}

// ---------------- 审计 ----------------
async function loadAudit() {
  try {
    const res = await api('/audit?n=50');
    const list = res.entries || [];
    $('auditList').innerHTML = list.length ? list.map((e) => `
      <div class="audit-item">
        <span class="audit-time">${fmtTime(e.time)}</span>
        <span class="audit-action">${escapeHtml(e.action)}</span>
        <span class="audit-actor">${escapeHtml(e.actor || '-')} ${e.target ? '(' + escapeHtml(e.target) + ')' : ''}</span>
        <span class="audit-result ${escapeHtml(e.result)}">${escapeHtml(e.result)}</span>
        ${e.reason ? '<span class="audit-reason">' + escapeHtml(e.reason) + '</span>' : ''}
      </div>`).join('') : '<div class="empty">暂无审计记录</div>';
  } catch (e) { /* 已处理 */ }
}

// ---------------- 事件绑定 ----------------
$('loginBtn').addEventListener('click', doLogin);
$('password').addEventListener('keydown', (e) => { if (e.key === 'Enter') doLogin(); });
$('logoutBtn').addEventListener('click', logout);
$('newBtn').addEventListener('click', () => openDrawer(null));
$('searchInput').addEventListener('input', renderTable);
$('stateFilter').addEventListener('change', renderTable);
$('refreshAudit').addEventListener('click', loadAudit);
$('fTitle').addEventListener('input', updateLimits);
$('fSummary').addEventListener('input', updateLimits);
$('saveBtn').addEventListener('click', saveItem);
$('drawerClose').addEventListener('click', closeDrawer);
$('cancelBtn').addEventListener('click', closeDrawer);
$('drawerMask').addEventListener('click', closeDrawer);
$('wdConfirm').addEventListener('click', confirmWithdraw);
$('wdCancel').addEventListener('click', closeWithdraw);

$('itemRows').addEventListener('click', (e) => {
  const btn = e.target.closest('button[data-act]');
  if (!btn) return;
  const id = btn.closest('tr').dataset.id;
  const item = state.items.find((i) => i.id === id);
  if (!item) return;
  if (btn.dataset.act === 'edit') openDrawer(item);
  else openWithdraw(id);
});

// 会话恢复
if (state.token) {
  api('/session').then(enterApp).catch(() => logout());
}