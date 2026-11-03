/*
 * The parent app.
 *
 * No framework and no build step: the whole thing is served from a Go binary on a
 * Raspberry Pi that should keep working untouched for years, and a dependency tree is
 * the part most likely to rot. ~600 lines of plain DOM is cheaper to keep alive.
 *
 * Two things here are worth reading closely:
 *   - the reauth dance in apiWrite(), which is what turns "no passwords, Face ID only"
 *     into something that does not prompt on every single tap;
 *   - totpCode(), the third implementation of RFC 6238 in this repo. It must agree with
 *     core-rules/Totp.kt and server/internal/totp, or a parent reads a code off this
 *     screen and the television rejects it.
 */
'use strict';

const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text !== undefined) node.textContent = text;
  return node;
};

const DAYS = ['MONDAY', 'TUESDAY', 'WEDNESDAY', 'THURSDAY', 'FRIDAY', 'SATURDAY', 'SUNDAY'];
const DAY_LABELS = ['一', '二', '三', '四', '五', '六', '日'];
const SECRET_KEY = 'bg.escapeSecret';

let state = null;    // last /api/parent/state response
let draft = null;    // policy being edited
let apps = [];       // apps the TVs have reported
let pollTimer = null;
let codeTimer = null;

/* ------------------------------------------------------------------ helpers */

function toast(message, bad = false) {
  const node = $('toast');
  node.textContent = message;
  node.classList.toggle('bad', bad);
  node.classList.add('show');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => node.classList.remove('show'), 2600);
}

function duration(seconds) {
  if (!seconds || seconds < 60) return '0 分';
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return hours ? `${hours} 小时 ${minutes} 分` : `${minutes} 分`;
}

function clockOf(millis) {
  return new Date(millis).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
}

function sinceText(millis) {
  const seconds = Math.max(0, Math.round((Date.now() - millis) / 1000));
  if (seconds < 60) return '刚刚';
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时前`;
  return `${Math.floor(seconds / 86400)} 天前`;
}

/* --------------------------------------------------------------- api client */

async function api(method, path, body) {
  const response = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (response.status === 204) return null;
  const text = await response.text();
  const data = text ? JSON.parse(text) : null;
  if (!response.ok) {
    throw Object.assign(new Error(data?.detail || data?.error || `HTTP ${response.status}`), {
      code: data?.error,
      status: response.status,
    });
  }
  return data;
}

/*
 * Every write goes through here. The server accepts a write only if Face ID happened in
 * the last few minutes; when it has not, it answers `reauth_required` rather than
 * failing, we run the passkey prompt and replay the request. That is what gives the
 * behaviour the family asked for: glancing at the dashboard is free, but changing
 * anything — or unlocking the TV — costs one Face ID.
 */
async function apiWrite(method, path, body) {
  try {
    return await api(method, path, body);
  } catch (error) {
    if (error.code !== 'reauth_required') throw error;
    await login();
    return api(method, path, body);
  }
}

/* ----------------------------------------------------------------- webauthn */

function b64urlToBuf(value) {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/');
  const binary = atob(padded + '='.repeat((4 - (padded.length % 4)) % 4));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes.buffer;
}

function bufToB64url(buffer) {
  const bytes = new Uint8Array(buffer);
  let binary = '';
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

async function register(name, setupCode) {
  const options = await api('POST', '/api/parent/register/begin', { name, setupCode });
  const publicKey = options.publicKey;
  publicKey.challenge = b64urlToBuf(publicKey.challenge);
  publicKey.user.id = b64urlToBuf(publicKey.user.id);
  (publicKey.excludeCredentials || []).forEach((c) => { c.id = b64urlToBuf(c.id); });

  const credential = await navigator.credentials.create({ publicKey });
  await api('POST', '/api/parent/register/finish', {
    id: credential.id,
    rawId: bufToB64url(credential.rawId),
    type: credential.type,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      attestationObject: bufToB64url(credential.response.attestationObject),
      clientDataJSON: bufToB64url(credential.response.clientDataJSON),
      transports: credential.response.getTransports ? credential.response.getTransports() : [],
    },
  });
}

async function login() {
  const options = await api('POST', '/api/parent/login/begin', {});
  const publicKey = options.publicKey;
  publicKey.challenge = b64urlToBuf(publicKey.challenge);
  (publicKey.allowCredentials || []).forEach((c) => { c.id = b64urlToBuf(c.id); });

  const credential = await navigator.credentials.get({ publicKey, mediation: 'optional' });
  await api('POST', '/api/parent/login/finish', {
    id: credential.id,
    rawId: bufToB64url(credential.rawId),
    type: credential.type,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      authenticatorData: bufToB64url(credential.response.authenticatorData),
      clientDataJSON: bufToB64url(credential.response.clientDataJSON),
      signature: bufToB64url(credential.response.signature),
      userHandle: credential.response.userHandle ? bufToB64url(credential.response.userHandle) : '',
    },
  });
}

/*
 * Offline, there is no server to verify an assertion against, so this only proves the
 * person in front of the phone passed Face ID just now. That is the right bar for
 * revealing a code that is itself only good for 30 seconds — and the phone's own lock
 * screen is the real boundary either way.
 */
async function localFaceId() {
  try {
    await navigator.credentials.get({
      publicKey: {
        challenge: crypto.getRandomValues(new Uint8Array(32)),
        rpId: location.hostname,
        userVerification: 'required',
        timeout: 60000,
      },
    });
    return true;
  } catch {
    return false;
  }
}

/* --------------------------------------------------------------------- totp */

const BASE32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

function base32Decode(text) {
  const clean = text.toUpperCase().replace(/[^A-Z2-7]/g, '');
  const out = [];
  let bits = 0;
  let value = 0;
  for (const ch of clean) {
    value = (value << 5) | BASE32.indexOf(ch);
    bits += 5;
    if (bits >= 8) {
      out.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  return new Uint8Array(out);
}

async function totpCode(secretBase32, timeMillis, step = 30, digits = 6) {
  const key = await crypto.subtle.importKey(
    'raw', base32Decode(secretBase32), { name: 'HMAC', hash: 'SHA-1' }, false, ['sign'],
  );
  const counter = Math.floor(Math.floor(timeMillis / 1000) / step);
  const message = new ArrayBuffer(8);
  const view = new DataView(message);
  view.setUint32(0, Math.floor(counter / 4294967296));
  view.setUint32(4, counter >>> 0);

  const signature = new Uint8Array(await crypto.subtle.sign('HMAC', key, message));
  const offset = signature[signature.length - 1] & 0x0f;
  const binary = ((signature[offset] & 0x7f) << 24)
    | (signature[offset + 1] << 16)
    | (signature[offset + 2] << 8)
    | signature[offset + 3];
  return String(binary % 10 ** digits).padStart(digits, '0');
}

/* ------------------------------------------------------------------ actions */

async function refresh() {
  try {
    state = await api('GET', '/api/parent/state');
    render();
    setConnected(true);
  } catch (error) {
    if (error.code === 'signin_required') {
      showGate();
      return;
    }
    setConnected(false);
  }
}

function setConnected(online) {
  const node = $('conn-state');
  if (!online) {
    node.textContent = '连不上 Pi — 用下面的应急码';
    node.className = 'small warn';
    return;
  }
  const devices = state?.devices || [];
  const live = devices.filter((d) => d.online).length;
  node.textContent = devices.length
    ? `${live}/${devices.length} 台电视在线`
    : '还没有配对电视';
  node.className = 'muted small';
}

async function unlock(minutes, packages) {
  try {
    await apiWrite('POST', '/api/parent/grants', { minutes, packages: packages || null });
    toast(`已解锁 ${minutes} 分钟`);
    await refresh();
  } catch (error) {
    toast(error.message, true);
  }
}

async function setEnforcement(enabled) {
  try {
    state = await apiWrite('POST', '/api/parent/enforcement', { enabled });
    render();
  } catch (error) {
    toast(error.message, true);
    $('enforce-toggle').checked = !enabled;
  }
}

async function savePolicy() {
  try {
    state = await apiWrite('PUT', '/api/parent/policy', draft);
    draft = structuredClone(state.policy);
    toast('规则已保存');
    render();
  } catch (error) {
    toast(error.message, true);
  }
}

async function showEscapeCode() {
  let secret = localStorage.getItem(SECRET_KEY);
  let online = true;
  try {
    const fetched = await apiWrite('GET', '/api/parent/escape-secret');
    secret = fetched.escapeSecretBase32;
    localStorage.setItem(SECRET_KEY, secret);
  } catch {
    online = false;
    // Offline: the secret cached from a previous online session is all we have, so
    // gate it on Face ID locally before putting it on screen.
    if (!secret) { toast('还没缓存过应急密钥,需要先联网打开一次', true); return; }
    if (!await localFaceId()) { toast('Face ID 没通过', true); return; }
  }

  const panel = $('code-panel');
  panel.replaceChildren();
  const code = el('div', 'code', '······');
  const timer = el('div', 'code-timer');
  const bar = el('div');
  timer.append(bar);
  const note = el('p', 'muted small',
    online ? '用遥控器输进电视。每 30 秒换一个。'
           : '离线模式:用手机本地时间算的,电视时钟差太多可能不认。');
  panel.append(code, timer, note);

  clearInterval(codeTimer);
  const tick = async () => {
    const now = Date.now();
    code.textContent = await totpCode(secret, now);
    const remaining = 30 - Math.floor((now / 1000) % 30);
    bar.style.width = `${(remaining / 30) * 100}%`;
  };
  await tick();
  codeTimer = setInterval(tick, 1000);
}

async function pairDevice() {
  try {
    const { code, expiresAtMillis } = await apiWrite('POST', '/api/parent/pairing-code');
    const box = $('pairing-code');
    box.hidden = false;
    box.replaceChildren(
      el('div', 'code', code),
      el('p', 'muted small',
        `在电视上打开「电视守护」,地址填 ${location.host},配对码输这个。${clockOf(expiresAtMillis)} 前有效。`),
    );
  } catch (error) {
    toast(error.message, true);
  }
}

/* ---------------------------------------------------------------- rendering */

function render() {
  if (!state) return;
  if (!draft) draft = structuredClone(state.policy);

  $('gate').hidden = true;
  $('main').hidden = false;

  $('enforce-toggle').checked = !!state.policy.enforcementEnabled;
  $('today-total').textContent = duration(state.todaySeconds);
  $('week-total').textContent = duration(state.weekSeconds);

  renderGrants();
  renderBlocks();
  renderTopApps();
  renderRules();
  renderDevices();
  setConnected(true);
}

function renderGrants() {
  const box = $('grants');
  box.replaceChildren();
  const grants = (state.grants || []).filter((g) => g.expiresAtMillis > Date.now());
  for (const grant of grants) {
    const row = el('div', 'item');
    const scope = grant.packages?.length ? grant.packages.length + ' 个 app' : '全部';
    row.append(el('div', null, `${scope} · 到 ${clockOf(grant.expiresAtMillis)}`));
    const cancel = el('button', 'danger', '取消');
    cancel.onclick = async () => {
      try {
        await apiWrite('DELETE', `/api/parent/grants/${grant.id}`);
        await refresh();
      } catch (error) { toast(error.message, true); }
    };
    row.append(cancel);
    box.append(row);
  }
}

function renderBlocks() {
  const blocks = state.recentBlocks || [];
  $('blocks-card').hidden = blocks.length === 0;
  const box = $('blocks');
  box.replaceChildren();
  for (const block of blocks.slice(0, 5)) {
    const row = el('div', 'item');
    const left = el('div');
    left.append(el('div', null, block.appLabel || block.packageName));
    left.append(el('div', 'meta', `${reasonText(block.reason)} · ${sinceText(block.atMillis)}`));
    const allow = el('button', 'ghost', '放行 30 分钟');
    allow.onclick = () => unlock(30, [block.packageName]);
    row.append(left, allow);
    box.append(row);
  }
}

function reasonText(reason) {
  return {
    NOT_IN_ALLOWLIST: '不在白名单',
    IN_BLOCKLIST: '在黑名单',
    CURFEW: '禁用时段',
    APP_BUDGET_EXHAUSTED: '该 app 限额用完',
    DAILY_BUDGET_EXHAUSTED: '今日限额用完',
    WEEKLY_BUDGET_EXHAUSTED: '本周限额用完',
  }[reason] || reason;
}

function renderTopApps() {
  const box = $('top-apps');
  box.replaceChildren();
  const top = state.topApps || [];
  const max = Math.max(1, ...top.map((a) => a.seconds));
  for (const app of top.slice(0, 6)) {
    const row = el('div', 'bar-row');
    row.append(el('span', null, app.label || app.packageName));
    row.append(el('span', 'muted small', duration(app.seconds)));
    const track = el('div', 'bar-track');
    const fill = el('div', 'bar-fill');
    fill.style.width = `${(app.seconds / max) * 100}%`;
    track.append(fill);
    row.append(track);
    box.append(row);
  }
}

async function renderDailyChart() {
  try {
    const usage = await api('GET', '/api/parent/usage?days=14');
    const box = $('daily-chart');
    box.replaceChildren();
    const daily = usage.daily || [];
    const max = Math.max(1, ...daily.map((d) => d.seconds));
    const today = new Date().toISOString().slice(0, 10);
    for (const day of daily) {
      const bar = el('div', day.day === today ? 'today' : null);
      bar.style.height = `${(day.seconds / max) * 100}%`;
      bar.title = `${day.day} · ${duration(day.seconds)}`;
      box.append(bar);
    }
  } catch { /* the chart is the least important thing here */ }
}

function renderDevices() {
  const box = $('devices');
  box.replaceChildren();
  for (const device of state.devices || []) {
    const row = el('div', 'item');
    const left = el('div');
    left.append(el('div', null, device.name));
    left.append(el('div', 'meta',
      `${device.model || ''} Android ${device.androidRelease || '?'} · ${sinceText(device.lastSeenMillis)}`));
    const right = el('div', 'row');
    right.append(el('span', `pill ${device.online ? 'on' : 'off'}`, device.online ? '在线' : '离线'));
    const remove = el('button', 'danger', '移除');
    remove.onclick = async () => {
      try {
        await apiWrite('DELETE', `/api/parent/devices/${device.id}`);
        await refresh();
      } catch (error) { toast(error.message, true); }
    };
    right.append(remove);
    row.append(left, right);
    box.append(row);
  }
}

/* ------------------------------------------------------------- rule editing */

function renderRules() {
  for (const button of $('list-mode').children) {
    button.classList.toggle('selected', button.dataset.mode === draft.listMode);
  }
  for (const button of $('offline-mode').children) {
    button.classList.toggle('selected', button.dataset.mode === draft.offlineBehavior);
  }
  $('grace-field').hidden = draft.offlineBehavior !== 'OPEN_AFTER_GRACE';
  $('grace-minutes').value = draft.offlineGraceMinutes ?? 60;
  $('weekly-minutes').value = draft.weeklyMinutes ?? '';

  renderDailyLimits();
  renderAppLists();
  renderCurfews();
}

function renderDailyLimits() {
  const box = $('daily-limits');
  box.replaceChildren();
  DAYS.forEach((day, index) => {
    const label = el('label', null, `周${DAY_LABELS[index]}`);
    const input = el('input');
    input.type = 'number';
    input.min = '0';
    input.step = '15';
    input.inputMode = 'numeric';
    input.placeholder = '不限';
    input.value = draft.dailyMinutes?.[day] ?? '';
    input.oninput = () => {
      draft.dailyMinutes = draft.dailyMinutes || {};
      if (input.value === '') delete draft.dailyMinutes[day];
      else draft.dailyMinutes[day] = Math.max(0, parseInt(input.value, 10) || 0);
    };
    box.append(label, input);
  });
}

function renderAppLists() {
  const listBox = $('app-list');
  const limitBox = $('per-app-limits');
  listBox.replaceChildren();
  limitBox.replaceChildren();

  if (!apps.length) {
    listBox.append(el('p', 'muted small', '电视上报过用量之后,这里会列出可选的 app。'));
    return;
  }

  const field = draft.listMode === 'ALLOWLIST' ? 'allowlist'
    : draft.listMode === 'BLOCKLIST' ? 'blocklist' : null;

  for (const app of apps) {
    if (field) {
      const row = el('div', 'item');
      row.append(el('div', null, app.label || app.packageName));
      const selected = (draft[field] || []).includes(app.packageName);
      const toggle = el('button', selected ? 'selected' : null,
        selected ? (field === 'allowlist' ? '允许' : '禁用') : '—');
      toggle.onclick = () => {
        draft[field] = draft[field] || [];
        const at = draft[field].indexOf(app.packageName);
        if (at >= 0) draft[field].splice(at, 1);
        else draft[field].push(app.packageName);
        renderAppLists();
      };
      row.append(toggle);
      listBox.append(row);
    }

    const limitRow = el('div', 'item');
    limitRow.append(el('div', null, app.label || app.packageName));
    const input = el('input');
    input.type = 'number';
    input.min = '0';
    input.step = '15';
    input.inputMode = 'numeric';
    input.placeholder = '不限';
    input.style.maxWidth = '110px';
    input.value = draft.perAppDailyMinutes?.[app.packageName] ?? '';
    input.oninput = () => {
      draft.perAppDailyMinutes = draft.perAppDailyMinutes || {};
      if (input.value === '') delete draft.perAppDailyMinutes[app.packageName];
      else draft.perAppDailyMinutes[app.packageName] = Math.max(0, parseInt(input.value, 10) || 0);
    };
    limitRow.append(input);
    limitBox.append(limitRow);
  }
}

function renderCurfews() {
  const box = $('curfews');
  box.replaceChildren();
  draft.curfews = draft.curfews || [];

  draft.curfews.forEach((curfew, index) => {
    const wrap = el('div', 'curfew');

    const name = el('input');
    name.type = 'text';
    name.placeholder = '名字,比如「作业时间」';
    name.value = curfew.label || '';
    name.oninput = () => { curfew.label = name.value; };

    const times = el('div', 'times');
    const start = el('input');
    start.type = 'time';
    start.value = curfew.start;
    start.onchange = () => { curfew.start = start.value; };
    const end = el('input');
    end.type = 'time';
    end.value = curfew.end;
    end.onchange = () => { curfew.end = end.value; };
    times.append(start, el('span', 'muted', '到'), end);

    const picker = el('div', 'daypicker');
    DAYS.forEach((day, dayIndex) => {
      const on = (curfew.days || []).includes(day);
      const button = el('button', on ? 'selected' : null, DAY_LABELS[dayIndex]);
      button.onclick = () => {
        curfew.days = curfew.days || [];
        const at = curfew.days.indexOf(day);
        if (at >= 0) curfew.days.splice(at, 1);
        else curfew.days.push(day);
        renderCurfews();
      };
      picker.append(button);
    });

    // The midnight-crossing rule trips people up, so say it rather than document it.
    const hint = el('p', 'muted small',
      curfew.end <= curfew.start
        ? '跨零点:选中的是「开始」那天,结束在第二天。'
        : '');

    const remove = el('button', 'danger', '删掉这个时段');
    remove.onclick = () => { draft.curfews.splice(index, 1); renderCurfews(); };

    wrap.append(name, times, picker, hint, remove);
    box.append(wrap);
  });
}

/* -------------------------------------------------------------------- gates */

function showGate() {
  $('main').hidden = true;
  $('gate').hidden = false;
  clearInterval(pollTimer);
}

async function boot() {
  const bootstrap = await api('GET', '/api/parent/bootstrap').catch(() => null);
  if (!bootstrap) {
    $('gate').hidden = false;
    $('gate-error').hidden = false;
    $('gate-error').textContent = '连不上服务端。确认 Pi 开着,而且地址对。';
    return;
  }

  if (!window.PublicKeyCredential) {
    $('gate').hidden = false;
    $('gate-error').hidden = false;
    $('gate-error').textContent =
      '这个浏览器用不了通行密钥。要用 HTTPS 打开,而且证书要被信任 — 见 docs/TLS.md。';
    return;
  }

  if (bootstrap.signedIn) {
    await refresh();
    await renderDailyChart();
    loadApps();
    startPolling();
    return;
  }

  $('gate').hidden = false;
  $('gate-setup').hidden = !bootstrap.needsSetup;
  $('gate-login').hidden = bootstrap.needsSetup;
  $('gate-sub').textContent = bootstrap.needsSetup ? '第一次使用,先注册一个通行密钥' : '用 Face ID 登录';
}

async function loadApps() {
  try {
    const response = await api('GET', '/api/parent/apps');
    apps = response.apps || [];
    if (draft) renderAppLists();
  } catch { /* the rule editor degrades to "no apps known yet" */ }
}

function startPolling() {
  clearInterval(pollTimer);
  pollTimer = setInterval(() => {
    if (document.visibilityState === 'visible') refresh();
  }, 15000);
}

async function afterSignIn() {
  $('gate-error').hidden = true;
  await refresh();
  await renderDailyChart();
  loadApps();
  startPolling();
}

function gateError(error) {
  $('gate-error').hidden = false;
  $('gate-error').textContent = error.name === 'NotAllowedError'
    ? 'Face ID 没通过,或者被取消了。'
    : error.message;
}

/* ------------------------------------------------------------------ wire up */

$('btn-register').onclick = async () => {
  try {
    await register($('setup-name').value.trim() || '家长', $('setup-code').value.trim());
    await afterSignIn();
  } catch (error) { gateError(error); }
};

$('btn-register-more').onclick = async () => {
  try {
    await login();                       // an existing parent authorises the new phone
    await register('家长', '');
    await afterSignIn();
  } catch (error) { gateError(error); }
};

$('btn-login').onclick = async () => {
  try {
    await login();
    await afterSignIn();
  } catch (error) { gateError(error); }
};

$('btn-logout').onclick = async () => {
  await api('POST', '/api/parent/logout', {}).catch(() => {});
  location.reload();
};

$('enforce-toggle').onchange = (event) => setEnforcement(event.target.checked);
$('btn-show-code').onclick = showEscapeCode;
$('btn-pair').onclick = pairDevice;
$('btn-save-policy').onclick = savePolicy;

document.querySelectorAll('.unlock').forEach((button) => {
  button.onclick = () => unlock(parseInt(button.dataset.minutes, 10));
});

$('list-mode').onclick = (event) => {
  const mode = event.target.dataset?.mode;
  if (!mode) return;
  draft.listMode = mode;
  renderRules();
};

$('offline-mode').onclick = (event) => {
  const mode = event.target.dataset?.mode;
  if (!mode) return;
  draft.offlineBehavior = mode;
  renderRules();
};

$('grace-minutes').oninput = (event) => {
  draft.offlineGraceMinutes = Math.max(1, parseInt(event.target.value, 10) || 60);
};

$('weekly-minutes').oninput = (event) => {
  draft.weeklyMinutes = event.target.value === ''
    ? null
    : Math.max(0, parseInt(event.target.value, 10) || 0);
};

$('btn-add-curfew').onclick = () => {
  draft.curfews = draft.curfews || [];
  draft.curfews.push({
    id: '', label: '就寝时间', days: ['MONDAY', 'TUESDAY', 'WEDNESDAY', 'THURSDAY', 'SUNDAY'],
    start: '21:30', end: '06:30',
  });
  renderCurfews();
};

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && state) refresh();
});

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('sw.js').catch(() => {});
}

boot();
