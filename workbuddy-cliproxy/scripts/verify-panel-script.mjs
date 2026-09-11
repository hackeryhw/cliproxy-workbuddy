#!/usr/bin/env node
// Headless verification of panel/workbuddy-panel.js.
//
// Builds a miniature panel DOM using the same CSS-module class names the real
// control panel emits, runs the integration script inside node:vm, and asserts
// on the DOM it decorates and the HTTP calls it issues.
//
// Usage: node scripts/verify-panel-script.mjs

import vm from 'node:vm';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const HERE = dirname(fileURLToPath(import.meta.url));
const SCRIPT = resolve(HERE, '..', 'panel', 'workbuddy-panel.js');

const failures = [];
function check(name, ok, detail) {
  if (ok) {
    console.log(`  OK   ${name}`);
  } else {
    failures.push(name);
    console.log(`  FAIL ${name}${detail ? ' — ' + detail : ''}`);
  }
}

// ---------------------------------------------------------------------------
// Minimal DOM
// ---------------------------------------------------------------------------
// Minimal text node. It carries a tagName so the shared matcher can skip it
// instead of dereferencing undefined.
class TextNode {
  constructor(text) {
    this.tagName = '#TEXT';
    this.textContent = String(text ?? '');
    this.children = [];
  }
  descendants() { return []; }
  getAttribute() { return null; }
}

class El {
  constructor(tag, className = '') {
    this.tagName = tag.toUpperCase();
    this.className = className;
    this.children = [];
    this.attrs = Object.create(null);
    this.listeners = Object.create(null);
    this.style = {};
    this.title = '';
    this.disabled = false;
    this.type = '';
    this._text = '';
  }
  get textContent() {
    return this._text + this.children.map((c) => c.textContent).join('');
  }
  set textContent(v) {
    this._text = String(v ?? '');
    this.children = [];
  }
  appendChild(child) {
    this.children.push(child);
    child.parent = this;
    return child;
  }
  cloneNode() { return this; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; }
  addEventListener(evt, fn) { (this.listeners[evt] ||= []).push(fn); }
  click() { (this.listeners.click || []).forEach((fn) => fn()); }
  descendants() {
    const out = [];
    const walk = (n) => { for (const c of n.children) { out.push(c); walk(c); } };
    walk(this);
    return out;
  }
  querySelectorAll(selector) { return queryAll(this, selector); }
  querySelector(selector) { return queryAll(this, selector)[0] || null; }
}

// matchesSimple handles the two shapes the integration script uses:
//   [attr*="value"]   substring match (CSS-module class prefixes)
//   [attr="value"]    exact match (data attributes)
//
// The operator and the equals sign are captured separately on purpose: folding
// them into one group (as in [*^$]?=) makes the operator come back as "*=" and
// every substring selector silently stops matching.
function matchesSimple(el, sel) {
  if (sel.startsWith('.')) {
    return String(el.className || '').split(/\s+/).includes(sel.slice(1));
  }
  const attr = sel.match(/^\[([A-Za-z-]+)([*^$]?)(=)"?([^"\]]*)"?\]$/);
  if (attr) {
    const [, name, operator, , value] = attr;
    const actual = name === 'class' ? el.className : el.getAttribute(name);
    if (actual == null) return false;
    if (operator === '*') return String(actual).includes(value);
    return String(actual) === value;
  }
  return el.tagName.toLowerCase() === sel.toLowerCase();
}

function matchesCompound(el, compound) {
  // Class tokens must keep their leading dot, otherwise '.wb-grid' tokenises
  // to 'wb-grid' and gets compared as a tag name.
  const parts = compound.match(/\[[^\]]+\]|\.[A-Za-z0-9_-]+|[A-Za-z][A-Za-z0-9-]*/g) || [];
  if (!parts.length) return false;
  return parts.every((p) => matchesSimple(el, p));
}

function queryAll(root, selector) {
  const groups = String(selector).split(',').map((s) => s.trim()).filter(Boolean);
  const out = [];
  for (const el of root.descendants()) {
    for (const g of groups) {
      if (matchesCompound(el, g)) { out.push(el); break; }
    }
  }
  return out;
}

function card(name, label, provider = 'workbuddy') {
  const root = new El('div', 'AuthFileCard-module__card___a1');
  const head = new El('div', 'AuthFileCard-module__head___a2');
  const account = new El('span', 'AuthFileCard-module__account___a3');
  account.textContent = label;
  head.appendChild(account);
  root.appendChild(head);

  const file = new El('p', 'AuthFileCard-module__fileName___a4');
  file.textContent = name;
  root.appendChild(file);

  const badge = new El('span', 'AuthFileCard-module__typeBadge___a5');
  badge.textContent = provider;
  root.appendChild(badge);

  const meta = new El('div', 'AuthFileCard-module__metaRow___a6');
  const size = new El('span');
  size.textContent = '1.2 KB';
  meta.appendChild(size);
  root.appendChild(meta);

  const footer = new El('footer', 'AuthFileCard-module__actions___a7');
  const main = new El('div', 'AuthFileCard-module__actionsMain___a8');
  footer.appendChild(main);
  root.appendChild(footer);

  return root;
}

const body = new El('body');
const page = new El('div', 'AuthFilesPage-module__page___p1');
const grid = new El('div', 'AuthFilesPage-module__grid___p2');
const buddyCard = card('workbuddy.json', '56657220');
const otherCard = card('codex-team.json', 'someone@example.com', 'codex');
grid.appendChild(buddyCard);
grid.appendChild(otherCard);
page.appendChild(grid);
body.appendChild(page);

const quotaPage = new El('div', 'QuotaPage-module__page___q1');
body.appendChild(quotaPage);

const head = new El('head');
const document = {
  readyState: 'complete',
  head,
  body,
  getElementById: () => null,
  createElement: (tag) => new El(tag),
  createTextNode: (text) => new TextNode(text),
  addEventListener: () => {},
  querySelector: (s) => queryAll(body, s)[0] || null,
  querySelectorAll: (s) => queryAll(body, s),
};

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------
const PANEL_KEY = 'panel-secret-abc123';
// The panel stores its session behind an encrypted adapter, so anything read
// out of localStorage is ciphertext. The script therefore has to recover the
// key by sniffing the panel's own Authorization header.
const CIPHERTEXT = 'enc:v1:9f2c…not-the-real-key';
const localStorage = (() => {
  const map = new Map([
    ['cli-proxy-auth', JSON.stringify({ state: { managementKey: CIPHERTEXT, isAuthenticated: true } })],
  ]);
  return {
    get length() { return map.size; },
    key: (i) => [...map.keys()][i] ?? null,
    getItem: (k) => (map.has(k) ? map.get(k) : null),
    setItem: (k, v) => map.set(k, String(v)),
    removeItem: (k) => map.delete(k),
  };
})();

const requests = [];
let claimedIndex = null;
let refreshedIndex = null;

// /accounts is the cheap call: names only, no credits.
const ACCOUNT_LIST = {
  ok: true,
  accounts: [{ auth_index: 'idx-1', name: 'workbuddy.json', label: '56657220', uid: 'uid-1' }],
};

// /accounts/status is the expensive call, served only on demand.
const ACCOUNT_STATUS = {
  ok: true,
  accounts: [{
    auth_index: 'idx-1', name: 'workbuddy.json', label: '56657220', uid: 'uid-1',
    checked_in_today: false, streak_days: 2, daily_credit: 100,
    activity_name: '开学季',
    credits: { total: 2100, remain: 2082.47, used: 17.53, unit: 'credits', is_paid: false },
  }],
};

const fetchStub = (url, init = {}) => {
  const path = String(url);
  const headers = init.headers || {};
  requests.push({ path, method: init.method || 'GET', authorization: headers.Authorization || '', body: init.body });

  const json = (body, status = 200) =>
    Promise.resolve({
      ok: status < 400,
      status,
      json: () => Promise.resolve(body),
      text: () => Promise.resolve(JSON.stringify(body)),
    });

  // Everything under the plugin route needs the real management key.
  if (path.includes('/plugins/workbuddy/')) {
    if (headers.Authorization !== 'Bearer ' + PANEL_KEY) {
      return json({ error: 'invalid management key' }, 401);
    }
  }
  if (path.includes('/plugins/workbuddy/accounts/status')) return json(ACCOUNT_STATUS);
  if (path.endsWith('/plugins/workbuddy/accounts')) return json(ACCOUNT_LIST);
  if (path.endsWith('/plugins/workbuddy/checkin/claim')) {
    claimedIndex = JSON.parse(init.body || '{}').auth_index;
    return json({ ok: true, message: '签到成功', account: '56657220' });
  }
  if (path.endsWith('/plugins/workbuddy/refresh')) {
    refreshedIndex = JSON.parse(init.body || '{}').auth_index;
    return json({
      ok: true, account: '56657220', name: 'workbuddy.json',
      expires_at: '2026-11-10T09:13:43Z', message: '凭证已刷新，有效期至 2026-11-10 17:13',
    });
  }
  return json({ ok: false, error: { message: 'unexpected path ' + path } }, 404);
};

class MutationObserverStub {
  observe() {}
  disconnect() {}
}

// The panel talks to the management API through XHR (axios). This stub lets the
// harness play the panel and hand the script a real Authorization header.
class XMLHttpRequestStub {
  setRequestHeader(name, value) {
    this._headers = this._headers || {};
    this._headers[name] = value;
  }
  open() {}
  send() {}
}

const sandbox = {
  console,
  JSON, Number, Math, Date, Object, Array, String, Boolean, Promise, isFinite, isNaN,
  setTimeout, clearTimeout,
  setInterval: () => 0,
  clearInterval: () => {},
  MutationObserver: MutationObserverStub,
  XMLHttpRequest: XMLHttpRequestStub,
  localStorage,
  sessionStorage: { getItem: () => null, setItem: () => {}, length: 0, key: () => null },
  fetch: fetchStub,
  document,
};
sandbox.window = sandbox;
sandbox.globalThis = sandbox;

// ---------------------------------------------------------------------------
// Run the integration script
// ---------------------------------------------------------------------------
vm.createContext(sandbox);
try {
  vm.runInContext(readFileSync(SCRIPT, 'utf8'), sandbox, { timeout: 5000 });
} catch (err) {
  console.error('panel script threw:', err && err.message);
  process.exit(1);
}

const settle = () => new Promise((r) => setTimeout(r, 300));
await settle();

console.log('panel integration assertions:');

// 1. Safety property: with only ciphertext in storage, the script must not
//    attempt any authenticated request at all. CPA bans a client after 5
//    failed management attempts for 30 minutes, and this script polls, so a
//    speculative request would risk locking the operator out.
check('issues no request before a key is known', requests.length === 0,
  requests.map((r) => r.method + ' ' + r.path).join(' | '));

// Safety property: the sniffer must be installed synchronously at script
// evaluation, because the panel's module bundle runs before DOMContentLoaded
// and would otherwise get its first requests past an unpatched transport.
check('patches XHR synchronously', XMLHttpRequestStub.prototype.__workbuddySniffed === true);

// The panel now performs one of its own management calls, which is where the
// script picks the key up.
//
// Note: setInterval is stubbed out in this harness, and no manual reload() is
// issued, so a request appearing here can only come from the key-capture hook.
// That is the latency guarantee: cards must not wait for the next refresh tick.
const panelXhr = new XMLHttpRequestStub();
panelXhr.setRequestHeader('Authorization', 'Bearer ' + PANEL_KEY);

await settle(600);

const afterSniff = requests.find((r) => r.path.endsWith('/plugins/workbuddy/accounts'));
check('recovers the key by sniffing the panel request',
  !!afterSniff && afterSniff.authorization === 'Bearer ' + PANEL_KEY,
  afterSniff ? afterSniff.authorization : 'no request');
check('loads immediately on key capture (no tick, no manual reload)', !!afterSniff);

// 2. The workbuddy card was decorated, the codex card was left alone.
const chip = buddyCard.querySelector('[data-workbuddy="chip"]');
const checkinBtn = buddyCard.querySelector('[data-workbuddy="checkin"]');
const refreshBtn = buddyCard.querySelector('[data-workbuddy="refresh"]');
check('injects credit chip', !!chip, chip ? chip.textContent : 'missing');
check('injects check-in button', !!checkinBtn);
check('injects refresh button', !!refreshBtn);
check('unrelated provider card untouched', otherCard.querySelector('[data-workbuddy="chip"]') === null);

// 3. Latency guarantee: painting cards must NOT trigger a credit lookup.
//    A credit lookup costs two upstream round trips per account.
check('does not fetch credits on page load',
  !requests.some((r) => r.path.includes('/accounts/status')),
  requests.map((r) => r.path).join(' | '));
check('chip shows an unloaded state', !!chip && /未加载/.test(chip.textContent), chip ? chip.textContent : '');

// 4. Quota cards sit in the same page/grid as native provider cards.
const quotaCard = quotaPage.querySelector('[data-workbuddy="quota-card"]');
check('appends a quota card per account',
  quotaPage.querySelectorAll('[data-workbuddy="quota-card"]').length === 1);
check('does not wrap quota cards in a separate section',
  quotaPage.querySelector('[data-workbuddy="quota-section"]') === null);
check('quota card uses the credential file name',
  !!quotaCard && /workbuddy\.json/.test(quotaCard.textContent),
  quotaCard ? quotaCard.textContent.slice(0, 80) : '');
const loadLink = quotaCard && quotaCard.querySelectorAll('button').find((b) => /点击此处刷新额度/.test(b.textContent));
check('quota card offers 点击此处刷新额度', !!loadLink);

// 5. Only an explicit click pulls the expensive data.
if (loadLink) {
  const before = requests.length;
  loadLink.click();
  await settle(300);
  const detail = requests.slice(before).find((r) => r.path.includes('/accounts/status'));
  check('clicking the link fetches /accounts/status', !!detail, detail ? detail.path : 'no request');
  check('the click names the auth_index',
    !!detail && detail.path.includes('auth_index=idx-1'), detail ? detail.path : '');
  await settle(300);
  const refreshed = quotaPage.querySelector('[data-workbuddy="quota-card"]');
  check('quota card shows the balance after the click',
    !!refreshed && refreshed.textContent.includes('2,082.47'),
    refreshed ? refreshed.textContent.slice(0, 80) : '');
}

// 6. Claim targets that account.
checkinBtn.click();
await settle();

check('check-in issues POST /checkin/claim',
  requests.some((r) => r.path.endsWith('/plugins/workbuddy/checkin/claim') && r.method === 'POST'));
check('check-in names the account auth_index', claimedIndex === 'idx-1', String(claimedIndex));

// 7. Refresh goes through the plugin, which rotates the token itself.
const beforeRefresh = requests.length;
refreshBtn.click();
await settle();

const refreshReq = requests.slice(beforeRefresh).find((r) => r.path.endsWith('/plugins/workbuddy/refresh'));
check('refresh issues POST /refresh', !!refreshReq && refreshReq.method === 'POST');
check('refresh names the account auth_index', refreshedIndex === 'idx-1', String(refreshedIndex));
check('refresh reports the new expiry',
  refreshBtn.title.includes('2026-11-10'), refreshBtn.title);
check('refresh does not fall back to the host expired marker',
  !requests.some((r) => r.path.endsWith('/auth-files/fields')));

console.log('');
if (failures.length) {
  console.log(`${failures.length} assertion(s) failed:`);
  failures.forEach((f) => console.log('  - ' + f));
  process.exit(1);
}
console.log('all panel integration assertions passed');
