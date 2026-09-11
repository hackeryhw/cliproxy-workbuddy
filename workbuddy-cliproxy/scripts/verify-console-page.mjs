#!/usr/bin/env node
// Headless verification of the WorkBuddy account page served by the plugin.
//
// The page runs inside a same-origin iframe in the control panel and recovers
// the management key by sniffing the PARENT window's outgoing requests. This
// harness reproduces that: a tiny DOM, a fake parent window whose XHR the page
// is expected to patch, and fetch stubs that only accept the real key.
//
// Usage:
//   node scripts/verify-console-page.mjs                 # fetch from a running gateway
//   node scripts/verify-console-page.mjs <page.html>     # read a saved page (hermetic)

import vm from 'node:vm';
import { readFileSync, existsSync } from 'node:fs';

const arg = process.argv[2];
const fromFile = arg && existsSync(arg);
const BASE = fromFile ? 'http://console.invalid' : (arg || 'http://127.0.0.1:8317');
const PAGE = BASE + '/v0/resource/plugins/workbuddy/checkin';
const API = '/v0/management/plugins/workbuddy';
const PANEL_API = '/v0/management';

const failures = [];
function check(name, ok, detail) {
  if (ok) console.log(`  OK   ${name}`);
  else {
    failures.push(name);
    console.log(`  FAIL ${name}${detail ? ' — ' + detail : ''}`);
  }
}

// ---------------------------------------------------------------------------
// Fetch the page the plugin actually serves
// ---------------------------------------------------------------------------
let html;
if (fromFile) {
  html = readFileSync(arg, 'utf8');
  console.log(`page: read ${html.length} chars from ${arg}`);
} else {
  const res = await fetch(PAGE);
  html = await res.text();
  console.log(`page: HTTP ${res.status}, ${html.length} chars`);
}
const script = html.match(/<script>([\s\S]*?)<\/script>/);
if (!script) {
  console.error('no <script> block in the served page');
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Minimal DOM
// ---------------------------------------------------------------------------
class TextNode {
  constructor(t) { this.tagName = '#TEXT'; this.textContent = String(t ?? ''); this.children = []; }
  descendants() { return []; }
  getAttribute() { return null; }
}

class El {
  constructor(tag, cls = '') {
    this.tagName = tag.toUpperCase();
    this.className = cls;
    this.children = [];
    this.attrs = Object.create(null);
    this.listeners = Object.create(null);
    this.style = {};
    this.title = '';
    this.disabled = false;
  }
  get textContent() { return this._text ?? '' + this.children.map((c) => c.textContent).join(''); }
  set textContent(v) { this._text = String(v ?? ''); this.children = []; }
  set innerHTML(v) { if (v === '') this.children = []; }
  get innerHTML() { return this.children.map((c) => c.textContent).join(''); }
  appendChild(c) { this.children.push(c); c.parent = this; return c; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  addEventListener(e, fn) { (this.listeners[e] ||= []).push(fn); }
  click() { (this.listeners.click || []).forEach((fn) => fn()); }
  descendants() { const o = []; const w = (n) => { for (const c of n.children) { o.push(c); w(c); } }; w(this); return o; }
  querySelectorAll(s) { return queryAll(this, s); }
  querySelector(s) { return queryAll(this, s)[0] || null; }
  classList = { _s: new Set(), add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); }, contains(c) { return this._s.has(c); } };
}

function matchesSimple(el, sel) {
  if (sel.startsWith('.')) {
    return String(el.className || '').split(/\s+/).includes(sel.slice(1));
  }
  const a = sel.match(/^\[([A-Za-z-]+)([*^$]?)(=)"?([^"\]]*)"?\]$/);
  if (a) {
    const [, name, op, , value] = a;
    const actual = name === 'class' ? el.className : el.getAttribute(name);
    if (actual == null) return false;
    return op === '*' ? String(actual).includes(value) : String(actual) === value;
  }
  return el.tagName.toLowerCase() === sel.toLowerCase();
}
function queryAll(root, selector) {
  const groups = String(selector).split(',').map((s) => s.trim()).filter(Boolean);
  const out = [];
  for (const el of root.descendants()) {
    for (const g of groups) if (matchesSimple(el, g)) { out.push(el); break; }
  }
  return out;
}

// ---------------------------------------------------------------------------
// Fixture: mirrors the ids the page writes into
// ---------------------------------------------------------------------------
const ids = ['sub', 'accounts', 'loading', 'keyCard', 'note', 'keyInput', 'keySave', 'reload'];
const nodes = new Map(ids.map((id) => [id, new El('div', '')]));
nodes.get('keyCard').classList.add('hide');
nodes.get('note').classList.add('hide');
const body = new El('body');
ids.forEach((id) => body.appendChild(nodes.get(id)));

const document = {
  readyState: 'complete',
  head: new El('head'),
  body,
  getElementById: (id) => nodes.get(id) || null,
  createElement: (t) => new El(t),
  createTextNode: (t) => new TextNode(t),
  addEventListener: () => {},
  querySelector: (s) => queryAll(body, s)[0] || null,
  querySelectorAll: (s) => queryAll(body, s),
};

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------
const PANEL_KEY = 'panel-secret-abc123';
class XHRStub {
  setRequestHeader(name, value) { (this._h ||= {})[name] = value; }
  open() {}
  send() {}
}

const requests = [];
let claimedIndex = null;
let refreshedIndex = null;

const fetchStub = (url, init = {}) => {
  const path = String(url);
  const headers = init.headers || {};
  requests.push({ path, method: init.method || 'GET', authorization: headers.Authorization || '', body: init.body });
  const json = (b, s = 200) => Promise.resolve({
    ok: s < 400, status: s, json: () => Promise.resolve(b), text: () => Promise.resolve(JSON.stringify(b)),
  });

  if (path.includes('/plugins/workbuddy/') && headers.Authorization !== 'Bearer ' + PANEL_KEY) {
    return json({ error: { message: 'invalid management key' } }, 401);
  }
  // Mirrors the real API: /accounts is cheap (names only), the credits live
  // behind /accounts/status and cost two upstream round trips per account.
  if (path.includes('/plugins/workbuddy/accounts/status')) {
    return json({ ok: true, accounts: [{
      auth_index: 'idx-1', name: 'workbuddy.json', label: '56657220',
      checked_in_today: false, streak_days: 2, daily_credit: 100, activity_name: '开学季',
      credits: { total: 2100, remain: 2082.47, used: 17.53, unit: 'credits', is_paid: false },
    }] });
  }
  if (path.endsWith('/plugins/workbuddy/accounts')) {
    return json({ ok: true, accounts: [
      { auth_index: 'idx-1', name: 'workbuddy.json', label: '56657220', uid: 'uid-1' },
    ] });
  }
  if (path.endsWith('/plugins/workbuddy/checkin/claim')) {
    claimedIndex = JSON.parse(init.body || '{}').auth_index;
    return json({ ok: true, message: '签到成功，获得 100 积分' });
  }
  if (path.endsWith('/plugins/workbuddy/refresh')) {
    refreshedIndex = JSON.parse(init.body || '{}').auth_index;
    return json({
      ok: true, account: '56657220', name: 'workbuddy.json',
      expires_at: '2026-11-10T09:13:43Z', message: '凭证已刷新，有效期至 2026-11-10 17:13',
    });
  }
  return json({ ok: false, error: { message: 'unexpected ' + path } }, 404);
};

const localStorage = (() => {
  const m = new Map();
  return {
    getItem: (k) => (m.has(k) ? m.get(k) : null),
    setItem: (k, v) => m.set(k, String(v)),
    removeItem: (k) => m.delete(k),
    get length() { return m.size; },
    key: (i) => [...m.keys()][i] ?? null,
  };
})();

// The page patches parent.XMLHttpRequest; this is the parent window.
const parentWindow = { XMLHttpRequest: XHRStub, fetch: fetchStub };

const sandbox = {
  console, JSON, Number, Math, Date, Object, Array, String, Boolean, Promise, isFinite, isNaN,
  setTimeout, clearTimeout, setInterval: () => 0,
  localStorage,
  sessionStorage: { getItem: () => null, setItem: () => {}, length: 0, key: () => null },
  fetch: fetchStub,
  document,
};
sandbox.window = sandbox;
sandbox.window.parent = parentWindow;
sandbox.globalThis = sandbox;

vm.createContext(sandbox);
try {
  vm.runInContext(script[1], sandbox, { timeout: 5000 });
} catch (err) {
  console.error('page script threw:', err && err.message);
  process.exit(1);
}

const settle = (ms = 300) => new Promise((r) => setTimeout(r, ms));
await settle();

console.log('\nbrowser-side assertions:');

// 1. It patched the parent window's transport, synchronously.
check('patches the parent XHR', XHRStub.prototype.__wbSniffed === true);

// 2. Safety: no guessing. Until the parent reveals the key, no request is sent.
check('issues no request before a key is known', requests.length === 0,
  requests.map((r) => r.method + ' ' + r.path).join(' | '));

// 3. The panel's own request hands the key over.
new XHRStub().setRequestHeader('Authorization', 'Bearer ' + PANEL_KEY);
await settle(700);

const accounts = requests.find((r) => r.path.endsWith('/plugins/workbuddy/accounts'));
check('recovers the key from the parent request',
  !!accounts && accounts.authorization === 'Bearer ' + PANEL_KEY,
  accounts ? accounts.authorization : 'no request');

// 4. Cards render immediately from the cheap list, with the balance left unloaded.
const cards = queryAll(nodes.get('accounts'), '.card');
check('renders one card per account', cards.length === 1, `${cards.length} cards`);
check('does not fetch credits on load',
  !requests.some((r) => r.path.includes('/accounts/status')),
  requests.map((r) => r.path).join(' | '));
check('card offers an explicit pull', cards[0] && /点击此处刷新额度/.test(cards[0].textContent),
  cards[0] ? cards[0].textContent.slice(0, 90) : '');

// 5. Only the click pulls the expensive data.
const loadLink = cards[0] && queryAll(cards[0], 'button').find((b) => /点击此处刷新额度/.test(b.textContent));
check('renders the load link', !!loadLink);
if (loadLink) {
  const before = requests.length;
  loadLink.click();
  await settle(300);
  const detail = requests.slice(before).find((r) => r.path.includes('/accounts/status'));
  check('clicking pulls /accounts/status', !!detail, detail ? detail.path : 'no request');
  check('the pull names the auth_index', !!detail && detail.path.includes('auth_index=idx-1'));
  await settle(200);
  const after = queryAll(nodes.get('accounts'), '.card')[0];
  check('card shows the balance after the pull',
    !!after && after.textContent.includes('2,082.47'),
    after ? after.textContent.slice(0, 90) : '');
}

// 5. Claim targets that account.
const claimBtn = queryAll(nodes.get('accounts'), 'button').find((b) => /签到/.test(b.textContent));
check('renders a check-in button', !!claimBtn);
if (claimBtn) {
  claimBtn.click();
  await settle();
  check('check-in POSTs /checkin/claim',
    requests.some((r) => r.path.endsWith('/checkin/claim') && r.method === 'POST'));
  check('check-in names the auth_index', claimedIndex === 'idx-1', String(claimedIndex));
}

// 6. Refresh rotates the credential through the plugin rather than asking the
// host to refresh a credential it has no refresh lead for.
const refreshBtn = queryAll(nodes.get('accounts'), 'button').find((b) => /刷新凭证/.test(b.textContent));
check('renders a refresh button', !!refreshBtn);
if (refreshBtn) {
  const before = requests.length;
  refreshBtn.click();
  await settle();
  const refresh = requests.slice(before).find((r) => r.path.endsWith('/plugins/workbuddy/refresh'));
  check('refresh POSTs /refresh', !!refresh && refresh.method === 'POST', refresh ? refresh.method : 'no request');
  check('refresh names the auth_index', refreshedIndex === 'idx-1', String(refreshedIndex));
  check('refresh does not fall back to the host expired marker',
    !requests.some((r) => r.path.endsWith('/auth-files/fields')));
}

console.log('');
if (failures.length) {
  console.log(`${failures.length} assertion(s) failed:`);
  failures.forEach((f) => console.log('  - ' + f));
  process.exit(1);
}
console.log('all browser-side assertions passed');
