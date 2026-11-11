// MyTizi（Windows）界面。
//
// 数据流：后端把整份 UIState（internal/appcore/state.go）通过 "state" 事件推过来，界面据此重绘；
// 按钮只调用 main.App 上的方法，不依赖方法的返回值更新界面。
// 直接用浏览器打开本文件时（没有 Wails 运行时）进入预览模式：用 ?state=connected 等切换示例数据，
// ?theme=dark|light 强制配色。
'use strict';

(function () {
  // ---------- 图标（内联 SVG，currentColor 着色） ----------

  const I = {
    power: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><path d="M12 3.4V12"/><path d="M7.11 7.18A7.6 7.6 0 1 0 16.89 7.18"/></svg>',
    more: '<svg class="icon" viewBox="0 0 16 16" fill="currentColor"><circle cx="3" cy="8" r="1.25"/><circle cx="8" cy="8" r="1.25"/><circle cx="13" cy="8" r="1.25"/></svg>',
    link: '<svg class="icon link-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M6.4 9.6l3.2-3.2"/><path d="M7.3 4.5l1.2-1.2a2.7 2.7 0 0 1 3.8 3.8L11.1 8.3"/><path d="M8.7 11.5l-1.2 1.2a2.7 2.7 0 0 1-3.8-3.8L4.9 7.7"/></svg>',
    refresh: '<svg class="icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M13.6 8a5.6 5.6 0 1 1-5.6-5.6c1.57 0 3.07.62 4.19 1.71L13.6 5.4"/><path d="M13.6 2.2v3.2h-3.2"/></svg>',
    route: '<svg class="icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3"><circle cx="3.2" cy="4" r="1.7"/><circle cx="12.8" cy="4" r="1.7"/><circle cx="8" cy="12.3" r="1.7"/><path d="M5.2 4h5.6M4.2 5.7l2.8 4.9M11.8 5.7L9 10.6" stroke-dasharray="1.3 1.3"/></svg>',
    lifebuoy: '<svg class="icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4"><circle cx="8" cy="8" r="6.2"/><circle cx="8" cy="8" r="2.6"/><path d="M3.6 3.6l2.6 2.6M12.4 3.6L9.8 6.2M3.6 12.4l2.6-2.6M12.4 12.4L9.8 9.8"/></svg>',
    warning: (tone) => `<svg class="warn ${tone}" viewBox="0 0 16 16"><path fill="currentColor" d="M6.7 2.25a1.5 1.5 0 0 1 2.6 0l5.55 9.6A1.5 1.5 0 0 1 13.55 14H2.45a1.5 1.5 0 0 1-1.3-2.15z"/><path d="M8 5.7v3.5" stroke="#fff" stroke-width="1.5" stroke-linecap="round"/><circle cx="8" cy="11.35" r=".95" fill="#fff"/></svg>`,
    wrench: '<svg class="warn blue" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"><path d="M9.9 2.3a3.6 3.6 0 0 0-4.5 4.6L2.2 10.1a1.4 1.4 0 0 0 2 2l3.2-3.2a3.6 3.6 0 0 0 4.6-4.5l-2 2-1.9-.4-.4-1.9z"/></svg>',
    chevron: '<svg class="chev" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"><path d="M3 4.5L6 7.5l3-3"/></svg>',
    check: '<svg class="check" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 8.5l3 3 6-7"/></svg>',
    globeShield: '<svg class="hero" viewBox="0 0 64 64" fill="none"><g stroke="currentColor" stroke-width="3.2" stroke-linecap="round"><circle cx="27" cy="27" r="20"/><ellipse cx="27" cy="27" rx="8.5" ry="20"/><path d="M7 27h40M10.5 17h33M10.5 37h33"/></g><path d="M46.5 30l11 4.2v8.4c0 6.8-4.6 11.6-11 14.2-6.4-2.6-11-7.4-11-14.2v-8.4z" fill="currentColor" style="stroke: var(--bg)" stroke-width="3.6" stroke-linejoin="round"/><path d="M46.5 34.6v17.6c-3.9-2-6.6-5.2-6.6-9.6v-5.5z" style="fill: var(--bg)" opacity=".35"/></svg>',
    download: '<svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="currentColor"/><path d="M8 4.4v6.2M5.4 8.1L8 10.7l2.6-2.6" fill="none" style="stroke: var(--on-accent)" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>',
    paste: '<svg class="icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"><rect x="3" y="2.8" width="10" height="11.6" rx="1.6"/><rect x="5.5" y="1.4" width="5" height="2.8" rx=".9" style="fill: var(--bg)"/><path d="M5.6 7.4h4.8M5.6 10h3.2" stroke-linecap="round"/></svg>',
  };

  // ---------- 工具 ----------

  const $ = (sel) => document.querySelector(sel);
  const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const looksLikeLink = (text) => /^\s*(https?:\/\/\S+|[a-z][a-z0-9+.-]*:\/\/\S*[?&]url=https?\S+)\s*$/i.test(text || '');

  function setHTML(el, html) {
    if (el && el._html !== html) {
      el.innerHTML = html;
      el._html = html;
      return true;
    }
    return false;
  }

  function duration(since) {
    const seconds = Math.max(0, Math.floor((Date.now() - new Date(since).getTime()) / 1000));
    if (seconds >= 3600) return `${Math.floor(seconds / 3600)} 小时 ${Math.floor((seconds % 3600) / 60)} 分`;
    if (seconds >= 60) return `${Math.floor(seconds / 60)} 分钟`;
    return '不到 1 分钟';
  }

  // ---------- 后端 ----------

  let backend = null;

  function wailsBackend() {
    return {
      call: (method, ...args) => window.wails.Call.ByName('main.App.' + method, ...args),
      on: (name, cb) => window.wails.Events.On(name, (event) => cb(event && event.data)),
    };
  }

  async function call(method, ...args) {
    try {
      return await backend.call(method, ...args);
    } catch (err) {
      console.error('调用失败', method, err);
      return undefined;
    }
  }

  // ---------- 状态 ----------

  let state = null;
  const ui = {
    view: '',
    link: '',
    menu: null, // { id, focus, animate }
    sheet: false,
    confirm: false,
  };

  function setState(next) {
    if (!next) return;
    state = next;
    if (!Array.isArray(state.nodes)) state.nodes = [];
    if (!state.settings) state.settings = {};
    render();
  }

  const isBusy = () => state.phase === 'connecting' || state.phase === 'disconnecting';
  const isConnected = () => state.phase === 'connected';

  // ---------- 渲染 ----------

  function render() {
    if (!state) return;
    const onboarding = state.phase === 'needsSubscription';
    document.body.classList.toggle('onboarding-active', onboarding);
    if (onboarding) {
      ui.menu = null;
      ui.sheet = false;
      ui.confirm = false;
      renderOnboarding();
    } else {
      renderMain();
    }
    renderToast();
    renderMenu();
    renderSheet();
    renderConfirm();
  }

  // ---- 引导：添加订阅 ----

  function renderOnboarding() {
    const app = $('#app');
    if (ui.view !== 'onboarding') {
      ui.view = 'onboarding';
      app._html = null;
      app.innerHTML = `
        <main class="onboarding">
          <div class="top"></div>
          ${I.globeShield}
          <h1>添加订阅</h1>
          <p class="lead">粘贴服务商或公司提供的订阅链接，<br>其余的规则、DNS 和 IPv6 都由我们处理。</p>
          <form id="add-form" autocomplete="off">
            <input id="link" class="text-input" type="text" inputmode="url" placeholder="https://…" spellcheck="false" aria-label="订阅链接">
            <button id="paste" class="btn" type="button" data-action="paste">${I.paste}<span>粘贴</span></button>
            <button id="add" class="btn accent wide" type="submit"></button>
            <p id="add-error" class="error" hidden></p>
          </form>
          <div class="bottom"></div>
          <p class="footnote">订阅内容只保存在这台电脑上</p>
        </main>`;
      const input = $('#link');
      input.value = ui.link;
      input.addEventListener('input', () => {
        ui.link = input.value;
        renderOnboarding();
      });
      $('#add-form').addEventListener('submit', (e) => {
        e.preventDefault();
        addSubscription();
      });
      setTimeout(() => input.focus(), 30);
    }
    const adding = !!state.addingSubscription;
    const input = $('#link');
    if (input.value !== ui.link && document.activeElement !== input) input.value = ui.link;
    input.disabled = adding;
    $('#paste').disabled = adding;
    const add = $('#add');
    add.disabled = adding || !ui.link.trim();
    setHTML(add, adding
      ? `<span class="spinner"></span><span>${esc(state.addProgress || '正在添加…')}</span>`
      : '<span>添加并检测</span>');
    const error = $('#add-error');
    error.hidden = !state.addError;
    error.textContent = state.addError || '';
  }

  function addSubscription() {
    if (!state || state.addingSubscription || !ui.link.trim()) return;
    call('AddSubscription', ui.link.trim());
  }

  async function pasteLink() {
    const text = await call('ClipboardText');
    if (!text) return;
    ui.link = String(text).trim();
    const input = $('#link');
    if (input) input.value = ui.link;
    renderOnboarding();
    if (looksLikeLink(ui.link)) addSubscription();
  }

  // ---- 主界面 ----

  function renderMain() {
    const app = $('#app');
    if (ui.view !== 'main') {
      ui.view = 'main';
      ui.link = '';
      app._html = null;
      app.innerHTML = `
        <main class="main" id="main">
          <header class="header">
            <span class="app-name">MyTizi</span>
            <button class="subtle icon-btn" data-menu="more" title="更多" aria-label="更多">${I.more}</button>
          </header>
          <div class="spacer"></div>
          <section class="status">
            <div id="r-power"></div>
            <div id="r-status" class="status-text"></div>
          </section>
          <div class="spacer mid"></div>
          <section class="cards" id="r-cards"></section>
          <div class="spacer mid"></div>
          <footer class="footer" id="r-footer"></footer>
        </main>`;
    }
    const alerts = [state.serviceProblem, state.connectivityIssue, state.phase === 'failed'].filter(Boolean).length;
    $('#main').classList.toggle('compact', alerts >= 2);
    setHTML($('#r-power'), powerHTML());
    $('#r-power').classList.toggle('glow', isConnected());
    setHTML($('#r-status'), statusHTML());
    setHTML($('#r-cards'), cardsHTML());
    setHTML($('#r-footer'), footerHTML());
  }

  function powerHTML() {
    const tint = { connected: 'green', connecting: 'orange', disconnecting: 'orange', failed: 'red' }[state.phase] || '';
    const busy = isBusy();
    const title = isConnected() ? '点击断开' : '点击连接';
    return `<button class="power ${tint}" data-action="toggle" title="${title}" aria-label="${title}" ${busy ? 'disabled' : ''}>`
      + (busy ? '<span class="spinner lg"></span>' : I.power) + '</button>';
  }

  function statusHTML() {
    const titles = { connected: '已连接', connecting: '正在连接…', disconnecting: '正在断开…', failed: '连接失败' };
    const title = titles[state.phase] || '未连接';
    let sub;
    switch (state.phase) {
      case 'connected': {
        const node = state.activeNode || '…';
        const row = state.nodes.find((n) => n.name === state.activeNode);
        const delay = row && row.delay > 0 ? ` · ${row.delay} ms` : '';
        const started = state.startedAt ? `　已运行 ${duration(state.startedAt)}` : '';
        sub = node + delay + started;
        break;
      }
      case 'connecting': sub = state.step || ''; break;
      case 'disconnecting': sub = ''; break;
      case 'failed': sub = '点击重试'; break;
      default: sub = '点击上方按钮连接';
    }
    return `<div class="status-title">${esc(title)}</div><div class="status-sub">${esc(sub)}</div>`;
  }

  function cardsHTML() {
    const parts = [];
    if (state.serviceProblem) {
      const repairing = !!state.repairingService;
      parts.push(`
        <div class="card alert blue">
          <div class="alert-row">${I.wrench}<div class="alert-text">${esc(state.serviceProblem)}</div></div>
          <div class="alert-actions">
            <button class="btn small" data-action="repair" ${repairing ? 'disabled' : ''}>${repairing ? '<span class="spinner"></span>正在修复…' : '修复后台组件'}</button>
            <span class="caption">需要一次管理员授权</span>
          </div>
        </div>`);
    }
    if (state.connectivityIssue) {
      parts.push(`
        <div class="card alert orange">
          <div class="alert-row">${I.warning('orange')}<div class="alert-text">${esc(state.connectivityIssue)}</div>
            <button class="btn small" data-action="diagnose" ${state.copyingDiagnostics ? 'disabled' : ''}>诊断</button>
          </div>
        </div>`);
    }
    if (state.phase === 'failed') {
      parts.push(`
        <div class="card alert red">
          <div class="alert-row">${I.warning('red')}<div class="alert-text">${esc(state.errorMessage || '连接失败')}</div></div>
          <div class="alert-actions">
            <button class="btn small" data-action="diagnose" ${state.copyingDiagnostics ? 'disabled' : ''}>复制诊断信息</button>
            <span class="caption">粘贴给 AI 即可排查</span>
          </div>
        </div>`);
    }
    const sub = state.subscription;
    if (sub) {
      const refresh = state.refreshingSubscription
        ? '<span class="spinner" aria-label="正在更新"></span>'
        : `<button class="subtle icon-btn sm" data-action="refresh" title="立即更新订阅" aria-label="立即更新订阅">${I.refresh}</button>`;
      let usage = '';
      if (sub.usage) {
        const fraction = Math.max(0, Math.min(1, Number(sub.usage.fraction) || 0));
        usage = `
          <div class="usage">
            <div class="bar"><div class="${fraction > 0.9 ? 'hot' : ''}" style="width:${(fraction * 100).toFixed(1)}%"></div></div>
            <div class="usage-text"><span>已用 ${esc(sub.usage.usedText)} / ${esc(sub.usage.totalText)}</span>${sub.usage.expireText ? `<span>到期 ${esc(sub.usage.expireText)}</span>` : ''}</div>
          </div>`;
      }
      parts.push(`
        <div class="card">
          <div class="sub-head">${I.link}<span class="sub-name" title="${esc(sub.name)}">${esc(sub.name)}</span>${refresh}</div>
          <div class="sub-meta">${esc([sub.host, `${sub.nodeCount} 条线路`, sub.updatedText].filter(Boolean).join(' · '))}</div>
          ${usage}
        </div>`);
    }
    const connected = isConnected();
    const label = state.selection === 'AUTO' || !state.selection ? '自动选择' : state.selection;
    parts.push(`
      <div class="card line-picker" title="${connected ? '选择走哪条线路' : '连接后可以切换线路'}">
        <div class="label">${I.route}<span>线路</span></div>
        <button class="subtle picker-btn" data-menu="lines" ${connected ? '' : 'disabled'}><span>${esc(label)}</span>${I.chevron}</button>
      </div>`);
    return parts.join('');
  }

  function footerHTML() {
    const update = state.availableUpdate;
    const right = update
      ? `<button class="subtle update-btn" data-menu="update" title="发现新版本，点击下载">${I.download}<span>新版本 v${esc(update.version)}</span></button>`
      : `<span class="version">${state.version === 'dev' || !state.version ? 'dev' : 'v' + esc(state.version)}</span>`;
    return `<button class="subtle trouble" data-menu="trouble">${I.lifebuoy}<span>遇到问题</span>${I.chevron}</button>${right}`;
  }

  // ---- 底部提示 ----

  function renderToast() {
    const toast = $('#toast');
    if (state.notice) {
      if (toast.textContent !== state.notice) toast.textContent = state.notice;
      toast.classList.add('show');
    } else {
      toast.classList.remove('show');
    }
  }

  // ---------- 菜单 ----------

  function menuItems(id) {
    const s = state;
    switch (id) {
      case 'more':
        return [
          { label: '更新订阅', disabled: s.refreshingSubscription || !s.subscription, run: () => call('RefreshSubscription') },
          { label: s.checkingUpdate ? '正在检查更新…' : '检查更新…', disabled: s.checkingUpdate, run: () => call('CheckUpdates') },
          { label: '高级设置…', run: openSheet },
        ];
      case 'trouble':
        return [
          { label: '复制诊断信息（给 AI 排查）', disabled: s.copyingDiagnostics, run: () => call('CopyDiagnostics', false) },
          { label: '复制完整诊断（含最近连接日志）', disabled: s.copyingDiagnostics, run: () => call('CopyDiagnostics', true) },
          'sep',
          { label: '打开日志文件夹', run: () => call('OpenLogsFolder') },
          'sep',
          { label: '重新连接', disabled: isBusy(), run: () => call('Reconnect') },
        ];
      case 'update':
        if (!s.availableUpdate) return [];
        return [
          { label: `下载 v${s.availableUpdate.version}`, run: () => call('OpenUpdate') },
          { label: '忽略此版本', run: () => call('SkipUpdate') },
        ];
      case 'lines': {
        const selection = s.selection || 'AUTO';
        return [
          { label: '自动选择（延迟最低）', checked: selection === 'AUTO', run: () => selectNode('AUTO') },
          'sep',
          ...s.nodes.map((n) => ({ label: n.name, meta: n.delay > 0 ? `${n.delay} ms` : '', checked: selection === n.name, run: () => selectNode(n.name) })),
          ...(s.nodes.length ? ['sep'] : []),
          { label: s.testingLatency ? '正在测速…' : '重新测速', disabled: s.testingLatency, run: () => call('TestLatency') },
        ];
      }
      default:
        return [];
    }
  }

  function selectNode(name) {
    state.selection = name;
    render();
    call('SelectNode', name);
  }

  function openMenu(id) {
    const anchor = document.querySelector(`[data-menu="${id}"]`);
    if (!anchor || anchor.disabled) return;
    ui.menu = { id, focus: -1, animate: true };
    renderMenu();
  }

  function closeMenu() {
    ui.menu = null;
    renderMenu();
  }

  function renderMenu() {
    const layer = $('#menu-layer');
    const anchor = ui.menu && document.querySelector(`[data-menu="${ui.menu.id}"]`);
    const items = ui.menu ? menuItems(ui.menu.id) : [];
    if (!ui.menu || !anchor || anchor.disabled || !items.length) {
      ui.menu = null;
      layer.hidden = true;
      setHTML(layer, '');
      return;
    }
    const checks = items.some((it) => it !== 'sep' && 'checked' in it);
    const html = `<div class="menu ${checks ? '' : 'no-check'}" role="menu">` + items.map((it, i) => it === 'sep'
      ? '<div class="menu-sep" role="separator"></div>'
      : `<button class="menu-item ${it.checked ? 'checked' : ''}" role="menuitem" data-menu-item="${i}" ${it.disabled ? 'disabled' : ''}>`
        + (checks ? I.check : '') + `<span class="grow">${esc(it.label)}</span>`
        + (it.meta ? `<span class="meta">${esc(it.meta)}</span>` : '') + '</button>').join('') + '</div>';
    layer.hidden = false;
    if (!setHTML(layer, html) && !ui.menu.animate) return;
    const menu = layer.firstElementChild;
    if (!ui.menu.animate) menu.style.animation = 'none';
    ui.menu.animate = false;
    positionMenu(menu, anchor.getBoundingClientRect());
  }

  function positionMenu(menu, rect) {
    const margin = 8;
    const vw = document.body.clientWidth;
    const vh = document.body.clientHeight;
    const below = vh - rect.bottom - margin - 4;
    const above = rect.top - margin - 4;
    menu.style.maxHeight = 'none';
    const natural = menu.scrollHeight + 2;
    const up = natural > below && above > below;
    menu.classList.toggle('up', up);
    const room = up ? above : below;
    menu.style.maxHeight = Math.max(80, room) + 'px';
    const width = Math.min(menu.offsetWidth, vw - margin * 2);
    let left = rect.left + rect.width / 2 > vw / 2 ? rect.right - width : rect.left;
    left = Math.max(margin, Math.min(left, vw - margin - width));
    menu.style.left = left + 'px';
    if (up) {
      menu.style.top = '';
      menu.style.bottom = (vh - rect.top + 4) + 'px';
    } else {
      menu.style.bottom = '';
      menu.style.top = (rect.bottom + 4) + 'px';
    }
    // 打开线路菜单时把当前选中项滚到可见处。
    const checked = menu.querySelector('.menu-item.checked');
    if (checked && menu.scrollHeight > menu.clientHeight) checked.scrollIntoView({ block: 'nearest' });
  }

  function moveMenuFocus(delta) {
    const buttons = [...document.querySelectorAll('#menu-layer .menu-item:not(:disabled)')];
    if (!buttons.length) return;
    let index = buttons.indexOf(document.activeElement);
    index = index < 0 ? (delta > 0 ? 0 : buttons.length - 1) : (index + delta + buttons.length) % buttons.length;
    buttons[index].focus();
  }

  // ---------- 高级设置 ----------

  function openSheet() {
    ui.sheet = true;
    renderSheet();
  }

  function closeSheet() {
    ui.sheet = false;
    ui.confirm = false;
    renderSheet();
    renderConfirm();
  }

  function setting(key, title, caption) {
    const on = !!state.settings[key];
    return `
      <div class="setting" data-setting="${key}">
        <div class="setting-text">
          <div class="setting-title">${esc(title)}</div>
          ${caption ? `<div class="setting-caption">${esc(caption)}</div>` : ''}
        </div>
        <button class="switch" role="switch" aria-checked="${on}" aria-label="${esc(title)}"></button>
      </div>`;
  }

  function renderSheet() {
    const layer = $('#sheet-layer');
    if (!ui.sheet || !state || state.phase === 'needsSubscription') {
      layer.hidden = true;
      setHTML(layer, '');
      return;
    }
    const checking = !!state.checkingUpdate;
    const version = state.version === 'dev' || !state.version ? 'dev' : 'v' + esc(state.version);
    const wasHidden = layer.hidden;
    layer.hidden = false;
    setHTML(layer, `
      <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="adv-title">
        <div class="dialog-body">
          <h2 class="dialog-title" id="adv-title">高级设置</h2>
          ${setting('strictRoute', '严格接管（推荐）', '拦截其他网卡上的 DNS 查询和所有 IPv6 流量，防止绕过规则。断开后自动失效，不会改动系统设置。')}
          ${setting('autoConnectOnLaunch', '打开 App 时自动连接')}
          ${setting('launchAtLogin', '登录 Windows 时自动打开 MyTizi')}
          ${setting('autoCheckUpdates', '自动检查更新', '每天检查一次 GitHub Releases，只提示，不会自动安装。')}
          <div class="divider"></div>
          <div class="button-row">
            <button class="btn" data-action="customRules">编辑自定义规则…</button>
            <button class="btn" data-action="confirmRemove">更换订阅…</button>
          </div>
          <div class="button-row">
            <button class="btn" data-action="sheetCheckUpdates" ${checking ? 'disabled' : ''}>${checking ? '正在检查更新…' : '检查更新…'}</button>
            <button class="link-btn" data-action="projectPage">项目主页</button>
            <span class="push">${version}</span>
          </div>
          <p class="hint">修改网络相关选项后，需要重新连接才会生效。</p>
        </div>
        <div class="dialog-footer">
          ${isConnected() ? '<button class="btn" data-action="sheetReconnect">重新连接</button>' : ''}
          <button class="btn accent" data-action="closeSheet">完成</button>
        </div>
      </div>`);
    if (wasHidden) setTimeout(() => { const done = layer.querySelector('[data-action="closeSheet"]'); if (done) done.focus({ preventScroll: true }); }, 20);
  }

  function renderConfirm() {
    const layer = $('#confirm-layer');
    if (!ui.confirm || !ui.sheet) {
      layer.hidden = true;
      setHTML(layer, '');
      return;
    }
    const wasHidden = layer.hidden;
    layer.hidden = false;
    setHTML(layer, `
      <div class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="confirm-title">
        <div class="dialog-body">
          <h2 class="dialog-title" id="confirm-title">更换订阅？</h2>
          <p class="dialog-text">更换订阅会断开当前连接并删除已保存的订阅。</p>
        </div>
        <div class="dialog-footer">
          <button class="btn danger-text" data-action="doRemove">更换订阅</button>
          <button class="btn accent" data-action="cancelConfirm">取消</button>
        </div>
      </div>`);
    if (wasHidden) setTimeout(() => { const cancel = layer.querySelector('[data-action="cancelConfirm"]'); if (cancel) cancel.focus(); }, 20);
  }

  function toggleSetting(key) {
    const value = !state.settings[key];
    state.settings[key] = value; // 先改界面，后端随后推送确认后的状态
    renderSheet();
    call('SetSetting', key, value);
  }

  // ---------- 交互 ----------

  const actions = {
    paste: pasteLink,
    toggle: () => { if (!isBusy()) call('Toggle'); },
    diagnose: () => call('CopyDiagnostics', false),
    refresh: () => call('RefreshSubscription'),
    repair: () => call('RepairService'),
    customRules: () => call('OpenCustomRules'),
    confirmRemove: () => { ui.confirm = true; renderConfirm(); },
    cancelConfirm: () => { ui.confirm = false; renderConfirm(); },
    doRemove: () => { closeSheet(); call('RemoveSubscription'); },
    sheetCheckUpdates: () => { closeSheet(); call('CheckUpdates'); },
    projectPage: () => call('OpenProjectPage'),
    sheetReconnect: () => { closeSheet(); call('Reconnect'); },
    closeSheet,
  };

  document.addEventListener('click', (event) => {
    const target = event.target;
    const layer = $('#menu-layer');
    if (!layer.hidden) {
      const item = target.closest('[data-menu-item]');
      if (item && !item.disabled) {
        const entry = menuItems(ui.menu.id)[Number(item.dataset.menuItem)];
        closeMenu();
        if (entry && entry.run) entry.run();
        return;
      }
      if (!target.closest('.menu')) closeMenu();
      return;
    }
    const menuButton = target.closest('[data-menu]');
    if (menuButton && !menuButton.disabled) {
      openMenu(menuButton.dataset.menu);
      return;
    }
    const settingRow = target.closest('[data-setting]');
    if (settingRow) {
      toggleSetting(settingRow.dataset.setting);
      return;
    }
    const actionEl = target.closest('[data-action]');
    if (actionEl && !actionEl.disabled && actions[actionEl.dataset.action]) {
      actions[actionEl.dataset.action]();
      return;
    }
    // 点在对话框外的遮罩上：关闭（确认框优先）。
    if (target.id === 'confirm-layer') actions.cancelConfirm();
    else if (target.id === 'sheet-layer') closeSheet();
  });

  document.addEventListener('keydown', (event) => {
    const menuOpen = !$('#menu-layer').hidden;
    if (event.key === 'Escape') {
      if (menuOpen) closeMenu();
      else if (ui.confirm) actions.cancelConfirm();
      else if (ui.sheet) closeSheet();
      else return;
      event.preventDefault();
      return;
    }
    if (menuOpen && (event.key === 'ArrowDown' || event.key === 'ArrowUp')) {
      moveMenuFocus(event.key === 'ArrowDown' ? 1 : -1);
      event.preventDefault();
    }
  });

  // 只有输入框保留右键菜单；也不允许把界面元素拖出去。
  document.addEventListener('contextmenu', (event) => {
    if (!event.target.closest('input, textarea')) event.preventDefault();
  });
  document.addEventListener('dragstart', (event) => event.preventDefault());
  window.addEventListener('resize', () => { if (ui.menu) { ui.menu.animate = true; renderMenu(); } });

  // "已运行 x 分钟" 每 30 秒刷新一次。
  setInterval(() => { if (state && isConnected() && ui.view === 'main') setHTML($('#r-status'), statusHTML()); }, 30000);

  // ---------- 预览模式（浏览器直接打开 index.html） ----------

  function previewBackend(kind) {
    const listeners = [];
    const minutesAgo = (m) => new Date(Date.now() - m * 60000).toISOString();
    const subscription = {
      name: '我的订阅', host: 'sub.example.com', nodeCount: 3, updatedText: '3 小时前更新',
      usage: { fraction: 0.215, usedText: '40.05 GB', totalText: '186.26 GB', expireText: '2027年1月18日' },
    };
    const nodes = [
      { name: '香港-01', type: 'Shadowsocks', delay: 82 },
      { name: '日本-02 东京 IPLC', type: 'Trojan', delay: 126 },
      { name: '美国-03', type: 'Vmess', delay: 0 },
    ];
    const base = {
      phase: 'disconnected', step: '', version: '0.4.0', subscription, nodes, selection: 'AUTO', activeNode: '',
      startedAt: null, errorMessage: '', notice: '', connectivityIssue: '', serviceProblem: '',
      addingSubscription: false, addProgress: '', addError: '', refreshingSubscription: false, testingLatency: false,
      copyingDiagnostics: false, checkingUpdate: false, repairingService: false, availableUpdate: null,
      settings: { strictRoute: true, autoConnectOnLaunch: false, autoCheckUpdates: true, launchAtLogin: false },
    };
    const connected = { phase: 'connected', activeNode: '香港-01', startedAt: minutesAgo(47) };
    const variants = {
      onboarding: { phase: 'needsSubscription', subscription: null, nodes: [] },
      adding: { phase: 'needsSubscription', subscription: null, nodes: [], addingSubscription: true, addProgress: '正在下载订阅…' },
      adderror: { phase: 'needsSubscription', subscription: null, nodes: [], addError: '订阅地址返回的是一个网页，不是订阅内容。链接可能已过期，或需要先登录。' },
      disconnected: {},
      connected,
      connecting: { phase: 'connecting', step: '正在启动…' },
      failed: { phase: 'failed', errorMessage: '没能启动虚拟网卡。请先退出其他代理/VPN 软件再试；仍不行请复制诊断信息。' },
      serviceProblem: { serviceProblem: '后台组件没有运行，连接需要它。点“修复后台组件”重新安装并启动。' },
      connectivityIssue: { ...connected, connectivityIssue: '所有线路都连不上：节点服务器可能已经不可用，或者当前网络挡住了代理协议。', nodes: nodes.map((n) => ({ ...n, delay: 0 })) },
      crowded: { phase: 'failed', errorMessage: '与后台组件的连接中断了。', serviceProblem: '后台组件没有运行，连接需要它。点“修复后台组件”重新安装并启动。' },
      advanced: connected,
      confirm: connected,
      menu: connected,
      trouble: connected,
      more: connected,
      update: { ...connected, availableUpdate: { version: '0.5.0' }, notice: '发现新版本 v0.5.0，点右下角下载' },
      notice: { notice: '已复制诊断信息，直接粘贴给 AI 即可' },
      nousage: { ...connected, subscription: { ...subscription, usage: null, name: '公司订阅（很长很长的名字会被截断）' } },
    };
    let s = JSON.parse(JSON.stringify({ ...base, ...(variants[kind] || variants.connected) }));
    let noticeTimer = 0;
    const push = () => { const copy = JSON.parse(JSON.stringify(s)); listeners.forEach((cb) => cb(copy)); };
    const notice = (text, ms) => {
      s.notice = text; push();
      clearTimeout(noticeTimer);
      noticeTimer = setTimeout(() => { s.notice = ''; push(); }, ms || 3000);
    };
    const wait = (ms) => new Promise((r) => setTimeout(r, ms));
    const methods = {
      State: () => JSON.parse(JSON.stringify(s)),
      ClipboardText: () => 'https://sub.example.com/api/v1/client/subscribe?token=preview',
      async AddSubscription(link) {
        s.addingSubscription = true; s.addError = ''; s.addProgress = '正在下载订阅…'; push();
        await wait(700); s.addProgress = '正在解析线路…'; push();
        await wait(700);
        s.addingSubscription = false; s.addProgress = '';
        if (!/^https?:\/\//i.test(link)) { s.addError = '这不是有效的订阅链接，请粘贴以 http:// 或 https:// 开头的完整地址。'; push(); return; }
        Object.assign(s, { phase: 'disconnected', subscription, nodes }); push();
      },
      async Toggle() {
        if (s.phase === 'connected') {
          s.phase = 'disconnecting'; push(); await wait(600);
          Object.assign(s, { phase: 'disconnected', activeNode: '', startedAt: null, connectivityIssue: '' }); push();
        } else {
          Object.assign(s, { phase: 'connecting', step: '正在检查组件…', errorMessage: '' }); push();
          await wait(500); s.step = '正在生成配置…'; push();
          await wait(500); s.step = '正在启动…'; push();
          await wait(700);
          Object.assign(s, { phase: 'connected', step: '', activeNode: '香港-01', startedAt: new Date().toISOString() }); push();
        }
      },
      async Reconnect() { if (s.phase === 'connected') await methods.Toggle(); await methods.Toggle(); },
      async RefreshSubscription() { s.refreshingSubscription = true; push(); await wait(900); s.refreshingSubscription = false; notice('订阅已更新：3 条线路'); },
      SelectNode(name) { s.selection = name; if (s.phase === 'connected') s.activeNode = name === 'AUTO' ? '香港-01' : name; push(); },
      async TestLatency() { s.testingLatency = true; push(); await wait(900); s.testingLatency = false; s.nodes = nodes; push(); },
      async CopyDiagnostics(full) { s.copyingDiagnostics = true; notice('正在收集诊断信息…', 30000); await wait(900); s.copyingDiagnostics = false; notice(full ? '已复制完整诊断（含最近连接日志），直接粘贴给 AI 即可' : '已复制诊断信息，直接粘贴给 AI 即可', 6000); },
      OpenLogsFolder() {}, OpenCustomRules() {}, OpenProjectPage() {}, OpenUpdate() {}, Quit() {},
      SetSetting(key, value) { s.settings[key] = value; push(); },
      RemoveSubscription() { Object.assign(s, { phase: 'needsSubscription', subscription: null, nodes: [], errorMessage: '' }); push(); },
      async CheckUpdates() { s.checkingUpdate = true; push(); await wait(800); s.checkingUpdate = false; s.availableUpdate = { version: '0.5.0' }; notice('发现新版本 v0.5.0', 6000); },
      SkipUpdate() { const v = s.availableUpdate && s.availableUpdate.version; s.availableUpdate = null; notice(`已忽略 v${v}，可以在高级设置里手动检查`); },
      async RepairService() { s.repairingService = true; push(); await wait(1200); s.repairingService = false; s.serviceProblem = ''; notice('后台组件已修复'); },
    };
    return {
      call: async (method, ...args) => {
        console.log('[预览]', method, ...args);
        const fn = methods[method];
        return fn ? fn(...args) : undefined;
      },
      on: (name, cb) => { if (name === 'state') listeners.push(cb); },
    };
  }

  // ---------- 启动 ----------

  async function waitForRuntime(ms) {
    const deadline = Date.now() + ms;
    while (!window.wails && Date.now() < deadline) await new Promise((r) => setTimeout(r, 25));
    return !!window.wails;
  }

  async function start() {
    const params = new URLSearchParams(location.search);
    const theme = params.get('theme');
    if (theme === 'dark' || theme === 'light') document.documentElement.dataset.theme = theme;

    const preview = location.protocol === 'file:' || !(await waitForRuntime(3000));
    if (preview) document.documentElement.classList.add('preview');
    const kind = params.get('state') || 'connected';
    backend = preview ? previewBackend(kind) : wailsBackend();
    backend.on('state', setState);
    if (preview && kind === 'adding') ui.link = 'https://sub.example.com/api/v1/client/subscribe?token=preview';
    setState(await call('State'));

    if (preview) {
      if (kind === 'advanced') openSheet();
      if (kind === 'confirm') { openSheet(); ui.confirm = true; renderConfirm(); }
      if (kind === 'menu') openMenu('lines');
      if (kind === 'trouble') openMenu('trouble');
      if (kind === 'more') openMenu('more');
    }
  }

  document.addEventListener('DOMContentLoaded', start);
})();
