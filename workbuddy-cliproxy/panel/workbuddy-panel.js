/* WorkBuddy panel integration for the CLIProxyAPI control panel.
 *
 * Appended to management.html as a standalone script block, so it never touches
 * the panel's minified bundle. It decorates whatever the panel renders:
 *
 *   - Auth Files page: every WorkBuddy credential card gains a 签到 button that
 *     checks in THAT account and a 刷新凭证 button, plus a credit chip.
 *   - Quota Management page: one card per account in the native quota grid,
 *     using the same idle / loaded / action-pill shape as Antigravity/Codex.
 *
 * Credits are NEVER fetched on page load. A credit lookup costs two upstream
 * round trips per account, so cards are painted immediately from the cheap
 * account list and the operator pulls the numbers with an explicit click —
 * the same lazy shape the panel's own quota cards use.
 */
(function () {
  'use strict';

  var API = '/v0/management/plugins/workbuddy';
  var PANEL_API = '/v0/management';
  var KEY_SLOTS = ['workbuddy-panel-key', 'workbuddy-console-key'];
  var MARK = 'data-workbuddy';

  // ---------------------------------------------------------------------------
  // Management key
  //
  // The panel keeps its session behind an encrypted storage adapter, so reading
  // localStorage yields ciphertext. Sniffing the header off the panel's own
  // management requests is both simpler and immune to whatever the storage
  // layer does. It is also the ONLY safe option: CPA bans a client for 30
  // minutes after 5 failed management attempts, so guessing is not an option.
  // ---------------------------------------------------------------------------
  var sniffedKey = '';

  function rememberKey(value) {
    if (typeof value !== 'string') return;
    var match = /^Bearer\s+(\S+)$/i.exec(value.trim());
    var candidate = match ? match[1] : value.trim();
    if (candidate.length >= 4 && candidate !== sniffedKey) {
      sniffedKey = candidate;
      onKeyReady();
    }
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
    } catch (e) { /* ignore malformed headers */ }
  }

  function installKeySniffer() {
    try {
      var XHR = window.XMLHttpRequest;
      if (XHR && XHR.prototype && !XHR.prototype.__workbuddySniffed) {
        var originalSetHeader = XHR.prototype.setRequestHeader;
        XHR.prototype.setRequestHeader = function (name, value) {
          if (typeof name === 'string' && /^authorization$/i.test(name.trim())) rememberKey(value);
          return originalSetHeader.apply(this, arguments);
        };
        XHR.prototype.__workbuddySniffed = true;
      }
    } catch (e) { /* ignore */ }

    try {
      if (typeof window.fetch === 'function' && !window.fetch.__workbuddySniffed) {
        var originalFetch = window.fetch;
        var wrapped = function (input, init) {
          try {
            if (init && init.headers) sniffHeaders(init.headers);
            else if (input && input.headers) sniffHeaders(input.headers);
          } catch (e) { /* ignore */ }
          return originalFetch.apply(this, arguments);
        };
        wrapped.__workbuddySniffed = true;
        window.fetch = wrapped;
      }
    } catch (e) { /* ignore */ }
  }

  function key() {
    if (sniffedKey) return sniffedKey;
    for (var i = 0; i < KEY_SLOTS.length; i++) {
      try {
        var value = localStorage.getItem(KEY_SLOTS[i]);
        if (value) return value;
      } catch (e) { /* storage unavailable */ }
    }
    return '';
  }

  function call(path, init) {
    var options = init || {};
    var headers = { Authorization: 'Bearer ' + key() };
    if (options.method === 'POST') headers['Content-Type'] = 'application/json';
    return fetch(API + path, {
      method: options.method || 'GET',
      headers: headers,
      body: options.body
    }).then(function (r) {
      if (r.status === 401 || r.status === 403) {
        sniffedKey = '';
        throw new Error('management key unavailable');
      }
      return r.json().then(function (body) {
        if (!r.ok || body.ok === false) {
          var detail = (body && body.error && body.error.message) || ('HTTP ' + r.status);
          throw new Error(detail);
        }
        return body;
      });
    });
  }

  // ---------------------------------------------------------------------------
  // State
  // ---------------------------------------------------------------------------
  var summaries = null;   // cheap: [{auth_index, name, label}] from /accounts
  var details = {};       // auth_index -> report from /accounts/status
  var pending = {};       // auth_index -> true while a detail fetch is in flight
  var loadingList = null;
  var listeners = [];

  function notify() {
    listeners.forEach(function (fn) {
      try { fn(); } catch (e) { /* ignore listener errors */ }
    });
  }

  // loadSummaries is the cheap call: no upstream work, so it is safe to run on
  // every page render.
  function loadSummaries(force) {
    if (loadingList) return loadingList;
    if (summaries && !force) return Promise.resolve(summaries);
    if (!key()) return Promise.resolve(null);
    loadingList = call('/accounts')
      .then(function (payload) {
        summaries = payload.accounts || [];
        return summaries;
      })
      .catch(function (err) {
        summaries = [];
        if (window.console) console.warn('[workbuddy] account list failed:', err.message);
        return summaries;
      })
      .then(function (list) {
        loadingList = null;
        notify();
        return list;
      });
    return loadingList;
  }

  // loadDetail is the expensive call, and it only ever runs because an operator
  // clicked. Results are cached per account until something invalidates them.
  function loadDetail(authIndex, force) {
    if (!authIndex) return Promise.resolve(null);
    if (pending[authIndex]) return pending[authIndex];
    if (details[authIndex] && !force) return Promise.resolve(details[authIndex]);
    if (!key()) return Promise.resolve(null);
    var request = call('/accounts/status?auth_index=' + encodeURIComponent(authIndex))
      .then(function (payload) {
        var report = (payload.accounts || [])[0] || null;
        if (report) details[authIndex] = report;
        return report;
      })
      .catch(function (err) {
        details[authIndex] = { auth_index: authIndex, error: err.message };
        return details[authIndex];
      })
      .then(function (report) {
        delete pending[authIndex];
        notify();
        return report;
      });
    pending[authIndex] = request;
    notify();
    return request;
  }

  function detailOf(authIndex) { return details[authIndex] || null; }

  function summaryFor(text) {
    if (!summaries) return null;
    var needle = text || '';
    for (var i = 0; i < summaries.length; i++) {
      var s = summaries[i];
      if (s.name && needle.indexOf(s.name) >= 0) return s;
      if (s.label && s.label.length >= 3 && needle.indexOf(s.label) >= 0) return s;
    }
    return null;
  }

  // ---------------------------------------------------------------------------
  // Styling
  // ---------------------------------------------------------------------------
  function installStyles() {
    if (document.getElementById('workbuddy-panel-style')) return;
    var style = document.createElement('style');
    style.id = 'workbuddy-panel-style';
    style.textContent = [
      '.wb-chip{display:inline-flex;align-items:center;gap:5px;padding:2px 8px;border-radius:999px;',
      'font-size:11px;line-height:18px;font-weight:600;white-space:nowrap;',
      'background:rgba(37,99,235,.10);color:#1d4ed8;border:1px solid rgba(37,99,235,.25)}',
      '.wb-chip[data-tone="done"]{background:rgba(22,163,74,.10);color:#15803d;border-color:rgba(22,163,74,.28)}',
      '.wb-chip[data-tone="warn"]{background:rgba(217,119,6,.12);color:#b45309;border-color:rgba(217,119,6,.3)}',
      '.wb-chip[data-tone="error"]{background:rgba(220,38,38,.10);color:#b91c1c;border-color:rgba(220,38,38,.28)}',
      '.wb-chip[data-tone="idle"]{background:transparent;color:inherit;opacity:.7;border-style:dashed}',
      '.wb-btn{display:inline-flex;align-items:center;gap:5px;padding:4px 11px;border-radius:8px;',
      'font-size:12px;font-weight:600;cursor:pointer;border:1px solid rgba(37,99,235,.35);',
      'background:rgba(37,99,235,.08);color:#1d4ed8;transition:background .15s,opacity .15s}',
      '.wb-btn:hover:not(:disabled){background:rgba(37,99,235,.18)}',
      '.wb-btn:disabled{opacity:.55;cursor:default}',
      '.wb-btn[data-state="done"]{border-color:rgba(22,163,74,.35);background:rgba(22,163,74,.10);color:#15803d}',
      '.wb-btn[data-state="busy"]{border-color:rgba(107,114,128,.35);background:rgba(107,114,128,.10);color:#4b5563}',
      '.wb-actions{display:inline-flex;align-items:center;gap:6px;margin-left:6px}',
      '.wb-dot{width:6px;height:6px;border-radius:50%;background:currentColor;opacity:.75}',
      '.wb-qc-grid{grid-template-columns:repeat(auto-fill,minmax(min(100%,340px),1fr));align-items:stretch;gap:16px;display:grid}',
      '.wb-qc-card{border:1px solid var(--border-color,rgba(127,127,127,.25));',
      'background:color-mix(in srgb,var(--bg-primary,transparent) 82%,transparent);',
      'transition:transform .2s ease-out,border-color .2s ease-out,box-shadow .2s ease-out;',
      'border-radius:14px;flex-direction:column;gap:11px;padding:15px 16px;display:flex;position:relative}',
      '@media (hover:hover) and (pointer:fine){.wb-qc-card:hover{border-color:var(--border-hover,rgba(127,127,127,.45));',
      'transform:translateY(-2px);box-shadow:0 12px 26px -14px #00000029}}',
      '.wb-qc-head{align-items:center;gap:9px;min-width:0;display:flex}',
      '.wb-qc-icon{background:color-mix(in srgb,var(--bg-tertiary,rgba(127,127,127,.2)) 60%,transparent);',
      'border-radius:8px;flex-shrink:0;justify-content:center;align-items:center;width:26px;height:26px;display:flex;',
      'color:var(--text-secondary,inherit);font-size:11px;font-weight:700}',
      '.wb-qc-file{letter-spacing:.01em;min-width:0;color:var(--text-primary,inherit);text-overflow:ellipsis;',
      'white-space:nowrap;font-family:ui-monospace,SF Mono,Menlo,Consolas,monospace;font-size:12.5px;font-weight:600;overflow:hidden}',
      '.wb-qc-body{flex-direction:column;gap:9px;min-width:0;display:flex}',
      '.wb-qc-idle{cursor:pointer;border:1px dashed var(--border-color,rgba(127,127,127,.35));width:100%;min-height:76px;',
      'color:var(--text-tertiary,inherit);background:0 0;border-radius:10px;flex-direction:column;justify-content:center;',
      'align-items:center;gap:6px;padding:12px 10px;display:flex;font:inherit}',
      '.wb-qc-idle:disabled{cursor:not-allowed;opacity:.55}',
      '.wb-qc-idle:active:not(:disabled){transform:scale(.98)}',
      '.wb-qc-idle:focus-visible{outline:2px solid var(--primary-color,#2563eb);outline-offset:2px}',
      '@media (hover:hover) and (pointer:fine){.wb-qc-idle:hover:not(:disabled){',
      'border-color:color-mix(in srgb,var(--primary-color,#2563eb) 35%,var(--border-color,rgba(127,127,127,.35)));',
      'color:var(--text-secondary,inherit);border-style:solid}}',
      '.wb-qc-idleHint{text-align:center;font-family:ui-monospace,SF Mono,Menlo,Consolas,monospace;font-size:11.5px;line-height:1.5}',
      '.wb-qc-error{color:var(--viz-failure,#ef4444);background:color-mix(in srgb,var(--viz-failure,#ef4444) 8%,transparent);',
      'border:1px solid color-mix(in srgb,var(--viz-failure,#ef4444) 32%,transparent);overflow-wrap:anywhere;',
      'border-radius:9px;padding:8px 10px;font-size:11.5px;line-height:1.5}',
      '.wb-qc-row{flex-direction:column;gap:5px;min-width:0;display:flex}',
      '.wb-qc-rowHead{justify-content:space-between;align-items:baseline;gap:8px;min-width:0;display:flex}',
      '.wb-qc-model{min-width:0;color:var(--text-secondary,inherit);text-overflow:ellipsis;white-space:nowrap;',
      'font-size:12.5px;font-weight:600;overflow:hidden}',
      '.wb-qc-meta{font-variant-numeric:tabular-nums;letter-spacing:.01em;flex-shrink:0;align-items:baseline;gap:7px;',
      'font-family:ui-monospace,SF Mono,Menlo,Consolas,monospace;font-size:11.5px;display:inline-flex}',
      '.wb-qc-percent{color:var(--text-primary,inherit);font-weight:650}',
      '.wb-qc-amount{font-variant-numeric:tabular-nums;color:var(--text-secondary,inherit)}',
      '.wb-qc-reset{color:var(--text-quaternary,inherit)}',
      '.wb-qc-bar{background:color-mix(in srgb,var(--text-primary,#111) 8%,transparent);border-radius:9999px;height:6px;overflow:hidden}',
      '.wb-qc-fill{height:100%;border-radius:9999px;transition:width .36s ease}',
      '.wb-qc-fill.is-high{background:var(--viz-success,#22c55e)}',
      '.wb-qc-fill.is-mid{background:var(--quota-medium-color,#eab308)}',
      '.wb-qc-fill.is-low{background:var(--viz-failure,#ef4444)}',
      '.wb-qc-actions{flex-wrap:wrap;justify-content:flex-end;gap:6px;display:flex}',
      '.wb-qc-pill{cursor:pointer;border:1px solid var(--border-color,rgba(127,127,127,.35));color:var(--text-secondary,inherit);',
      'white-space:nowrap;background:0 0;border-radius:9999px;align-items:center;gap:5px;padding:5px 11px;',
      'font-family:inherit;font-size:11.5px;font-weight:600;line-height:1.4;display:inline-flex}',
      '.wb-qc-pill:disabled{cursor:not-allowed;opacity:.5}',
      '.wb-qc-pill:active:not(:disabled){transform:scale(.97)}',
      '.wb-qc-pill:focus-visible{outline:2px solid var(--primary-color,#2563eb);outline-offset:2px}',
      '@media (hover:hover) and (pointer:fine){.wb-qc-pill:hover:not(:disabled){border-color:var(--border-hover,rgba(127,127,127,.5));',
      'background:color-mix(in srgb,var(--bg-tertiary,rgba(127,127,127,.12)) 55%,transparent);color:var(--text-primary,inherit)}}',
      '.wb-qc-skel{flex-direction:column;gap:9px;display:flex}',
      '.wb-qc-skelRow{flex-direction:column;gap:5px;display:flex}',
      '.wb-qc-skelLabel{height:12px;width:38%;border-radius:4px;background:color-mix(in srgb,var(--text-primary,#111) 8%,transparent)}',
      '.wb-qc-skelTrack{height:6px;border-radius:9999px;background:color-mix(in srgb,var(--text-primary,#111) 8%,transparent)}',
      '@media (prefers-color-scheme: dark){',
      '.wb-chip{background:rgba(96,165,250,.14);color:#93c5fd;border-color:rgba(96,165,250,.3)}',
      '.wb-btn{background:rgba(96,165,250,.14);color:#93c5fd;border-color:rgba(96,165,250,.3)}',
      '}'
    ].join('');
    document.head.appendChild(style);
  }

  // ---------------------------------------------------------------------------
  // Auth Files page
  // ---------------------------------------------------------------------------
  var CARD = '[class*="AuthFileCard-module__card"]';
  var ACTIONS = '[class*="AuthFileCard-module__actionsMain"]';
  var META = '[class*="AuthFileCard-module__metaRow"]';

  function decorateAuthCards() {
    if (!summaries) return;
    var cards = document.querySelectorAll(CARD);
    for (var i = 0; i < cards.length; i++) {
      var card = cards[i];
      if (card.getAttribute(MARK)) continue;
      var summary = summaryFor(card.textContent || '');
      if (!summary) continue;
      card.setAttribute(MARK, 'card');

      var chip = document.createElement('span');
      chip.className = 'wb-chip';
      chip.setAttribute(MARK, 'chip');
      var meta = card.querySelector(META);
      (meta || card).appendChild(chip);

      var refresh = document.createElement('button');
      refresh.type = 'button';
      refresh.className = 'wb-btn';
      refresh.setAttribute(MARK, 'refresh');
      refresh.textContent = '刷新凭证';
      refresh.title = '把该凭证标记为过期，交给 CPA 触发一次刷新';
      refresh.addEventListener('click', function (s, n) {
        return function () { runRefresh(s, n); };
      }(summary, refresh));

      var claim = document.createElement('button');
      claim.type = 'button';
      claim.className = 'wb-btn';
      claim.title = '为「' + (summary.label || summary.name) + '」领取每日积分';
      claim.setAttribute(MARK, 'checkin');
      claim.addEventListener('click', function (s, n) {
        return function () { runClaim(s, n); };
      }(summary, claim));

      var group = document.createElement('div');
      group.className = 'wb-actions';
      group.appendChild(refresh);
      group.appendChild(claim);
      (card.querySelector(ACTIONS) || card).appendChild(group);

      paintAuthCard(summary, chip, claim);
    }
  }

  function paintAuthCard(summary, chip, claim) {
    var detail = detailOf(summary.auth_index);
    if (!detail) {
      // Nothing has been asked for yet: offer the same explicit pull the quota
      // page uses instead of a spinner.
      chip.textContent = '额度未加载';
      chip.setAttribute('data-tone', 'idle');
      chip.title = '点击加载该账号的积分余额';
      chip.style.cursor = 'pointer';
      chip.onclick = function () { loadDetail(summary.auth_index).then(null, function () {}); };
      claim.textContent = '签到';
      claim.disabled = false;
      claim.setAttribute('data-state', 'idle');
      return;
    }
    chip.onclick = null;
    chip.style.cursor = '';
    if (detail.error) {
      chip.textContent = '加载失败，点击重试';
      chip.setAttribute('data-tone', 'error');
      chip.title = detail.error;
      chip.style.cursor = 'pointer';
      chip.onclick = function () { loadDetail(summary.auth_index, true); };
    } else {
      var credits = detail.credits;
      chip.textContent = credits
        ? ('剩余 ' + fmt(credits.remain) + ' ' + (credits.unit || 'credits'))
        : 'WorkBuddy';
      chip.setAttribute('data-tone', detail.checked_in_today ? 'done' : 'info');
      chip.title = detail.checked_in_today
        ? ('今日已签到 · 连续 ' + detail.streak_days + ' 天')
        : ('每日 ' + detail.daily_credit + ' 积分可领');
    }
    claim.textContent = detail.checked_in_today ? '今日已签到' : '签到';
    claim.disabled = !!detail.checked_in_today;
    claim.setAttribute('data-state', detail.checked_in_today ? 'done' : 'idle');
  }

  function runClaim(summary, button) {
    if (button.getAttribute('data-busy') === '1') return;
    button.setAttribute('data-busy', '1');
    button.disabled = true;
    button.textContent = '签到中…';
    call('/checkin/claim', { method: 'POST', body: JSON.stringify({ auth_index: summary.auth_index }) })
      .then(function () { return loadDetail(summary.auth_index, true); })
      .catch(function (err) {
        button.textContent = '重试签到';
        button.title = err.message;
        button.disabled = false;
      })
      .then(function () { button.setAttribute('data-busy', '0'); });
  }

  function runTasksAuto(summary, button) {
    if (button.getAttribute('data-busy') === '1') return;
    var original = button.textContent;
    button.setAttribute('data-busy', '1');
    button.disabled = true;
    button.textContent = '任务中…';
    call('/tasks/auto_all', { method: 'POST', body: JSON.stringify({ auth_index: summary.auth_index }) })
      .then(function (payload) {
        button.title = (payload && payload.message) || '成长任务已执行';
        return loadDetail(summary.auth_index, true);
      })
      .catch(function (err) {
        button.textContent = '重试任务';
        button.title = err.message;
        button.disabled = false;
      })
      .then(function () { button.setAttribute('data-busy', '0'); });
  }

  function runRefresh(summary, button) {
    if (button.getAttribute('data-busy') === '1') return;
    var original = button.textContent;
    button.setAttribute('data-busy', '1');
    button.disabled = true;
    button.textContent = '刷新中…';
    // The plugin rotates the token itself instead of marking the credential
    // expired for the host to pick up: the host only auto-refreshes providers
    // it holds a refresh lead for, and a plugin cannot register one, so the
    // marker route would report success while nothing ever happened.
    call('/refresh', {
      method: 'POST',
      body: JSON.stringify({ auth_index: summary.auth_index })
    })
      .then(function (payload) {
        button.textContent = '已刷新';
        button.title = (payload && payload.message) || '凭证已刷新';
        button.setAttribute('data-state', 'done');
      })
      .catch(function (err) {
        button.textContent = '刷新失败';
        button.title = err.message;
      })
      .then(function () {
        button.setAttribute('data-busy', '0');
        window.setTimeout(function () {
          button.textContent = original;
          button.setAttribute('data-state', 'idle');
          button.disabled = false;
        }, 2500);
      });
  }

  // ---------------------------------------------------------------------------
  // Quota Management page
  //
  // WorkBuddy is not a native quota provider, so the panel never renders a
  // card for it. We drop one article per account into the same grid the
  // panel uses for Antigravity/Codex, and mirror that card's idle / loaded /
  // action-pill structure so the page reads as one list.
  // ---------------------------------------------------------------------------
  function moduleClass(fragment) {
    var needle = fragment + '___';
    var nodes = document.querySelectorAll('[class*="' + needle + '"]');
    for (var i = 0; i < nodes.length; i++) {
      if (nodes[i].getAttribute(MARK)) continue;
      var parts = String(nodes[i].className || '').split(/\s+/);
      for (var j = 0; j < parts.length; j++) {
        if (parts[j].indexOf(needle) >= 0) return parts[j];
      }
    }
    return '';
  }

  function skin(fragment, fallback) {
    return moduleClass(fragment) || fallback;
  }

  function refreshGlyph(size) {
    if (typeof document.createElementNS !== 'function') {
      var mark = document.createElement('span');
      mark.textContent = '↻';
      mark.setAttribute('aria-hidden', 'true');
      return mark;
    }
    var ns = 'http://www.w3.org/2000/svg';
    var svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('viewBox', '0 0 24 24');
    svg.setAttribute('width', String(size || 15));
    svg.setAttribute('height', String(size || 15));
    svg.setAttribute('fill', 'none');
    svg.setAttribute('stroke', 'currentColor');
    svg.setAttribute('stroke-width', '2');
    svg.setAttribute('stroke-linecap', 'round');
    svg.setAttribute('stroke-linejoin', 'round');
    svg.setAttribute('aria-hidden', 'true');
    var path = document.createElementNS(ns, 'path');
    path.setAttribute('d', 'M21 12a9 9 0 1 1-3.2-6.8');
    var poly = document.createElementNS(ns, 'polyline');
    poly.setAttribute('points', '21 3 21 9 15 9');
    svg.appendChild(path);
    svg.appendChild(poly);
    return svg;
  }

  function quotaFilterAllowsWorkbuddy() {
    var active = document.querySelector('[class*="ProviderTabs-module__tabActive"]')
      || document.querySelector('[class*="ProviderTabs-module__tab"][aria-pressed="true"]');
    if (!active) return true;
    var text = active.textContent || '';
    return /全部/.test(text) || /WorkBuddy/i.test(text);
  }

  function findQuotaHost() {
    var grid = document.querySelector('[class*="QuotaPage-module__grid"]');
    if (grid) return grid;
    var workbench = document.querySelector('[class*="QuotaPage-module__workbench"]');
    if (workbench) {
      var existing = workbench.querySelector('[data-workbuddy="quota-grid"]');
      if (existing) return existing;
      var created = document.createElement('div');
      created.className = skin('QuotaPage-module__grid', 'wb-qc-grid');
      created.setAttribute(MARK, 'quota-grid');
      workbench.appendChild(created);
      return created;
    }
    return document.querySelector('[class*="QuotaPage-module__page"]');
  }

  function removeQuotaCards(host) {
    var scope = host || document;
    var cards = scope.querySelectorAll('[data-workbuddy="quota-card"]');
    for (var i = cards.length - 1; i >= 0; i--) {
      if (cards[i].parentNode) cards[i].parentNode.removeChild(cards[i]);
    }
    var leftover = document.querySelector('[data-workbuddy="quota-section"]');
    if (leftover && leftover.parentNode) leftover.parentNode.removeChild(leftover);
  }

  function decorateQuotaPage() {
    if (!summaries) return;
    var host = findQuotaHost();
    if (!host) return;

    if (!quotaFilterAllowsWorkbuddy() || !summaries.length) {
      removeQuotaCards(host);
      return;
    }

    var leftover = host.querySelector('[data-workbuddy="quota-section"]');
    if (leftover && leftover.parentNode) leftover.parentNode.removeChild(leftover);

    var live = {};
    summaries.forEach(function (summary) {
      live[summary.auth_index] = true;
      var existing = host.querySelector('[data-wb-account="' + cssEscape(summary.auth_index) + '"]');
      if (existing) {
        paintQuotaCard(summary, existing);
        return;
      }
      var node = quotaCard(summary);
      host.appendChild(node);
      paintQuotaCard(summary, node);
    });

    var cards = host.querySelectorAll('[data-workbuddy="quota-card"]');
    for (var i = cards.length - 1; i >= 0; i--) {
      var key = cards[i].getAttribute('data-wb-account');
      if (!live[key]) {
        delete details[key];
        delete pending[key];
        if (cards[i].parentNode) cards[i].parentNode.removeChild(cards[i]);
      }
    }
  }

  function cssEscape(value) {
    return String(value).replace(/["\\]/g, '\\$&');
  }

  function quotaCard(summary) {
    var card = document.createElement('article');
    card.className = skin('QuotaCard-module__card', 'wb-qc-card');
    card.setAttribute(MARK, 'quota-card');
    card.setAttribute('data-wb-account', summary.auth_index);

    var head = document.createElement('header');
    head.className = skin('QuotaCard-module__head', 'wb-qc-head');
    var icon = document.createElement('span');
    icon.className = skin('QuotaCard-module__iconWrap', 'wb-qc-icon');
    icon.title = 'WorkBuddy';
    var fallback = document.createElement('span');
    fallback.className = skin('QuotaCard-module__iconFallback', '');
    fallback.textContent = 'W';
    icon.appendChild(fallback);
    head.appendChild(icon);
    var file = document.createElement('span');
    file.className = skin('QuotaCard-module__fileName', 'wb-qc-file');
    file.setAttribute(MARK, 'quota-file');
    file.textContent = summary.name || summary.label || summary.auth_index;
    file.title = summary.label || summary.name || '';
    head.appendChild(file);
    card.appendChild(head);

    var body = document.createElement('div');
    body.className = skin('QuotaCard-module__body', 'wb-qc-body');
    body.setAttribute(MARK, 'quota-body');
    card.appendChild(body);

    var actions = document.createElement('footer');
    actions.className = skin('QuotaCard-module__actionRow', 'wb-qc-actions');
    actions.setAttribute(MARK, 'quota-actions');
    actions.hidden = true;
    card.appendChild(actions);

    return card;
  }

  function paintQuotaCard(summary, card) {
    var body = card.querySelector('[data-workbuddy="quota-body"]');
    var actions = card.querySelector('[data-workbuddy="quota-actions"]');
    var file = card.querySelector('[data-workbuddy="quota-file"]');
    var detail = detailOf(summary.auth_index);
    var inFlight = !!pending[summary.auth_index];
    var signature = paintSignature(summary, detail, inFlight);
    if (card.getAttribute('data-wb-paint') === signature) return;
    card.setAttribute('data-wb-paint', signature);

    if (file) {
      file.textContent = summary.name || summary.label || summary.auth_index;
      file.title = summary.label || summary.name || '';
    }

    body.textContent = '';
    actions.textContent = '';

    if (!detail && inFlight) {
      actions.hidden = true;
      body.appendChild(quotaSkeleton());
      return;
    }

    if (!detail) {
      actions.hidden = true;
      var idle = document.createElement('button');
      idle.type = 'button';
      idle.className = skin('QuotaCard-module__idleBody', 'wb-qc-idle');
      idle.setAttribute(MARK, 'quota-refresh');
      idle.appendChild(refreshGlyph(15));
      var hint = document.createElement('span');
      hint.className = skin('QuotaCard-module__idleHint', 'wb-qc-idleHint');
      hint.textContent = '点击此处刷新额度';
      idle.appendChild(hint);
      idle.addEventListener('click', function () { loadDetail(summary.auth_index, true); });
      body.appendChild(idle);
      return;
    }

    if (detail.error) {
      var err = document.createElement('div');
      err.className = skin('QuotaCard-module__errorStrip', 'wb-qc-error');
      err.setAttribute('role', 'alert');
      err.textContent = '额度获取失败：' + detail.error;
      body.appendChild(err);
      actions.hidden = false;
      actions.appendChild(quotaPill('刷新额度', function () {
        loadDetail(summary.auth_index, true);
      }, 'quota-refresh', true));
      return;
    }

    body.appendChild(quotaRow(summary, detail));
    actions.hidden = false;
    var claim = quotaPill(detail.checked_in_today ? '今日已签到' : '签到', null, 'quota-checkin', false);
    claim.disabled = !!detail.checked_in_today;
    if (!detail.checked_in_today) {
      claim.addEventListener('click', function () { runClaim(summary, claim); });
    }
    actions.appendChild(claim);
    var tasksDone = !!detail.growth_tasks_all_done;
    var tasks = quotaPill(tasksDone ? '任务已完成' : '一键任务', null, 'quota-tasks', false);
    tasks.disabled = tasksDone;
    tasks.title = tasksDone
      ? '成长中心可自动任务均已领取'
      : ('自动完成成长中心全部可脚本化任务（待完成 ' + (detail.growth_tasks_pending || 0) + ' 项）');
    if (!tasksDone) {
      tasks.addEventListener('click', function () { runTasksAuto(summary, tasks); });
    }
    actions.appendChild(tasks);
    actions.appendChild(quotaPill('刷新额度', function () {
      loadDetail(summary.auth_index, true);
    }, 'quota-refresh', true));
  }

  function paintSignature(summary, detail, inFlight) {
    if (!detail) return (inFlight ? 'loading:' : 'idle:') + summary.auth_index;
    if (detail.error) return 'error:' + detail.error;
    var credits = detail.credits || {};
    return [
      'ok', summary.auth_index, detail.checked_in_today, detail.streak_days,
      detail.growth_tasks_all_done, detail.growth_tasks_pending,
      detail.activity_name, credits.remain, credits.total, credits.used
    ].join(':');
  }

  function quotaSkeleton() {
    var box = document.createElement('div');
    box.className = skin('QuotaCard-module__skeleton', 'wb-qc-skel');
    box.setAttribute('aria-busy', 'true');
    for (var i = 0; i < 2; i++) {
      var row = document.createElement('div');
      row.className = skin('QuotaCard-module__skeletonRow', 'wb-qc-skelRow');
      var label = document.createElement('span');
      label.className = skin('QuotaCard-module__skeletonLabel', 'wb-qc-skelLabel');
      var track = document.createElement('span');
      track.className = skin('QuotaCard-module__skeletonTrack', 'wb-qc-skelTrack');
      row.appendChild(label);
      row.appendChild(track);
      box.appendChild(row);
    }
    return box;
  }

  function quotaPill(label, onClick, mark, withGlyph) {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = skin('QuotaCard-module__actionPill', 'wb-qc-pill');
    if (mark) btn.setAttribute(MARK, mark);
    if (withGlyph) btn.appendChild(refreshGlyph(13));
    btn.appendChild(document.createTextNode(label));
    if (onClick) btn.addEventListener('click', onClick);
    return btn;
  }

  function quotaRow(summary, detail) {
    var credits = detail.credits || { remain: 0, total: 0, used: 0, unit: 'credits' };
    var remain = Number(credits.remain);
    var total = Number(credits.total);
    var pct = total > 0 && isFinite(remain) ? Math.min(100, Math.max(0, (remain / total) * 100)) : 0;

    var row = document.createElement('div');
    row.className = skin('QuotaBody-module__quotaRow', 'wb-qc-row');

    var head = document.createElement('div');
    head.className = skin('QuotaBody-module__quotaRowHeader', 'wb-qc-rowHead');
    var model = document.createElement('span');
    model.className = skin('QuotaBody-module__quotaModel', 'wb-qc-model');
    model.textContent = detail.activity_name || '积分';
    head.appendChild(model);

    var meta = document.createElement('div');
    meta.className = skin('QuotaBody-module__quotaMeta', 'wb-qc-meta');
    var percent = document.createElement('span');
    percent.className = skin('QuotaBody-module__quotaPercent', 'wb-qc-percent');
    percent.textContent = '剩余 ' + Math.round(pct) + '%';
    meta.appendChild(percent);
    var amount = document.createElement('span');
    amount.className = skin('QuotaBody-module__quotaAmount', 'wb-qc-amount');
    amount.textContent = fmt(credits.remain) + ' / ' + fmt(credits.total);
    meta.appendChild(amount);
    if (detail.checked_in_today || detail.streak_days) {
      var reset = document.createElement('span');
      reset.className = skin('QuotaBody-module__quotaReset', 'wb-qc-reset');
      reset.textContent = detail.checked_in_today
        ? ('今日已签到 · 连续 ' + (detail.streak_days || 0) + ' 天')
        : ('连续 ' + (detail.streak_days || 0) + ' 天');
      meta.appendChild(reset);
    }
    head.appendChild(meta);
    row.appendChild(head);

    var bar = document.createElement('div');
    bar.className = skin('QuotaBody-module__quotaBar', 'wb-qc-bar');
    var fill = document.createElement('div');
    var tone = pct >= 70
      ? skin('QuotaBody-module__quotaBarFillHigh', 'wb-qc-fill is-high')
      : pct >= 30
        ? skin('QuotaBody-module__quotaBarFillMedium', 'wb-qc-fill is-mid')
        : skin('QuotaBody-module__quotaBarFillLow', 'wb-qc-fill is-low');
    fill.className = skin('QuotaBody-module__quotaBarFill', 'wb-qc-fill') + ' ' + tone;
    fill.style.width = pct.toFixed(2) + '%';
    bar.appendChild(fill);
    row.appendChild(bar);
    return row;
  }

  function fmt(v) {
    var n = Number(v);
    if (!isFinite(n)) return '—';
    return n.toLocaleString('zh-CN', { maximumFractionDigits: 2 });
  }

  // ---------------------------------------------------------------------------
  // Boot
  // ---------------------------------------------------------------------------
  var scheduled = false;
  var firstLoadStarted = false;

  function kickFirstLoad() {
    if (firstLoadStarted || !sniffedKey) return;
    firstLoadStarted = true;
    loadSummaries(true).then(schedule).catch(schedule);
  }

  function onKeyReady() { kickFirstLoad(); }

  function schedule() {
    if (scheduled) return;
    scheduled = true;
    window.setTimeout(function () {
      scheduled = false;
      decorateAuthCards();
      decorateQuotaPage();
    }, 120);
  }

  function start() {
    installStyles();
    loadSummaries(false).then(schedule).catch(schedule);

    listeners.push(function () {
      var cards = document.querySelectorAll(CARD + '[' + MARK + '="card"]');
      for (var i = 0; i < cards.length; i++) {
        var card = cards[i];
        var summary = summaryFor(card.textContent || '');
        var chip = card.querySelector('[data-workbuddy="chip"]');
        var claim = card.querySelector('[data-workbuddy="checkin"]');
        if (summary && chip && claim) paintAuthCard(summary, chip, claim);
      }
      schedule();
    });

    if (window.MutationObserver && document.body) {
      new MutationObserver(schedule).observe(document.body, { childList: true, subtree: true });
    }

    // Until a key is known there is nothing to ask for, so poll fast and
    // quietly just to notice it, then settle into the steady cadence.
    var bootTicks = 0;
    var boot = window.setInterval(function () {
      bootTicks += 1;
      if (key()) {
        window.clearInterval(boot);
        if (!firstLoadStarted) kickFirstLoad();
        return;
      }
      if (bootTicks > 60) window.clearInterval(boot);
    }, 250);

    window.setInterval(function () {
      if (key()) loadSummaries(true);
    }, 30000);
  }

  installKeySniffer();

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }

  window.__workbuddyPanel = {
    reload: function () { return loadSummaries(true); },
    loadDetail: function (authIndex) { return loadDetail(authIndex, true); }
  };
})();
