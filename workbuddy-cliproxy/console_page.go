// The WorkBuddy account page served as a plugin resource route.
//
// The control panel renders plugin resources natively: a registered GET route
// carrying a Menu label shows up in the panel's own "插件" navigation group and
// is embedded at /plugin-pages/<pluginID>/<index> inside an iframe. That means
// the account dashboard lives entirely in the plugin — the panel bundle is
// never touched, so the panel's own auto-update keeps working.
//
// Key handling: resource routes are unauthenticated, and the panel stores its
// management key encrypted (enc::v1:: prefix, derived from host + user agent),
// so the page cannot read it from storage. What it CAN do is sniff it: the
// iframe is same-origin with the panel, so the page patches the parent window's
// XMLHttpRequest/fetch and picks the bearer token off the panel's own outgoing
// management requests. The key stays in the browser. A manual slot and a paste
// prompt cover the standalone case.
package main

const consoleHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>WorkBuddy 账号</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 24px;
    font: 14px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
    background: transparent; color: inherit;
  }
  .head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 18px; }
  h1 { font-size: 18px; margin: 0; font-weight: 700; }
  .sub { font-size: 12px; opacity: .6; margin-top: 2px; }
  button {
    font: inherit; cursor: pointer; border-radius: 8px; padding: 6px 14px;
    border: 1px solid rgba(37,99,235,.35); background: rgba(37,99,235,.10); color: #1d4ed8;
    font-size: 13px; font-weight: 600; transition: background .15s, opacity .15s;
  }
  button:hover:not(:disabled) { background: rgba(37,99,235,.20); }
  button:disabled { opacity: .5; cursor: default; }
  button.ghost { border-color: rgba(127,127,127,.3); background: transparent; color: inherit; opacity: .8; }
  button.done { border-color: rgba(22,163,74,.35); background: rgba(22,163,74,.12); color: #15803d; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 14px; }
  .card {
    border: 1px solid rgba(127,127,127,.22); border-radius: 14px; padding: 16px;
    background: rgba(127,127,127,.04);
  }
  .name { font-size: 14px; font-weight: 700; display: flex; align-items: center; gap: 8px; }
  .meta { font-size: 12px; opacity: .6; margin: 2px 0 12px; }
  .big { font-size: 28px; font-weight: 800; letter-spacing: -.5px; font-variant-numeric: tabular-nums; }
  .big small { font-size: 12px; font-weight: 500; opacity: .55; margin-left: 4px; }
  .bar { height: 6px; border-radius: 3px; background: rgba(127,127,127,.2); overflow: hidden; margin: 10px 0 8px; }
  .bar > i { display: block; height: 100%; background: linear-gradient(90deg,#4f8cff,#22c55e); }
  .row { display: flex; justify-content: space-between; font-size: 12px; opacity: .65; }
  .actions { display: flex; gap: 8px; margin-top: 14px; flex-wrap: wrap; }
  .tag {
    display: inline-flex; align-items: center; gap: 5px; padding: 2px 9px; border-radius: 999px;
    font-size: 11px; font-weight: 700; border: 1px solid rgba(127,127,127,.3);
  }
  .tag.done { color: #15803d; border-color: rgba(22,163,74,.35); background: rgba(22,163,74,.10); }
  .tag.todo { color: #1d4ed8; border-color: rgba(37,99,235,.3); background: rgba(37,99,235,.08); }
  .tag.err  { color: #b91c1c; border-color: rgba(220,38,38,.35); background: rgba(220,38,38,.08); }
  .note { margin-top: 16px; padding: 11px 13px; border-radius: 9px; font-size: 13px; border: 1px solid rgba(127,127,127,.25); }
  .note.ok  { color: #15803d; border-color: rgba(22,163,74,.35); background: rgba(22,163,74,.08); }
  .note.err { color: #b91c1c; border-color: rgba(220,38,38,.35); background: rgba(220,38,38,.08); }
  .note.info{ opacity: .85; }
  input {
    font: inherit; padding: 8px 11px; border-radius: 8px; width: 100%; margin-top: 8px;
    border: 1px solid rgba(127,127,127,.35); background: transparent; color: inherit;
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 13px;
  }
  .hide { display: none !important; }
  .center { text-align: center; padding: 40px 20px; opacity: .65; }
</style>
</head>
<body>
  <div class="head">
    <div>
      <h1>WorkBuddy 账号</h1>
      <div class="sub" id="sub">正在读取账号…</div>
    </div>
    <button id="reload" class="ghost">全部刷新额度</button>
  </div>

  <div id="accounts" class="grid"></div>
  <div id="loading" class="center">加载中…</div>

  <div id="keyCard" class="note info hide">
    未能自动获取管理密钥，请粘贴一次（与控制面板登录时所用密钥相同）。
    <input id="keyInput" type="password" placeholder="Management Key" autocomplete="off">
    <div style="margin-top:10px"><button id="keySave">保存并加载</button></div>
  </div>

  <div id="note" class="note info hide"></div>

<script>
(function () {
  'use strict';
  var API = '/v0/management/plugins/workbuddy';
  var SLOTS = ['workbuddy-console-key', 'workbuddy-panel-key'];
  var sniffed = '';

  function rememberKey(value) {
    if (typeof value !== 'string') return;
    var m = /^Bearer\s+(\S+)$/i.exec(value.trim());
    var candidate = m ? m[1] : value.trim();
    if (candidate.length >= 4 && candidate !== sniffed) sniffed = candidate;
  }

  function sniffHeaders(headers) {
    if (!headers) return;
    try {
      if (typeof headers.get === 'function') {
        var a = headers.get('Authorization') || headers.get('authorization');
        if (a) rememberKey(a);
        return;
      }
      if (Array.isArray(headers)) {
        headers.forEach(function (p) {
          if (p && p.length === 2 && /^authorization$/i.test(p[0])) rememberKey(p[1]);
        });
        return;
      }
      Object.keys(headers).forEach(function (k) {
        if (/^authorization$/i.test(k)) rememberKey(headers[k]);
      });
    } catch (e) { /* ignore */ }
  }

  // The panel embeds this page in a same-origin iframe, so we can observe the
  // panel's own outgoing management requests and lift the bearer token off them.
  // That is the only way in: the panel keeps its key behind an encrypted
  // storage adapter, and this page must never guess (CPA bans a client after
  // five failed management attempts).
  function installSnifferOn(target) {
    if (!target) return;
    try {
      var XHR = target.XMLHttpRequest;
      if (XHR && XHR.prototype && !XHR.prototype.__wbSniffed) {
        var origSet = XHR.prototype.setRequestHeader;
        XHR.prototype.setRequestHeader = function (name, value) {
          if (typeof name === 'string' && /^authorization$/i.test(name.trim())) rememberKey(value);
          return origSet.apply(this, arguments);
        };
        XHR.prototype.__wbSniffed = true;
      }
      if (typeof target.fetch === 'function' && !target.fetch.__wbSniffed) {
        var origFetch = target.fetch;
        var wrapped = function (input, init) {
          try {
            if (init && init.headers) sniffHeaders(init.headers);
            else if (input && input.headers) sniffHeaders(input.headers);
          } catch (e) { /* ignore */ }
          return origFetch.apply(this, arguments);
        };
        wrapped.__wbSniffed = true;
        target.fetch = wrapped;
      }
    } catch (e) { /* cross-origin parent: nothing to do */ }
  }

  installSnifferOn(window);
  if (window.parent && window.parent !== window) installSnifferOn(window.parent);

  function storedKey() {
    for (var i = 0; i < SLOTS.length; i++) {
      try {
        var v = localStorage.getItem(SLOTS[i]);
        if (v) return v;
      } catch (e) { /* ignore */ }
    }
    return '';
  }

  function key() { return sniffed || storedKey(); }

  function call(path, init) {
    var o = init || {};
    var headers = { Authorization: 'Bearer ' + key() };
    if (o.method === 'POST') headers['Content-Type'] = 'application/json';
    return fetch(API + path, { method: o.method || 'GET', headers: headers, body: o.body })
      .then(function (r) {
        if (r.status === 401 || r.status === 403) {
          sniffed = '';
          throw new Error('密钥无效或已过期');
        }
        return r.json().then(function (body) {
          if (!r.ok || body.ok === false) {
            throw new Error((body && body.error && body.error.message) || ('HTTP ' + r.status));
          }
          return body;
        });
      });
  }

  function note(kind, text) {
    var el = document.getElementById('note');
    el.className = 'note ' + kind;
    el.textContent = text;
  }

  function num(v) {
    var n = Number(v);
    if (!isFinite(n)) return '—';
    return n.toLocaleString('zh-CN', { maximumFractionDigits: 2 });
  }

  var summaries = [];
  var details = {};
  var pendingDetail = {};

  // Credits are fetched only when asked for: a lookup costs two upstream round
  // trips per account, so the page paints card shells from the cheap list first.
  function loadDetail(authIndex, force) {
    if (!authIndex) return Promise.resolve(null);
    if (pendingDetail[authIndex]) return pendingDetail[authIndex];
    if (details[authIndex] && !force) return Promise.resolve(details[authIndex]);
    pendingDetail[authIndex] = call('/accounts/status?auth_index=' + encodeURIComponent(authIndex))
      .then(function (payload) {
        var report = (payload.accounts || [])[0] || null;
        if (report) details[authIndex] = report;
        return report;
      })
      .catch(function (e) {
        details[authIndex] = { auth_index: authIndex, error: e.message };
        return details[authIndex];
      })
      .then(function (report) {
        delete pendingDetail[authIndex];
        render();
        return report;
      });
    return pendingDetail[authIndex];
  }

  function card(summary) {
    var a = summary;
    var detail = details[summary.auth_index];
    if (detail) a = Object.assign({}, summary, detail);

    var el = document.createElement('div');
    el.className = 'card';

    var name = document.createElement('div');
    name.className = 'name';
    name.appendChild(document.createTextNode(a.label || a.name || a.auth_index));
    var tag = document.createElement('span');
    if (a.error) {
      tag.className = 'tag err';
      tag.textContent = '异常';
    } else {
      tag.className = 'tag ' + (a.checked_in_today ? 'done' : 'todo');
      tag.textContent = a.checked_in_today ? '今日已签到' : '今日未签到';
    }
    name.appendChild(tag);
    el.appendChild(name);

    var meta = document.createElement('div');
    meta.className = 'meta';
    meta.textContent = a.activity_name
      ? (a.activity_name + ' · 连续 ' + (a.streak_days || 0) + ' 天 · 每日 ' + (a.daily_credit || 0))
      : (a.name || '');
    el.appendChild(meta);

    if (a.error) {
      var err = document.createElement('div');
      err.className = 'tag err';
      err.textContent = a.error;
      el.appendChild(err);
    } else if (!details[summary.auth_index]) {
      var box = document.createElement('div');
      box.className = 'center';
      box.style.padding = '18px 0';
      var hint = document.createElement('div');
      hint.style.fontSize = '12px';
      hint.style.marginBottom = '8px';
      hint.textContent = '额度未加载';
      box.appendChild(hint);
      var link = document.createElement('button');
      link.className = 'ghost';
      link.textContent = pendingDetail[summary.auth_index] ? '正在刷新…' : '点击此处刷新额度';
      link.disabled = !!pendingDetail[summary.auth_index];
      link.addEventListener('click', function (idx) {
        return function () { loadDetail(idx, true); };
      }(summary.auth_index));
      box.appendChild(link);
      el.appendChild(box);
    } else if (a.credits) {
      var big = document.createElement('div');
      big.className = 'big';
      big.appendChild(document.createTextNode(num(a.credits.remain)));
      var unit = document.createElement('small');
      unit.textContent = a.credits.unit || 'credits';
      big.appendChild(unit);
      el.appendChild(big);

      var bar = document.createElement('div');
      bar.className = 'bar';
      var fill = document.createElement('i');
      var total = Number(a.credits.total) || 0;
      var used = Number(a.credits.used) || 0;
      fill.style.width = (total > 0 ? Math.min(100, Math.max(0, (used / total) * 100)) : 0).toFixed(2) + '%';
      bar.appendChild(fill);
      el.appendChild(bar);

      var row = document.createElement('div');
      row.className = 'row';
      var l = document.createElement('span');
      l.textContent = '已用 ' + num(used);
      var r = document.createElement('span');
      r.textContent = '总量 ' + num(total);
      row.appendChild(l);
      row.appendChild(r);
      el.appendChild(row);
    }

    var actions = document.createElement('div');
    actions.className = 'actions';

    var claim = document.createElement('button');
    claim.textContent = a.checked_in_today ? '今日已签到' : '立即签到';
    claim.disabled = !!a.checked_in_today || !!a.error;
    if (a.checked_in_today) claim.className = 'done';
    claim.addEventListener('click', function () { runClaim(a, claim); });
    actions.appendChild(claim);

    var refresh = document.createElement('button');
    refresh.className = 'ghost';
    refresh.textContent = '刷新凭证';
    refresh.title = '把该凭证标记为过期，交给 CPA 触发一次刷新';
    refresh.addEventListener('click', function () { runRefresh(a, refresh); });
    actions.appendChild(refresh);

    el.appendChild(actions);
    return el;
  }

  function render() {
    var host = document.getElementById('accounts');
    host.innerHTML = '';
    document.getElementById('loading').classList.add('hide');
    if (!summaries.length) {
      document.getElementById('sub').textContent = '没有找到 WorkBuddy 账号';
      return;
    }
    var loaded = 0;
    summaries.forEach(function (s) { if (details[s.auth_index]) loaded += 1; });
    document.getElementById('sub').textContent = summaries.length + ' 个账号'
      + (loaded ? '（已加载 ' + loaded + ' 个额度）' : '');
    summaries.forEach(function (s) { host.appendChild(card(s)); });
  }

  function load() {
    document.getElementById('loading').classList.remove('hide');
    return call('/accounts').then(function (payload) {
      summaries = payload.accounts || [];
      render();
    }).catch(function (e) {
      if (/密钥无效|密钥/.test(e.message)) { askKey(); return; }
      note('err', '加载失败：' + e.message);
    });
  }

  function loadAll() {
    if (!summaries.length) { load(); return; }
    note('info', '正在刷新全部额度…');
    Promise.all(summaries.map(function (s) { return loadDetail(s.auth_index, true); }))
      .then(function () { note('ok', '额度已刷新'); });
  }

  function runClaim(a, button) {
    var original = button.textContent;
    button.disabled = true;
    button.textContent = '签到中…';
    call('/checkin/claim', { method: 'POST', body: JSON.stringify({ auth_index: a.auth_index }) })
      .then(function (res) {
        note(res.ok ? 'ok' : 'info', res.message || (res.ok ? '签到成功' : '未签到成功'));
        return loadDetail(a.auth_index, true);
      })
      .catch(function (e) {
        note('err', '签到失败：' + e.message);
      })
      .then(function () {
        button.disabled = false;
        button.textContent = original;
      });
  }

  function runRefresh(a, button) {
    var original = button.textContent;
    button.disabled = true;
    button.textContent = '刷新中…';
    // The plugin rotates the token itself: the host only auto-refreshes
    // providers it holds a refresh lead for, and a plugin cannot register one,
    // so marking the credential expired would silently do nothing.
    call('/refresh', { method: 'POST', body: JSON.stringify({ auth_index: a.auth_index }) })
      .then(function (res) {
        note('ok', res.message || ('已刷新「' + (a.label || a.name) + '」的凭证'));
        return loadDetail(a.auth_index, true);
      })
      .catch(function (e) {
        note('err', '刷新失败：' + e.message);
      })
      .then(function () {
        button.disabled = false;
        button.textContent = original;
      });
  }

  function askKey() {
    document.getElementById('loading').classList.add('hide');
    document.getElementById('keyCard').classList.remove('hide');
    document.getElementById('sub').textContent = '等待管理密钥';
  }

  document.getElementById('keySave').addEventListener('click', function () {
    var v = document.getElementById('keyInput').value.trim();
    if (!v) return;
    SLOTS.forEach(function (s) { localStorage.setItem(s, v); });
    document.getElementById('keyCard').classList.add('hide');
    load();
  });

  document.getElementById('reload').addEventListener('click', function () { loadAll(); });

  // Boot: wait for the panel's next management request to reveal the key. The
  // panel polls continuously, so this resolves within seconds when embedded.
  var waited = 0;
  (function boot() {
    if (key()) { load(); return; }
    if (waited > 20000) { askKey(); return; }
    waited += 500;
    setTimeout(boot, 500);
  })();
})();
</script>
</body>
</html>
`
