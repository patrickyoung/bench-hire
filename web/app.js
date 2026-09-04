(() => {
  'use strict';

  const view = document.querySelector('#app-view');
  const main = document.querySelector('#main');
  const context = document.querySelector('#page-context');
  const announcer = document.querySelector('#route-announcer');
  const toast = document.querySelector('#toast');

  const state = {
    bootstrap: null,
    token: '',
    pollTimer: null,
    builderTimer: null,
    toastTimer: null,
    newStarted: 0,
    ui: { slug: '', tab: 'work', dir: 'work', file: '', definition: 'GOAL.md', dirty: false, manualDirty: false, editRoutine: '', workerChecks: [], checkSuggestions: [], checkSuggestionNote: '' },
    builderSeen: new Set(),
    cache: {},
  };

  const CADENCES = [
    ['', 'No routine'],
    ['hourly', 'Every hour'],
    ['daily', 'Every day'],
    ['weekdays', 'Weekdays'],
    ['weekly', 'Once a week'],
    ['30m', 'Every 30 minutes'],
    ['15m', 'Every 15 minutes'],
  ];
  const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];
  const STATE_META = {
    scheduled: ['quiet', 'Scheduled', 'later'],
    queued: ['neutral', 'Queued', 'next'],
    running: ['active', 'Running', 'live'],
    waiting: ['warning', 'Waiting', 'wait'],
    done: ['positive', 'Done', 'ok'],
    unfinished: ['warning', 'Unfinished', 'more'],
    broken: ['danger', 'Check broken', 'brk'],
    failed: ['danger', 'Failed', 'fail'],
    'timed-out': ['danger', 'Timed out', 'time'],
    boundary: ['danger', 'Boundary stop', 'stop'],
    unknown: ['danger', 'Unknown effect', '?'],
    cancelled: ['quiet', 'Cancelled', 'x'],
    planned: ['quiet', 'Planned', 'plan'],
    unsubmitted: ['danger', 'Not queued', '!'],
  };

  class RequestError extends Error {
    constructor(message, status, code, nextAction) {
      super(message);
      this.status = status;
      this.code = code;
      this.nextAction = nextAction;
    }
  }

  const esc = (value) => String(value ?? '')
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&#39;');
  const enc = (value) => encodeURIComponent(String(value ?? ''));
  const arr = (value) => (Array.isArray(value) ? value : []);

  function formatDate(value, withTime = true) {
    if (!value) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime()) || date.getFullYear() < 1971) return '—';
    const options = withTime
      ? { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }
      : { month: 'short', day: 'numeric', year: 'numeric' };
    return new Intl.DateTimeFormat(undefined, options).format(date);
  }

  function relative(value) {
    if (!value) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime()) || date.getFullYear() < 1971) return '—';
    const diff = (date.getTime() - Date.now()) / 1000;
    const abs = Math.abs(diff);
    const unit = abs < 60 ? [Math.round(abs), 'second'] : abs < 3600 ? [Math.round(abs / 60), 'minute'] : abs < 86400 ? [Math.round(abs / 3600), 'hour'] : [Math.round(abs / 86400), 'day'];
    const text = `${unit[0]} ${unit[1]}${unit[0] === 1 ? '' : 's'}`;
    return diff < 0 ? `${text} ago` : `in ${text}`;
  }

  function plural(count, word) { return `${count} ${word}${count === 1 ? '' : 's'}`; }

  function initials(name) {
    const parts = String(name || '').trim().split(/\s+/).filter(Boolean);
    return (parts.slice(0, 2).map((p) => p[0]).join('') || 'W').toUpperCase();
  }

  function stateChip(s) {
    const [cls, label] = STATE_META[s] || ['quiet', s || 'unknown'];
    return `<span class="status-chip ${cls}">${esc(label)}</span>`;
  }

  function recordMark(s) {
    const [cls, , glyph] = STATE_META[s] || ['neutral', '', '·'];
    return `<span class="record-mark ${cls}">${esc(glyph)}</span>`;
  }

  function showToast(message, kind = '') {
    toast.textContent = message;
    toast.className = `toast ${kind}`.trim();
    toast.hidden = false;
    clearTimeout(state.toastTimer);
    state.toastTimer = setTimeout(() => { toast.hidden = true; }, 5200);
  }

  async function api(path, options = {}) {
    const init = { method: options.method || 'GET', headers: { Accept: 'application/json', 'X-Hire-Token': state.token } };
    if (options.body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(options.body);
    }
    const response = await fetch(path, init);
    let payload = null;
    try { payload = await response.json(); } catch { payload = null; }
    if (!response.ok) {
      const error = payload?.error || {};
      throw new RequestError(error.message || `Request failed (${response.status})`, response.status, error.code || '', error.nextAction || '');
    }
    return payload;
  }

  async function refreshBootstrap() {
    const data = await api('/api/bootstrap');
    state.bootstrap = data;
    state.token = data.token;
    renderSidebar();
    return data;
  }

  function renderSidebar() {
    const data = state.bootstrap;
    if (!data) return;
    document.querySelector('#worker-count').textContent = String(arr(data.workers).length);
    document.querySelector('#attention-count').textContent = String(arr(data.attention).length);
    const dot = document.querySelector('#runtime-dot');
    const runtime = data.runtime || {};
    const model = runtime.model || {};
    dot.className = 'status-dot ' + (!runtime.ready ? 'attention' : ['ready', 'unproved'].includes(model.state) ? 'ready' : '');
    document.querySelector('#runtime-title').textContent = runtime.summary || 'Runtime';
    document.querySelector('#runtime-copy').textContent = model.state === 'ready'
      ? `${model.model} proved.`
      : model.state === 'unproved'
        ? `${model.model} configured.`
        : (model.message || '').split('. ')[0] + '.';
    const runner = data.runner || {};
    document.querySelector('#runner-line').textContent = runner.paused ? 'Runner paused' : `Runner on · ${runner.active || 0} active`;
  }

  function setNav(name) {
    document.querySelectorAll('.nav-item').forEach((item) => item.classList.toggle('active', item.dataset.nav === name));
  }

  function mount(title, html, nav) {
    clearInterval(state.builderTimer);
    state.builderTimer = null;
    context.textContent = title;
    announcer.textContent = title;
    document.title = `${title} · Bench Hire`;
    setNav(nav);
    view.innerHTML = html;
    main.scrollTo({ top: 0 });
  }

  function stopPolling() {
    clearInterval(state.pollTimer);
    state.pollTimer = null;
  }

  function poll(fn, ms) {
    stopPolling();
    state.pollTimer = setInterval(async () => {
      const active = document.activeElement;
      if (state.ui.dirty || state.ui.editRoutine || (active && view.contains(active) && ['TEXTAREA', 'INPUT', 'SELECT'].includes(active.tagName))) return;
      try { await fn(); } catch (error) { console.warn(error); }
    }, ms);
  }

  // Routing ------------------------------------------------------------------

  function navigate(path) {
    if ((state.ui.dirty || state.ui.manualDirty) && !window.confirm('Discard unsaved manual changes?')) return;
    if (path === `${window.location.pathname}${window.location.search}`) { route(); return; }
    window.history.pushState({}, '', path);
    route();
  }

  document.addEventListener('click', (event) => {
    const link = event.target.closest('a[data-link]');
    if (!link || event.metaKey || event.ctrlKey || event.shiftKey) return;
    event.preventDefault();
    navigate(link.getAttribute('href'));
  });
  window.addEventListener('popstate', route);

  async function route() {
    stopPolling();
    state.ui.dirty = false;
    state.ui.manualDirty = false;
    const path = window.location.pathname.replace(/\/+$/, '') || '/';
    try {
      if (!state.bootstrap) await refreshBootstrap();
      let match;
      if (path === '/') return await renderHome();
      if (path === '/workers') return await renderWorkers();
      if (path === '/attention') return await renderAttention();
      if (path === '/new') return await renderNew();
      if (path === '/setup') return await renderSetup();
      if ((match = path.match(/^\/workers\/([a-z0-9-]+)\/requests\/([a-z0-9-]+)$/))) return await renderRequest(match[1], match[2]);
      if ((match = path.match(/^\/workers\/([a-z0-9-]+)$/))) return await renderWorker(match[1]);
      mount('Not found', `<section class="route-page"><div class="empty-state"><span>?</span><h2>Nothing here</h2><p>That address does not name a page in Hire.</p><a class="primary-button" href="/" data-link>Back home</a></div></section>`, '');
    } catch (error) {
      mount('Error', `<section class="route-page"><div class="empty-state"><span>!</span><h2>Hire could not load this page</h2><p>${esc(error.message)}</p><a class="primary-button" href="/" data-link>Back home</a></div></section>`, '');
    }
  }

  // Home ---------------------------------------------------------------------

  function workerCard(w) {
    const cls = w.checkState !== 'valid' ? 'invalid' : !w.enabled ? 'paused' : '';
    const label = w.checkState !== 'valid' ? 'Check failed' : w.enabled ? 'Deployed' : 'Paused';
    return `<article class="catalog-card ${cls}">
      <div class="card-label-row chip-row"><span class="agent-glyph ${cls}">${esc(initials(w.name))}</span>${stateLabel(label)}</div>
      <h3>${esc(w.name)}</h3>
      <p>${esc(w.purpose)}</p>
      <dl class="compact-facts">
        <div><dt>Work</dt><dd>${w.running ? `${w.running} running · ` : ''}${w.queued} queued · ${w.done} done</dd></div>
        <div><dt>Routines</dt><dd>${arr(w.routines).length ? plural(arr(w.routines).length, 'routine') : 'none'}</dd></div>
        <div><dt>Model</dt><dd>${esc(w.model || 'unset')}</dd></div>
        <div><dt>Needs you</dt><dd>${w.attention ? `${w.attention} item${w.attention === 1 ? '' : 's'}` : 'nothing'}</dd></div>
      </dl>
      <a class="card-link" href="/workers/${enc(w.slug)}" data-link><span>Open worker</span><span aria-hidden="true">→</span></a>
    </article>`;
  }

  function stateLabel(label) {
    const cls = label === 'Deployed' ? 'positive' : label === 'Paused' ? 'warning' : 'danger';
    return `<span class="status-chip ${cls}">${esc(label)}</span>`;
  }

  function requestRow(w, r) {
    const meta = [r.kind, r.source.startsWith('routine:') ? 'routine' : r.source.startsWith('plan:') ? 'planned action' : r.source.startsWith('rerun:') ? 'run again' : 'you'];
    const when = r.state === 'scheduled' ? `runs ${relative(r.notBefore)}` : `updated ${relative(r.updatedAt)}`;
    return `<a class="record-row" href="/workers/${enc(w.slug)}/requests/${enc(r.id)}" data-link>
      ${recordMark(r.state)}
      <div class="record-main"><p class="kicker">${esc(meta.join(' · '))}${w.name ? ` · ${esc(w.name)}` : ''}</p><h3>${esc(r.title)}</h3><p>${esc(r.summary || (r.text || '').slice(0, 140))}</p></div>
      <div class="record-side">${stateChip(r.state)}<small>${esc(when)}</small>${r.children ? `<small>${plural(r.children, 'action')}</small>` : ''}</div>
    </a>`;
  }

  async function renderHome() {
    const data = await refreshBootstrap();
    const workers = arr(data.workers);
    const attention = arr(data.attention);
    const totals = workers.reduce((acc, w) => ({ queued: acc.queued + w.queued, running: acc.running + w.running, done: acc.done + w.done }), { queued: 0, running: 0, done: 0 });
    const model = data.runtime?.model || {};
    const html = `<section class="route-page">
      <div class="hero-grid">
        <div class="hero-copy">
          <p class="eyebrow">Bench Hire</p>
          <h1>Hire a <span>digital worker</span> in five minutes.</h1>
          <p class="lede">Describe the job, press Deploy, and give it work. The worker gets its own workspace and state, runs on a schedule or on request, resumes what it was doing after an interruption, and only calls a request done when a program agrees.</p>
          <div class="hero-actions"><a class="primary-button" href="/new" data-link>New worker</a><a class="secondary-button" href="/setup" data-link>Setup</a></div>
        </div>
        <aside class="identity-card">
          <span class="identity-seal">h</span>
          <div class="identity-copy">
            <p class="kicker">What a worker is made of</p>
            <h2>Ordinary Bench programs, no new runtime.</h2>
            <p>Every worker is an <code>agent</code> home on disk. Hire adds the door, the inbox, and the clock.</p>
            <dl class="identity-contract">
              <div><dt>Workspace</dt><dd><code>work/</code> for deliverables, <code>state/kv/</code> for facts, one home per worker</dd></div>
              <div><dt>Schedule</dt><dd>routines and later actions become <code>tend</code> jobs at absolute times</dd></div>
              <div><dt>Done</dt><dd><code>bin/check</code> exits 0 only when the request's RESULT.md exists and its check passes</dd></div>
              <div><dt>Resume</dt><dd>one checkpoint per request; a retry continues the same conversation</dd></div>
            </dl>
            <div class="identity-boundary"><strong>Boundary</strong><span>Cage confines writes to work/ and state/, network stays off unless you allow it, and external effects remain proposals for a person.</span></div>
          </div>
        </aside>
      </div>
      <div class="stats-grid">
        <div class="stat-card green"><strong>${workers.length}</strong><span>Workers</span><small>${workers.filter((w) => w.enabled && w.checkState === 'valid').length} deployed</small></div>
        <div class="stat-card"><strong>${totals.queued}</strong><span>Queued</span><small>waiting for a runner or a time</small></div>
        <div class="stat-card purple"><strong>${totals.running}</strong><span>Running</span><small>${data.runner?.paused ? 'runner paused' : `${data.runner?.workers || 0} runner loops`}</small></div>
        <div class="stat-card yellow"><strong>${totals.done}</strong><span>Done</span><small>check accepted</small></div>
        <div class="stat-card coral"><strong>${attention.length}</strong><span>Needs attention</span><small>${attention.length ? 'a person decides' : 'nothing waiting on you'}</small></div>
      </div>
      ${!['ready', 'unproved'].includes(model.state) ? `<div class="inline-notice warning">${esc(model.message || 'No model is configured yet.')} ${model.nextAction ? esc(model.nextAction) : ''} <a href="/setup" data-link>Open Setup →</a></div>` : ''}
      <section class="content-section">
        <div class="section-heading"><div><p class="eyebrow">Workers</p><h2>Your desk</h2></div><a href="/workers" data-link>All workers →</a></div>
        ${workers.length ? `<div class="card-grid">${workers.slice(0, 6).map(workerCard).join('')}</div>` : `<div class="empty-state"><span>＋</span><h2>No workers yet</h2><p>The first one takes about five minutes: a name, a job description, and Deploy.</p><a class="primary-button" href="/new" data-link>Create the first worker</a></div>`}
      </section>
      ${attention.length ? `<section class="content-section"><div class="section-heading"><div><p class="eyebrow">Needs attention</p><h2>Waiting on you</h2></div><a href="/attention" data-link>All items →</a></div><div class="record-list">${attention.slice(0, 5).map((r) => requestRow(workers.find((w) => w.slug === r.workerSlug) || { slug: r.workerSlug, name: r.workerSlug }, r)).join('')}</div></section>` : ''}
      <section class="content-section quiet-section">
        <div class="section-heading"><div><p class="eyebrow">How it works</p><h2>Three steps, one boundary</h2></div></div>
        <div class="principle-grid">
          <article><span>01</span><h3>Describe</h3><p>Name the worker and say what it does in plain words. Add a routine if the job repeats.</p></article>
          <article><span>02</span><h3>Deploy</h3><p>Hire writes an agent home and proves it with <code>agent check</code>. No model is consulted; the receipt is a digest.</p></article>
          <article><span>03</span><h3>Give it work</h3><p>Each request becomes timed actions and durable jobs. Done means <code>bin/check</code> exited 0, never that the model said so.</p></article>
        </div>
      </section>
    </section>`;
    mount('Home', html, 'home');
    poll(renderHome, 8000);
  }

  async function renderWorkers() {
    const data = await refreshBootstrap();
    const workers = arr(data.workers);
    mount('Workers', `<section class="route-page">
      <div class="page-head"><div><p class="eyebrow">Workers</p><h1>Every worker on the desk.</h1><p class="lede">Each one is an ordinary agent home you can also open from a terminal.</p></div><a class="primary-button" href="/new" data-link>New worker</a></div>
      ${workers.length ? `<div class="card-grid">${workers.map(workerCard).join('')}</div>` : `<div class="empty-state"><span>＋</span><h2>No workers yet</h2><p>Create the first one in about five minutes.</p><a class="primary-button" href="/new" data-link>Create a worker</a></div>`}
    </section>`, 'workers');
    poll(renderWorkers, 8000);
  }

  async function renderAttention() {
    const data = await refreshBootstrap();
    const items = arr(data.attention);
    const workers = arr(data.workers);
    mount('Needs attention', `<section class="route-page">
      <div class="page-head"><div><p class="eyebrow">Needs attention</p><h1>Work that needs a person.</h1><p class="lede">Unfinished runs can continue, broken checks need a fix, and an unknown effect is never retried until you say so.</p></div></div>
      ${items.length ? `<div class="record-list">${items.map((r) => requestRow(workers.find((w) => w.slug === r.workerSlug) || { slug: r.workerSlug, name: r.workerSlug }, r)).join('')}</div>` : `<div class="empty-state"><span>✓</span><h2>Nothing is waiting on you</h2><p>Every request is queued, running, done, or scheduled.</p></div>`}
    </section>`, 'attention');
    poll(renderAttention, 8000);
  }

  // New worker ---------------------------------------------------------------

  function builderProposalMarkup(proposal) {
    if (!proposal) return '<div class="builder-empty"><strong>No proposal yet</strong><p>Describe the outcome and the builder will draft every definition file for review.</p></div>';
    const files = proposal.files || {};
    const fileNames = [['goal', 'GOAL.md'], ['agents', 'AGENTS.md'], ['soul', 'SOUL.md'], ['plan', 'PLAN.md'], ['memory', 'MEMORY.md'], ['heartbeat', 'HEARTBEAT.md']];
    return `<div class="proposal-head"><div><p class="kicker">Current proposal</p><h3>${esc(proposal.name || 'Unnamed worker')}</h3><p>${esc(proposal.purpose || '')}</p></div><div class="chip-row"><span class="status-chip ${proposal.network ? 'active' : 'quiet'}">${proposal.network ? 'network proposed' : 'network off'}</span><span class="status-chip quiet">${plural(arr(proposal.checks).length, 'extra check')}</span></div></div>
      <div class="proposal-files">${fileNames.map(([key, name]) => `<details><summary><span>${name}</span><small>${files[key] ? `${files[key].length} characters` : 'empty'}</small></summary><pre class="output">${esc(files[key] || 'Not used in this proposal.')}</pre></details>`).join('')}</div>
      ${arr(proposal.checks).length ? `<details class="proposal-checks"><summary>${plural(arr(proposal.checks).length, 'structured acceptance check')}</summary><pre class="output">${esc(JSON.stringify(proposal.checks, null, 2))}</pre></details>` : ''}`;
  }

  const FIT_META = {
    misused: ['danger', 'misused'],
    missing: ['warning', 'missing'],
    used: ['positive', 'used'],
    not_needed: ['quiet', 'not needed'],
  };
  const FIT_RANK = { misused: 3, missing: 2, used: 1, not_needed: 0 };

  // platformCoverageMarkup folds the permanent reviewers' structured findings
  // into one view of the Bench feature catalogue: the worst status per feature
  // wins, and features needing the lead's attention come first.
  function platformCoverageMarkup(session, team) {
    const features = arr(team?.features);
    const reports = arr(session?.reports).filter((report) => report.expert?.kind === 'platform');
    if (!features.length || !reports.length) return '';
    const findings = new Map();
    reports.forEach((report) => arr(report.platformFit).forEach((item) => {
      const current = findings.get(item.feature);
      if (!current || (FIT_RANK[item.status] ?? -1) > (FIT_RANK[current.status] ?? -1)) findings.set(item.feature, { ...item, reviewer: report.expert?.name || '' });
    }));
    if (!findings.size) return '';
    const counts = { used: 0, missing: 0, misused: 0, not_needed: 0 };
    findings.forEach((item) => { counts[item.status] = (counts[item.status] || 0) + 1; });
    const rank = (feature) => { const item = findings.get(feature.id); return item ? FIT_RANK[item.status] ?? -1 : -2; };
    const rows = features.slice().sort((a, b) => rank(b) - rank(a)).map((feature) => {
      const item = findings.get(feature.id);
      const [tone, label] = item ? FIT_META[item.status] || ['quiet', item.status] : ['quiet', 'not assessed'];
      return `<li class="feature-fit ${item ? esc(item.status) : 'unassessed'}"><span class="status-chip ${tone}">${esc(label)}</span><strong title="${esc(feature.description)}">${esc(feature.label)}</strong>${item ? `<small>${esc(item.note)}${item.reviewer ? ` <em>· ${esc(item.reviewer)}</em>` : ''}</small>` : ''}</li>`;
    }).join('');
    const attention = counts.missing + counts.misused;
    return `<div class="feature-coverage"><div class="feature-coverage-head"><div><p class="kicker">Bench feature fit</p><h4>${attention ? `${plural(attention, 'feature')} for the lead to resolve` : 'Every assessed feature fits'}</h4></div><span class="mono-label">${counts.used} used · ${counts.missing} missing · ${counts.misused} misused · ${counts.not_needed} not needed</span></div><ul class="feature-fit-list">${rows}</ul></div>`;
  }

  function builderTeamMarkup(session, team) {
    const reports = arr(session?.reports);
    if (!reports.length) {
      const permanent = arr(team?.permanent);
      const suite = team?.suite?.agent ? ` for ${esc(team.suite.agent)}` : '';
      return `<div class="builder-empty compact"><strong>The review team assembles per message</strong><p>${permanent.length ? `Always present: ${permanent.map((expert) => esc(expert.name)).join(', ')}. ` : 'Three Bench reviewers are always present. '}One to three task experts are selected for each request. Every reviewer reads Hire's platform contract${suite} and, when editing, the live home from <code>agent show</code>, rather than remembering Bench.</p></div>`;
    }
    const cards = reports.map((report) => {
      const expert = report.expert || {};
      const fit = arr(report.platformFit);
      const fitChips = fit.length ? `<div class="chip-row fit-row">${Object.keys(FIT_META).map((status) => { const count = fit.filter((item) => item.status === status).length; return count ? `<span class="status-chip ${FIT_META[status][0]}">${count} ${FIT_META[status][1]}</span>` : ''; }).join('')}</div>` : '';
      const fitList = fit.length ? `<strong>Platform fit</strong><ul>${fit.map((item) => `<li><b>${esc(item.feature.replaceAll('_', ' '))}</b> · ${esc((FIT_META[item.status] || [0, item.status])[1])} — ${esc(item.note)}</li>`).join('')}</ul>` : '';
      const questions = arr(report.questions).length ? `<strong>Questions for you</strong><ul>${report.questions.map((item) => `<li>${esc(item)}</li>`).join('')}</ul>` : '';
      return `<article class="expert-card"><div class="chip-row"><span class="status-chip ${expert.kind === 'platform' ? 'positive' : 'neutral'}">${expert.kind === 'platform' ? 'Bench reviewer' : 'Task expert'}</span>${report.session ? `<span class="status-chip quiet" title="${esc(report.session)}">replayable session</span>` : ''}</div><h4>${esc(expert.name)}</h4><p>${esc(report.summary)}</p>${fitChips}<details><summary>Why this reviewer${arr(report.recommendations).length || arr(report.risks).length || fit.length || arr(report.questions).length ? ' and what it found' : ''}</summary><p>${esc(expert.reason || expert.focus)}</p>${arr(report.recommendations).length ? `<ul>${report.recommendations.map((item) => `<li>${esc(item)}</li>`).join('')}</ul>` : ''}${arr(report.risks).length ? `<strong>Risks</strong><ul>${report.risks.map((item) => `<li>${esc(item)}</li>`).join('')}</ul>` : ''}${questions}${fitList}</details></article>`;
    }).join('');
    return `<div class="expert-list">${cards}</div>${platformCoverageMarkup(session, team)}<p class="mono-label" data-mt="10">${plural(reports.length, 'specialist')} consulted in the latest completed turn · isolated replayable sessions</p>`;
  }

  const turnInProgress = (turn) => Boolean(turn && !['complete', 'failed'].includes(turn.status));

  function builderProgressText(turn) {
    const experts = arr(turn.experts).length;
    const reports = arr(turn.reports).length;
    switch (turn.status) {
      case 'routing': return 'Reading your message and choosing the task experts…';
      case 'reviewing': return `${reports} of ${experts} reviews are in. Each reviewer works in its own session; a review can take a minute or two. You can leave this page; the turn continues on the server.`;
      case 'synthesizing': return `All ${reports} reviews are in. The lead builder is drafting the proposal…`;
      default: return 'Working…';
    }
  }

  const REVIEW_STATE_META = {
    'standing by': ['quiet', 'standing by'],
    waiting: ['quiet', 'waiting for a slot'],
    reviewing: ['active', 'reviewing'],
    done: ['positive', 'review in'],
    failed: ['warning', 'dropped'],
    stopped: ['danger', 'stopped'],
  };
  const STAGES = [
    ['routing', 'Router', 'reads your message and picks 1–3 task experts'],
    ['reviewing', 'Reviewers', 'independent reviews, three at a time'],
    ['synthesizing', 'Lead builder', 'weighs every review and drafts the proposal'],
  ];

  function elapsedBetween(fromIso, toIso) {
    if (!fromIso) return '';
    const from = new Date(fromIso).getTime();
    const to = toIso ? new Date(toIso).getTime() : Date.now();
    const seconds = Math.round((to - from) / 1000);
    if (!Number.isFinite(seconds) || seconds < 0) return '';
    return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`;
  }

  // assemblyStagesMarkup is the router → reviewers → lead strip. Stages the
  // turn has passed are done, the current one glows, and a failed turn marks
  // the stage it died in. Timings come from the persisted stage timestamps.
  function assemblyStagesMarkup(turn) {
    const reached = turn.status === 'complete' ? 3 : turn.reviewedAt ? 2 : turn.routedAt ? 1 : 0;
    const bounds = [[turn.startedAt, turn.routedAt], [turn.routedAt, turn.reviewedAt], [turn.reviewedAt, turn.completedAt]];
    return `<ol class="assembly-stages" aria-label="Turn ${turn.number} stages">${STAGES.map(([, name, what], i) => {
      const cls = i < reached ? 'done' : i === reached ? (turn.status === 'failed' ? 'failed' : 'active') : 'pending';
      const [from, to] = bounds[i];
      const timing = cls === 'pending' ? '' : elapsedBetween(from, cls === 'active' ? null : to || turn.completedAt);
      const detail = i === 1 && turn.experts?.length ? `${arr(turn.reports).length}/${turn.experts.length} in` : what;
      return `<li class="${cls}"><strong>${name}</strong>${esc(detail)}${timing ? `<em>${esc(timing)}</em>` : ''}</li>`;
    }).join('')}</ol>`;
  }

  const workingDots = '<span class="working-dots" aria-hidden="true"><i></i><i></i><i></i></span>';

  // assemblyBoardMarkup shows the team forming and working during a turn:
  // the permanent reviewers stand by while the router picks task experts,
  // cards slide in as roles join, each says what it is checking, and the
  // lead waits until every review is in. Cards animate in only once per turn.
  function assemblyBoardMarkup(turn, team) {
    const routing = turn.status === 'routing';
    const states = turn.reviewStates || {};
    const reportsById = new Map(arr(turn.reports).map((report) => [report.expert?.id, report]));
    const roster = arr(turn.experts).length ? arr(turn.experts) : arr(team?.permanent);
    const failures = arr(turn.failures);
    // Entry animations stagger through CSS nth-of-type rules; the page's
    // Content-Security-Policy forbids inline style attributes.
    const card = (key, cls, tone, label, kind, name, focus, extra = '') => {
      const seenKey = `${turn.number}:${key}`;
      const enter = state.builderSeen.has(seenKey) ? '' : ' enter';
      state.builderSeen.add(seenKey);
      return `<li class="assembly-card ${cls}${enter}"><div class="assembly-card-head"><span class="status-chip ${tone}">${esc(label)}${label === 'reviewing' || label === 'choosing' || label === 'drafting' ? workingDots : ''}</span><span class="status-chip quiet">${esc(kind)}</span></div><h4>${esc(name)}</h4><p>${esc(focus)}</p>${extra}</li>`;
    };
    const cards = roster.map((expert) => {
      const st = routing ? 'standing by' : (states[expert.id] || (reportsById.has(expert.id) ? 'done' : 'waiting'));
      const [tone, label] = REVIEW_STATE_META[st] || ['quiet', st];
      const report = reportsById.get(expert.id);
      const failure = failures.find((item) => item.startsWith(expert.name));
      const extra = report ? `<small>${esc(report.summary)}</small>` : failure ? `<small class="muted">${esc(failure)}</small>` : '';
      return card(expert.id, `${st.replace(' ', '-')} ${expert.kind}`, tone, label, expert.kind === 'platform' ? 'Bench reviewer' : 'Task expert', expert.name, expert.focus, extra);
    });
    if (routing) cards.push(card('router', 'router', 'active', 'choosing', 'Router', 'Task experts', 'Reading your message to decide which one to three subject-matter roles this worker needs. Role names only, never real people.'));
    const leadState = turn.status === 'synthesizing' ? ['active', 'drafting', 'synthesizing'] : turn.status === 'complete' ? ['positive', 'proposal drafted', 'done'] : turn.status === 'failed' ? ['danger', 'stopped', 'stopped'] : ['quiet', 'waiting for reviews', 'waiting'];
    cards.push(card('lead', `lead ${leadState[2]}`, leadState[0], leadState[1], 'Lead builder', 'Lead builder', turn.status === 'synthesizing' ? `Reading ${arr(turn.reports).length} reviews, resolving every missing or misused finding, and drafting the complete definition.` : 'Receives every review as untrusted advice, reconciles conflicts, and writes the only proposal.'));
    return `${assemblyStagesMarkup(turn)}<ul class="assembly-list">${cards.join('')}</ul>`;
  }

  function builderTurnMarkup(turn) {
    if (!turn) return '';
    const experts = arr(turn.experts);
    const platform = experts.filter((expert) => expert.kind === 'platform').length;
    const domain = experts.filter((expert) => expert.kind === 'domain').length;
    const tone = turn.status === 'complete' ? 'positive' : turn.status === 'failed' ? 'danger' : 'active';
    const roster = experts.length ? `router → ${platform} Bench reviewers + ${domain} task ${domain === 1 ? 'expert' : 'experts'} → lead synthesis` : 'router → Bench reviewers + task experts → lead synthesis';
    const progress = turn.status === 'reviewing' ? ` · ${arr(turn.reports).length}/${experts.length} reviews in` : '';
    const failures = arr(turn.failures).length ? `<ul class="turn-failures">${turn.failures.map((item) => `<li>${esc(item)}</li>`).join('')}</ul>` : '';
    return `<div class="turn-pipeline"><span class="status-chip ${tone}">turn ${turn.number} · ${esc(turn.status)}${progress}</span><span>${roster}</span>${turn.synthesisSession ? `<code title="Ask session under var/ask">${esc(turn.synthesisSession)}</code>` : turn.routerSession ? `<code title="Ask session under var/ask">${esc(turn.routerSession)}</code>` : ''}${failures}</div>`;
  }

  // watchBuilder polls the persisted turn while it runs on the server and
  // re-renders only the builder section, so the rest of the page is untouched.
  function watchBuilder(scope) {
    clearInterval(state.builderTimer);
    state.builderTimer = setInterval(async () => {
      try {
        const data = await api(`/api/builder?worker=${enc(scope)}`);
        const el = view.querySelector(`[data-builder-worker="${scope}"]`);
        if (!el) {
          clearInterval(state.builderTimer);
          state.builderTimer = null;
          return;
        }
        const latest = arr(data.session?.turns).at(-1);
        el.outerHTML = builderMarkup(data, scope ? state.cache[scope] || null : null);
        if (!turnInProgress(latest)) {
          clearInterval(state.builderTimer);
          state.builderTimer = null;
          if (latest?.status === 'complete') showToast(`${plural(arr(data.session?.reports).length, 'specialist')} reviewed this proposal.`);
          else if (latest?.status === 'failed') showToast('The expert turn failed; the previous proposal was kept.', 'warning');
          const log = view.querySelector('.builder-log');
          log?.scrollTo({ top: log.scrollHeight });
        }
      } catch (error) {
        console.warn(error);
      }
    }, 2500);
  }

  function builderMarkup(data, worker = null) {
    const session = data?.session || null;
    const assistant = data?.assistant || {};
    const team = data?.team || {};
    const messages = arr(session?.messages);
    const latestTurn = arr(session?.turns).at(-1);
    const failed = latestTurn?.status === 'failed' ? latestTurn : null;
    const inProgress = turnInProgress(latestTurn);
    const scope = worker?.slug || '';
    const ready = Boolean(session?.ready && session?.proposal);
    const pending = inProgress
      ? `<div class="builder-message user"><span>You</span><p>${esc(latestTurn.message)}</p></div><div class="builder-message assistant pending"><span>Lead builder</span><p>${esc(builderProgressText(latestTurn))}</p></div>`
      : '';
    const conversation = messages.length || inProgress
      ? messages.map((message) => `<div class="builder-message ${message.role}"><span>${message.role === 'user' ? 'You' : message.role === 'note' ? 'Hire' : 'Lead builder'}</span><p>${esc(message.text)}</p></div>`).join('') + pending
      : `<div class="builder-welcome"><span class="assistant-mark">✦</span><div><p class="kicker">Expert agent builder</p><h2>What should this worker own?</h2><p>Describe the outcome in ordinary language. The lead builder assembles three permanent Bench reviewers, who read the platform contract and the live home as data, plus the right task experts for the job, then drafts a complete Hire-managed definition.</p></div></div>`;
    const manualEditingNote = worker ? 'The direct editor above remains available.' : 'Manual creation below remains available.';
    const disabledNote = assistant.ready
      ? (assistant.stale && !inProgress ? `<div class="inline-notice builder-readiness-notice"><div>${esc(assistant.message)}</div><div class="button-row builder-readiness-actions"><button class="secondary-button small" type="button" data-action="builder-reset">Start over</button></div></div>` : '')
      : `<div class="inline-notice warning builder-readiness-notice"><div>${esc(assistant.message || 'The expert team needs a model.')} <strong>${manualEditingNote}</strong></div><div class="button-row builder-readiness-actions"><a class="text-action" href="/setup" data-link>Choose a model in Setup →</a></div></div>`;
    return `<section class="agent-builder" data-builder-worker="${esc(scope)}">
      <div class="builder-head"><div><p class="eyebrow">Design room</p><h1>${worker ? `Refine ${esc(worker.name)} with an expert team.` : 'Describe the worker. An expert team designs it.'}</h1><p class="lede">Every message is reviewed by Bench architecture, evidence, and authority specialists plus one to three experts selected for this job.</p></div><span class="status-chip ${ready ? 'positive' : 'warning'}">${ready ? 'proposal ready' : 'draft only'}</span></div>
      ${disabledNote}
      <div class="builder-grid"><div class="builder-conversation"><div class="builder-log" role="log" aria-live="polite">${conversation}${failed ? `<div class="inline-notice danger"><strong>The latest expert turn failed.</strong> ${esc(failed.error || 'The prior proposal was kept unchanged.')}</div>` : ''}</div>${builderTurnMarkup(latestTurn)}
        <form data-form="builder-chat"><input type="hidden" name="workerSlug" value="${esc(scope)}"><div class="field"><label for="builder-message-${esc(scope || 'new')}">${messages.length ? 'Refine the proposal or answer the question' : 'Describe the outcome, inputs, evidence, and boundaries'}</label><textarea id="builder-message-${esc(scope || 'new')}" name="message" required maxlength="16384" placeholder="Build a release notes worker that reviews merged pull requests, drafts a weekly note with citations to each PR, and stops for a person when product ownership is unclear."></textarea></div><div class="builder-send"><small>One send uses a router, 3 permanent Bench reviewers with Hire's platform contract and checklists, 1–3 task experts, and a lead synthesis pass. Nothing is saved until you apply.</small><button class="primary-button" type="submit" ${assistant.ready && !inProgress ? '' : 'disabled'}>${inProgress ? 'Team is working…' : assistant.ready ? 'Assemble team and draft' : 'Choose a model first'}</button></div></form></div>
        <aside class="builder-review"><section><div class="builder-section-head"><p class="kicker">${inProgress ? 'Team assembling' : 'Review team'}</p>${session && !inProgress ? `<button class="text-action" type="button" data-action="builder-reset">Start over</button>` : ''}</div>${inProgress ? assemblyBoardMarkup(latestTurn, team) : `${latestTurn ? assemblyStagesMarkup(latestTurn) : ''}${builderTeamMarkup(session, team)}`}</section><section><div class="builder-section-head"><p class="kicker">Exact proposal</p><span>${session?.ready ? 'Ready to apply' : 'Needs review'}</span></div>${builderProposalMarkup(session?.proposal)}${arr(session?.changes).length ? `<div class="proposal-changes"><strong>Latest changes</strong><ul>${session.changes.map((change) => `<li>${esc(change)}</li>`).join('')}</ul></div>` : ''}${session?.proposal ? `<button class="primary-button full" type="button" data-action="builder-apply" ${ready && !inProgress && !assistant.stale ? '' : 'disabled'}>${worker ? `Apply changes to ${esc(worker.name)}` : 'Create worker from this proposal'}</button>${!session?.ready && !inProgress ? `<div class="inline-notice warning apply-locked"><strong>Apply is locked</strong> because the lead builder ended its last turn with a question and did not mark the proposal ready. Answer it in the conversation, or <button class="text-action" type="button" data-action="builder-accept-assumptions">tell the team to proceed with its stated assumptions</button> (one more review turn).</div>` : ''}<small class="apply-boundary">Applies these exact files and checks, then runs <code>agent check</code>. It does not schedule, run, approve, or prove business quality.</small>` : ''}</section></aside></div>
    </section>`;
  }

  async function renderNew() {
    await refreshBootstrap();
    const model = state.bootstrap?.settings?.model || '';
    const readiness = state.bootstrap?.runtime?.model || {};
    const builder = await api('/api/builder');
    state.newStarted = Date.now();
    mount('New worker', `<section class="route-page">
      ${builderMarkup(builder)}
      <details class="manual-create" data-mt="36"><summary><strong>Create manually</strong><span>Use the deterministic form without a model</span></summary>
      <div class="creation-grid">
        <div class="creation-panel">
          <p class="eyebrow">New worker · <span class="timer" id="new-timer">0:00</span></p>
          <h1>Describe the job. Hire builds the worker.</h1>
          <p class="lede">A name and a job description are enough. Deploy writes the agent home, proves it with <code>agent check</code>, and opens the inbox.</p>
          <form class="creation-form" data-form="create-worker">
            <div id="create-error" class="form-error" hidden></div>
            <div class="field"><label for="w-name">Name</label><input id="w-name" name="name" required maxlength="120" placeholder="Release notes clerk" autocomplete="off"></div>
            <div class="field"><label for="w-purpose">What this worker does</label><textarea id="w-purpose" name="purpose" class="tall" required maxlength="8192" placeholder="Turn merged pull requests into a weekly release note in work/release-notes/, in our house style. Keep a list of products and owners in state/kv/."></textarea><small>This becomes GOAL.md and AGENTS.md. Say what done looks like and what must never change.</small></div>
            <div class="field-group">
              <strong>Routine (optional)</strong>
              <p>Something the worker should do on a schedule. Each occurrence becomes one request with its own result.</p>
              <div class="field"><label for="w-routine">Instructions</label><textarea id="w-routine" name="routineInstructions" placeholder="Every weekday morning, summarise yesterday's merged pull requests into work/daily/YYYY-MM-DD.md."></textarea></div>
              <div class="three-field-grid">
                <div class="field"><label for="w-every">Cadence</label><select id="w-every" name="every">${CADENCES.map(([v, l]) => `<option value="${v}">${l}</option>`).join('')}</select></div>
                <div class="field"><label for="w-at">Time of day</label><input id="w-at" name="at" type="time" value="09:00"></div>
                <div class="field"><label for="w-weekday">Weekday</label><select id="w-weekday" name="weekday">${WEEKDAYS.map((d, i) => `<option value="${i}" ${i === 1 ? 'selected' : ''}>${d}</option>`).join('')}</select></div>
              </div>
              <div class="field" data-mt="13"><label for="w-rcheck">How to verify each occurrence (optional shell, runs from work/)</label><input id="w-rcheck" name="routineCheck" placeholder="test -s daily/$(date +%F).md" autocomplete="off"></div>
            </div>
            <div class="two-field-grid">
              <div class="field"><label for="w-model">Model</label><input id="w-model" name="model" value="${esc(model)}" placeholder="openai-codex/gpt-5.6-sol" list="provider-hints"><datalist id="provider-hints"><option value="openai-codex/gpt-5.6-sol"><option value="openai-codex/gpt-5.4-mini"><option value="anthropic/"><option value="openai/"><option value="gemini/"><option value="openrouter/"></datalist><small>Providers ask knows: anthropic, openai, openai-codex, gemini, openrouter. ${['ready', 'unproved'].includes(readiness.state) ? 'Hire confirms the model with one small call the first time it is used.' : esc(readiness.message || 'Choose a model in Setup.')}</small></div>
              <div class="field"><label>Boundary</label><div class="field check"><input id="w-net" name="network" type="checkbox"><label for="w-net">Allow network access inside Cage</label></div><small>Writes are always confined to work/ and state/. External effects stay proposals.</small></div>
            </div>
            <div class="form-action-row"><span>Deploy runs <code>agent new</code>, <code>agent check</code>, and <code>agent show</code>. No model call.</span><button class="primary-button" type="submit">Deploy worker</button></div>
          </form>
        </div>
        <aside class="method-panel">
          <div class="method-head"><span>5m</span><div><p class="kicker">The five-minute path</p><h2>Describe → Deploy → Give it work</h2></div></div>
          <ol>
            <li><span>01</span><div><strong>Describe</strong><small>Your words become GOAL.md and AGENTS.md, readable and editable later.</small></div></li>
            <li><span>02</span><div><strong>Deploy</strong><small>agent new scaffolds the home; Hire adds a request-aware bin/check; agent check proves it.</small></div></li>
            <li><span>03</span><div><strong>Give it work</strong><small>A request becomes actions with times, each a durable tend job with a checkpoint.</small></div></li>
            <li><span>04</span><div><strong>Watch</strong><small>Every attempt keeps its stdout, typescript, and RESULT.md. Unknown effects wait for you.</small></div></li>
          </ol>
          <div class="method-dark"><strong>What you get on disk</strong><p>An agent home the CLI understands as-is.</p><code>agent show var/workers/&lt;slug&gt;<br>agent history var/workers/&lt;slug&gt;<br>tend list</code></div>
        </aside>
      </div></details>
    </section>`, 'new');
    if (turnInProgress(arr(builder.session?.turns).at(-1))) watchBuilder('');
    const timer = document.querySelector('#new-timer');
    const every = document.querySelector('#w-every');
    const syncCadence = () => {
      const v = every.value;
      document.querySelector('#w-at').closest('.field').hidden = !(v === 'daily' || v === 'weekdays' || v === 'weekly');
      document.querySelector('#w-weekday').closest('.field').hidden = v !== 'weekly';
    };
    every.addEventListener('change', syncCadence);
    syncCadence();
    stopPolling();
    state.pollTimer = setInterval(() => {
      const s = Math.floor((Date.now() - state.newStarted) / 1000);
      timer.textContent = `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
    }, 1000);
  }

  async function submitCreate(form) {
    const data = new FormData(form);
    const body = {
      name: String(data.get('name') || '').trim(),
      purpose: String(data.get('purpose') || '').trim(),
      model: String(data.get('model') || '').trim(),
      network: data.get('network') === 'on',
    };
    const routine = String(data.get('routineInstructions') || '').trim();
    if (routine && data.get('every')) {
      body.routine = { title: '', instructions: routine, every: String(data.get('every')), at: String(data.get('at') || '09:00'), weekday: Number(data.get('weekday') || 1), check: String(data.get('routineCheck') || '').trim() };
    }
    const error = document.querySelector('#create-error');
    const button = form.querySelector('button[type="submit"]');
    button.disabled = true;
    button.textContent = 'Deploying…';
    try {
      const result = await api('/api/workers', { method: 'POST', body });
      const elapsed = Math.round((Date.now() - state.newStarted) / 1000);
      const w = result.worker;
      const receipt = w.receipt || {};
      form.hidden = true;
      form.insertAdjacentHTML('afterend', `<div class="deploy-receipt" data-mt="36">
        <span>OK</span>
        <div><p class="kicker">Deployed in ${Math.floor(elapsed / 60)}:${String(elapsed % 60).padStart(2, '0')}</p><h2>${esc(w.name)} is ready for work.</h2>
        <p>${esc(receipt.message || 'agent check accepted the home')}${w.checkState !== 'valid' ? ' — fix the definition before giving it work.' : ''}</p>
        <code>definition ${esc(receipt.definitionSha256 || '—')}</code><code>check ${esc(receipt.checkSha256 || '—')}</code><code>${esc(w.home)}</code>
        <div class="button-row" data-mt="16"><a class="primary-button" href="/workers/${enc(w.slug)}" data-link>Give it work</a><a class="secondary-button" href="/new" data-link>Create another</a></div></div>
      </div>`);
      stopPolling();
      await refreshBootstrap();
      showToast(`${w.name} deployed.`);
    } catch (err) {
      error.textContent = err.message + (err.nextAction ? ` ${err.nextAction}` : '');
      error.hidden = false;
      button.disabled = false;
      button.textContent = 'Deploy worker';
    }
  }

  // Worker page --------------------------------------------------------------

  async function loadWorker(slug) {
    const w = await api(`/api/workers/${enc(slug)}`);
    state.cache[slug] = w;
    return w;
  }

  async function renderWorker(slug, options = {}) {
    if (state.ui.slug !== slug) state.ui = { slug, tab: 'work', dir: 'work', file: '', definition: 'GOAL.md', dirty: false, manualDirty: false, editRoutine: '', workerChecks: [], checkSuggestions: [], checkSuggestionNote: '' };
    const w = await loadWorker(slug);
    if (!state.bootstrap) await refreshBootstrap();
    const model = state.bootstrap?.runtime?.model || {};
    const cls = w.checkState !== 'valid' ? 'invalid' : !w.enabled ? 'paused' : '';
    const label = w.checkState !== 'valid' ? 'Check failed' : w.enabled ? 'Deployed' : 'Paused';
    const receipt = w.receipt || {};
    const tabs = [['work', 'Work', w.requests.length], ['routines', 'Routines', w.routines.length], ['files', 'Files', null], ['definition', 'Edit worker', null], ['history', 'History', null]];
    const html = `<section class="route-page">
      <nav class="breadcrumbs"><a href="/workers" data-link>Workers</a><span>/</span><span>${esc(w.name)}</span></nav>
      <div class="worker-head">
        <span class="agent-glyph large ${cls}">${esc(initials(w.name))}</span>
        <div><div class="chip-row">${stateLabel(label)}<span class="status-chip quiet">${esc(w.model || 'no model')}</span><span class="status-chip ${w.network ? 'active' : 'quiet'}">${w.network ? 'network allowed' : 'network off'}</span>${w.attention ? `<span class="status-chip danger">${w.attention} needs you</span>` : ''}</div>
          <h1>${esc(w.name)}</h1><p class="lede">${esc(w.purpose)}</p>
          ${w.checkState !== 'valid' ? `<div class="inline-notice danger" data-mt="14">${esc(w.checkMessage || 'agent check rejects this home.')} Edit the definition and save to check again.</div>` : ''}</div>
        <div class="worker-head-actions">
          <div class="button-row"><button class="primary-button" data-action="edit-worker">Edit worker</button><button class="secondary-button" data-action="toggle-enabled">${w.enabled ? 'Pause intake' : 'Resume intake'}</button></div>
          <form data-form="worker-model" class="model-form"><input name="model" value="${esc(w.model || '')}" placeholder="provider/model" aria-label="Model for new requests"><button class="secondary-button small" type="submit">Change model</button></form>
          <button class="danger-button small" data-action="retire">Retire worker</button>
        </div>
      </div>
      <dl class="receipt-strip">
        <div><dt>Home</dt><dd>${esc(w.home)}</dd></div>
        <div><dt>Definition digest</dt><dd>${esc((receipt.definitionSha256 || '—').slice(0, 24))}${receipt.definitionSha256 ? '…' : ''}</dd></div>
        <div><dt>Check digest</dt><dd>${esc((receipt.checkSha256 || '—').slice(0, 24))}${receipt.checkSha256 ? '…' : ''}</dd></div>
        <div><dt>Authority</dt><dd>${esc(receipt.authority || 'Cage writes work+state')}${w.network ? ' · network allowed' : ''}</dd></div>
      </dl>
      <section class="composer">
        <div class="composer-head"><span class="mini-mark">→</span><div><p class="kicker">Give it work</p><h2>What should ${esc(w.name)} do?</h2></div></div>
        <form data-form="intake">
          <div class="field"><textarea name="text" required maxlength="65536" placeholder="Draft the release note for this week from work/prs.json, then next Monday at 9 send me a checklist of what still needs an owner."></textarea></div>
          <div class="field" data-mt="10"><input name="check" placeholder="How to verify (optional shell, runs from work/): test -s release-notes/this-week.md" autocomplete="off"></div>
          <div class="composer-actions">
            <div class="options">
              <label><input type="checkbox" name="plan" ${['ready', 'unproved'].includes(model.state) ? 'checked' : 'disabled'}> Plan with the model${['ready', 'unproved'].includes(model.state) ? '' : ' (no model)'}</label>
              <label>Start no earlier than <input type="datetime-local" name="notBefore"></label>
            </div>
            <button class="primary-button" type="submit" ${w.enabled && w.checkState === 'valid' ? '' : 'disabled'}>Send to worker</button>
          </div>
        </form>
        <small>Planning is one schema-bound <code>ask</code> call that splits timing and repeats into actions. Without it, the request runs as one action now. Every action is a durable <code>tend</code> job with its own checkpoint.</small>
      </section>
      <div class="tabs" role="tablist">${tabs.map(([id, name, count]) => `<button role="tab" data-tab="${id}" class="${state.ui.tab === id ? 'active' : ''}">${name}${count !== null ? `<em>${count}</em>` : ''}</button>`).join('')}</div>
      <div class="tab-panel" id="tab-panel"></div>
    </section>`;
    mount(w.name, html, 'workers');
    await renderTab(w);
    if (!options.noPoll) poll(() => refreshWorker(slug), 5000);
  }

  // refreshWorker updates the live parts of the page without touching the
  // composer, so a half-written request survives the poll.
  async function refreshWorker(slug) {
    const w = await loadWorker(slug);
    const tabs = document.querySelectorAll('.tabs button[data-tab]');
    tabs.forEach((b) => {
      const em = b.querySelector('em');
      if (!em) return;
      if (b.dataset.tab === 'work') em.textContent = String(w.requests.length);
      if (b.dataset.tab === 'routines') em.textContent = String(w.routines.length);
    });
    const chips = document.querySelector('.worker-head .chip-row');
    if (chips) {
      const label = w.checkState !== 'valid' ? 'Check failed' : w.enabled ? 'Deployed' : 'Paused';
      chips.innerHTML = `${stateLabel(label)}<span class="status-chip quiet">${esc(w.model || 'no model')}</span><span class="status-chip ${w.network ? 'active' : 'quiet'}">${w.network ? 'network allowed' : 'network off'}</span>${w.attention ? `<span class="status-chip danger">${w.attention} needs you</span>` : ''}`;
    }
    if (state.ui.tab === 'work' || state.ui.tab === 'routines') await renderTab(w);
    renderSidebar();
    try { await refreshBootstrap(); } catch { /* sidebar counts refresh on the next tick */ }
  }

  async function renderTab(w) {
    const panel = document.querySelector('#tab-panel');
    if (!panel) return;
    switch (state.ui.tab) {
      case 'work': panel.innerHTML = renderWorkTab(w); break;
      case 'routines': panel.innerHTML = renderRoutinesTab(w); break;
      case 'files': await renderFilesTab(w, panel); break;
      case 'definition': await renderDefinitionTab(w, panel); break;
      case 'history': await renderHistoryTab(w, panel); break;
    }
  }

  function renderWorkTab(w) {
    const requests = arr(w.requests);
    if (!requests.length) return `<div class="empty-state small"><span>→</span><h2>No work yet</h2><p>Send the first request above. It becomes a durable job the moment you press Send.</p></div>`;
    const attention = requests.filter((r) => ['unknown', 'unfinished', 'broken', 'failed', 'timed-out', 'boundary'].includes(r.state));
    return `${attention.length ? `<div class="boundary-note"><span>!</span><div><strong>${plural(attention.length, 'request')} need${attention.length === 1 ? 's' : ''} a person</strong><p>Open one to continue, retry, or resolve it. Nothing here is retried on its own.</p></div></div>` : ''}
      <div class="record-list">${requests.map((r) => requestRow({ slug: w.slug }, r)).join('')}</div>`;
  }

  function renderRoutinesTab(w) {
    const routines = arr(w.routines);
    return `<div class="file-layout routine-layout">
      <div class="record-list">${routines.length ? routines.map((r) => (r.id === state.ui.editRoutine ? routineEditForm(r) : `<article class="routine-card ${r.enabled ? '' : 'off'}">
        <div><div class="chip-row"><span class="status-chip ${r.enabled ? 'positive' : 'quiet'}">${r.enabled ? 'on' : 'paused'}</span><span class="status-chip quiet">${esc(r.cadence)}</span></div><h3>${esc(r.title)}</h3><p>${esc(r.instructions)}</p>
          <small class="mono-label">next ${esc(formatDate(r.nextDue))} (${esc(relative(r.nextDue))})${r.lastRequest ? ` · last <a href="/workers/${enc(w.slug)}/requests/${enc(r.lastRequest)}" data-link>${esc(r.lastRequest)}</a>` : ''}${r.check ? ` · check: <code>${esc(r.check)}</code>` : ''}</small></div>
        <div class="actions"><button class="secondary-button small" data-action="edit-routine" data-id="${esc(r.id)}">Edit</button><button class="secondary-button small" data-action="routine" data-id="${esc(r.id)}" data-do="run">Run now</button><button class="secondary-button small" data-action="routine" data-id="${esc(r.id)}" data-do="${r.enabled ? 'disable' : 'enable'}">${r.enabled ? 'Pause' : 'Resume'}</button><button class="danger-button small" data-action="routine" data-id="${esc(r.id)}" data-do="delete">Delete</button></div>
      </article>`)).join('') : `<div class="empty-state small"><span>⏱</span><h2>No routines</h2><p>A routine queues one request per occurrence. Add one on the right, or ask for something recurring in the composer.</p></div>`}</div>
      <form class="side-form" data-form="routine"><h3>Add a routine</h3><p>Hire is the clock; each occurrence is an ordinary request with its own RESULT.md.</p>
        <div class="field"><label>Instructions</label><textarea name="instructions" required></textarea></div>
        <div class="field"><label>Cadence</label><select name="every">${CADENCES.slice(1).map(([v, l]) => `<option value="${v}" ${v === 'daily' ? 'selected' : ''}>${l}</option>`).join('')}</select></div>
        <div class="two-field-grid"><div class="field"><label>Time</label><input name="at" type="time" value="09:00"></div><div class="field"><label>Weekday</label><select name="weekday">${WEEKDAYS.map((d, i) => `<option value="${i}" ${i === 1 ? 'selected' : ''}>${d}</option>`).join('')}</select></div></div>
        <div class="field"><label>Check (optional shell from work/)</label><input name="check" autocomplete="off"></div>
        <div class="form-action-row"><span></span><button class="primary-button" type="submit">Add routine</button></div>
      </form></div>`;
  }

  function cadenceOptions(selected) {
    const options = CADENCES.slice(1).map(([v, l]) => [v, l]);
    if (selected && !options.some(([v]) => v === selected)) options.push([selected, `Every ${selected}`]);
    return options.map(([v, l]) => `<option value="${esc(v)}" ${v === selected ? 'selected' : ''}>${esc(l)}</option>`).join('');
  }

  function routineEditForm(r) {
    return `<form class="routine-card" data-form="routine-update" data-id="${esc(r.id)}"><div>
      <p class="kicker">Editing routine</p>
      <div class="field" data-mt="8"><label>Instructions</label><textarea name="instructions" required>${esc(r.instructions)}</textarea></div>
      <div class="three-field-grid"><div class="field"><label>Cadence</label><select name="every">${cadenceOptions(r.every)}</select></div><div class="field"><label>Time</label><input name="at" type="time" value="${esc(r.at || '09:00')}"></div><div class="field"><label>Weekday</label><select name="weekday">${WEEKDAYS.map((d, i) => `<option value="${i}" ${i === r.weekday ? 'selected' : ''}>${d}</option>`).join('')}</select></div></div>
      <div class="field" data-mt="13"><label>Check (optional shell from work/)</label><input name="check" value="${esc(r.check || '')}" autocomplete="off"></div>
      <div class="button-row" data-mt="12"><button class="primary-button" type="submit">Save routine</button><button class="secondary-button" type="button" data-action="cancel-edit-routine">Cancel</button></div>
    </div></form>`;
  }

  async function renderFilesTab(w, panel) {
    const dir = state.ui.dir || 'work';
    let listing;
    try { listing = await api(`/api/workers/${enc(w.slug)}/files?path=${enc(dir)}`); } catch (error) { listing = { entries: [], error: error.message }; }
    let file = null;
    if (state.ui.file) {
      try { file = await api(`/api/workers/${enc(w.slug)}/files?path=${enc(state.ui.file)}`); } catch (error) { file = { path: state.ui.file, error: error.message }; }
    }
    const crumbs = dir.split('/').filter(Boolean);
    const roots = ['work', 'state', 'tools', 'skills'];
    panel.innerHTML = `<div class="file-layout">
      <div class="file-tree">
        <div class="file-crumbs">${roots.map((r) => `<button data-action="cd" data-path="${r}" ${dir.split('/')[0] === r ? 'data-tone="ink"' : ''}>${r}/</button>`).join(' ')}</div>
        <div class="file-crumbs">${crumbs.map((c, i) => `<button data-action="cd" data-path="${esc(crumbs.slice(0, i + 1).join('/'))}">${esc(c)}</button>`).join('<span>/</span>')}</div>
        ${listing.error ? `<p class="mono-label">${esc(listing.error)}</p>` : ''}
        <ul>${dir.includes('/') ? `<li><button data-action="cd" data-path="${esc(crumbs.slice(0, -1).join('/'))}"><span>↑</span><span>..</span><small></small></button></li>` : ''}
        ${arr(listing.entries).map((e) => `<li><button data-action="${e.dir ? 'cd' : 'open'}" data-path="${esc(e.path)}" class="${state.ui.file === e.path ? 'active' : ''}"><span>${e.dir ? '▸' : '·'}</span><span>${esc(e.name)}${e.dir ? '/' : ''}</span><small>${e.dir ? '' : `${e.size} B`}</small></button></li>`).join('')}
        ${!arr(listing.entries).length && !listing.error ? '<li><span class="mono-label">empty</span></li>' : ''}</ul>
        <form data-form="new-file" data-mt="12"><div class="field"><input name="path" placeholder="${esc(dir)}/notes.md" autocomplete="off"></div><div class="button-row" data-mt="8"><button class="secondary-button small" type="submit">New file</button></div></form>
      </div>
      <div class="file-view">${file ? (file.error ? `<p class="mono-label">${esc(file.error)}</p>` : `<div class="file-view-head"><code>${esc(file.path)}</code><div class="actions">${file.writable ? `<button class="secondary-button small" data-action="save-file">Save</button><button class="danger-button small" data-action="delete-file">Delete</button>` : '<span class="status-chip quiet">read only</span>'}</div></div>
        ${file.binary ? `<p class="mono-label">Binary file, ${file.size} bytes.</p>` : `<textarea id="file-editor" ${file.writable ? '' : 'readonly'}>${esc(file.content)}</textarea>${file.truncated ? '<p class="mono-label">Showing the first 512 KiB.</p>' : ''}`}`) : `<p class="mono-label">Pick a file. <code>work/</code> holds deliverables and <code>state/kv/</code> holds facts the worker keeps; both are writable here and by the worker. Everything else is read only.</p>`}</div>
    </div>`;
    const editor = document.querySelector('#file-editor');
    if (editor) editor.addEventListener('input', () => { state.ui.dirty = true; });
  }

  async function renderDefinitionTab(w, panel) {
    const [def, builder] = await Promise.all([
      api(`/api/workers/${enc(w.slug)}/definition`),
      api(`/api/builder?worker=${enc(w.slug)}`),
    ]);
    const names = ['GOAL.md', 'AGENTS.md', 'SOUL.md', 'PLAN.md', 'MEMORY.md', 'HEARTBEAT.md'];
    if (!(state.ui.definition in def.files)) state.ui.definition = 'GOAL.md';
    const current = state.ui.definition;
    state.ui.workerChecks = arr(def.checks);
    const checks = state.ui.workerChecks;
    const suggestions = arr(state.ui.checkSuggestions);
    const checkCard = (check, index, suggested = false) => {
      const detail = check.kind === 'file_nonempty'
        ? `Require work/${check.path} to exist and contain something.`
        : check.kind === 'text_contains'
          ? `Require work/${check.path} to contain the exact text “${check.text}”.`
          : `Require work/${check.path} to contain at least ${check.minimumBytes} bytes.`;
      return `<article class="worker-check-card ${suggested ? 'suggested' : ''}"><div><div class="chip-row"><span class="status-chip ${suggested ? 'neutral' : 'positive'}">${suggested ? 'AI suggestion' : 'active check'}</span><span class="status-chip quiet">${esc(check.kind.replaceAll('_', ' '))}</span></div><h4>${esc(check.description)}</h4><p>${esc(detail)}</p></div><button class="${suggested ? 'secondary-button' : 'danger-button'} small" type="button" data-action="${suggested ? 'dismiss-check-suggestion' : 'remove-worker-check'}" data-index="${index}">${suggested ? 'Dismiss' : 'Remove'}</button></article>`;
    };
    const suggestionPanel = suggestions.length || state.ui.checkSuggestionNote
      ? `<div class="check-suggestions"><div class="check-suggestion-head"><div><p class="kicker">Review before applying</p><h3>${plural(suggestions.length, 'suggested check')}</h3><p>${esc(state.ui.checkSuggestionNote || 'The assistant found mechanically testable conditions in this worker definition.')}</p></div><span class="status-chip neutral">${esc(def.checkAssistant?.model || w.model || 'model')}</span></div>${suggestions.map((check, index) => checkCard(check, index, true)).join('')}${suggestions.length ? `<form data-form="apply-check-suggestions"><button class="primary-button" type="submit">Add ${plural(suggestions.length, 'check')}</button><small>Nothing changes until you add them. Hire validates and compiles the structured checks; the model never writes shell.</small></form>` : ''}</div>`
      : '';
    const expertBuilderOpen = Boolean(builder?.session);
    const expertBuilderStatus = !builder?.assistant?.ready ? 'Model setup needed' : builder?.assistant?.stale ? 'Draft continues from current files' : 'Ready';
    panel.innerHTML = `<section class="direct-editor"><div class="section-heading"><div><p class="eyebrow">Edit worker</p><h2>Edit ${esc(w.name)} directly.</h2></div><span>Editable now</span></div><div class="inline-notice positive">The worker reads <strong>GOAL.md</strong> (what done looks like) and <strong>AGENTS.md</strong> (how to work) on every run. Edit them here; saving runs <code>agent check</code>. The name and summary on the right are what people see on the card.</div><div class="file-layout">
      <div class="file-tree"><p class="kicker" data-tone="muted">Definition files</p><div class="definition-list">${names.map((n) => `<button data-action="pick-definition" data-name="${n}" class="${n === current ? 'active' : ''}">${n}<small>${n in def.files ? `${def.files[n].length} B` : 'absent'}</small></button>`).join('')}</div>
        <p class="mono-label" data-mt="14">Saving runs <code>agent check</code> again. A rejected home pauses intake until it passes.</p>
        <details data-mt="14"><summary class="text-action">bin/check (read only)</summary><pre class="output short" data-mt="8">${esc(def.check)}</pre></details>
        <details data-mt="10"><summary class="text-action">agent show</summary><pre class="output" data-mt="8">${esc(def.show)}</pre></details></div>
      <div class="file-view"><div class="file-view-head"><code>${esc(current)}</code><div class="actions"><button class="primary-button small" data-action="save-definition">Save and check</button></div></div>
        <textarea id="definition-editor">${esc(def.files[current] ?? '')}</textarea>
        <form data-form="worker-update" class="side-form"><h3>Card name and summary</h3><p>Shown on the worker card and page head; the model does not read these.</p>
          <div class="field"><label>Name</label><input name="name" value="${esc(w.name)}" maxlength="120" required></div>
          <div class="field"><label>Summary</label><textarea name="purpose" maxlength="8192" required>${esc(w.purpose)}</textarea></div>
          <div class="field check" data-mt="12"><input id="d-net" name="network" type="checkbox" ${w.network ? 'checked' : ''}><label for="d-net">Allow network access inside Cage</label></div>
          <div class="button-row" data-mt="12"><button class="secondary-button small" type="submit">Save name and summary</button></div></form></div>
    </div></section>${builder?.revertable ? `<div class="inline-notice warning revert-notice"><div><strong>The expert team's proposal was applied ${esc(relative(builder.revertable.appliedAt))}</strong> (${esc(builder.revertable.previousName)} → ${esc(builder.revertable.appliedName)}). If the worker now stops or refuses work it used to do, restore the definition it had before that apply. Nothing else has changed since, so the revert is exact.</div><button class="secondary-button small" type="button" data-action="builder-revert">Revert to the previous definition</button></div>` : ''}<details class="expert-builder-disclosure"${expertBuilderOpen ? ' open' : ''}><summary><span><strong>Refine with an expert team</strong><small>Optional AI-assisted rewrite</small></span><span class="status-chip ${builder?.assistant?.ready ? 'positive' : 'warning'}">${esc(expertBuilderStatus)}</span></summary>${builderMarkup(builder, w)}</details><section class="acceptance-builder"><div class="section-heading"><div><p class="eyebrow">Definition of done</p><h2>What can Hire verify?</h2><p>Every request already needs a non-empty <code>RESULT.md</code>. Add stable evidence this worker should always produce.</p></div><span>${plural(checks.length, 'extra check')}</span></div><div class="check-builder-grid"><div><div class="worker-check-list">${checks.length ? checks.map((check, index) => checkCard(check, index)).join('') : '<div class="empty-checks"><strong>No extra checks yet</strong><p>A result file is still required. Ask the assistant to find stronger mechanical evidence in the worker definition.</p></div>'}</div>${suggestionPanel}</div><aside class="check-assistant"><span class="assistant-mark" aria-hidden="true">✦</span><p class="kicker">AI-assisted checks</p><h3>Describe evidence, not shell.</h3><p>The assistant reads the saved GOAL.md and AGENTS.md, then suggests only structured checks that Hire knows how to compile.</p><form data-form="check-assistant"><div class="field"><label for="check-guidance">Anything else it should consider? <span class="optional-mark">Optional</span></label><textarea id="check-guidance" name="guidance" maxlength="8192" placeholder="A weekly note should always include the heading Release notes and save a non-empty file at release-notes/latest.md."></textarea></div><button class="primary-button full" type="submit" ${def.checkAssistant?.ready ? '' : 'disabled'}>${def.checkAssistant?.ready ? 'Suggest checks' : 'Set a model first'}</button>${def.checkAssistant?.ready ? `<small>Uses one schema-bound call to ${esc(def.checkAssistant.model)}. Suggestions are not applied automatically.</small>` : `<small>This worker has no model yet. Choose one with Change model; you can still add a structured check below.</small>`}</form><details><summary>Add a structured check yourself</summary><form data-form="manual-worker-check"><div class="field"><label>Condition</label><select name="kind"><option value="file_nonempty">A file exists and is not empty</option><option value="text_contains">A file contains exact text</option><option value="minimum_bytes">A file has a minimum size</option></select></div><div class="field"><label>File under work/</label><input name="path" required placeholder="release-notes/latest.md"></div><div class="field"><label>Why this proves progress</label><input name="description" required maxlength="240" placeholder="The latest release note was written"></div><div class="field"><label>Exact text <span class="optional-mark">For contains-text only</span></label><input name="text" maxlength="512" placeholder="Release notes"></div><div class="field"><label>Minimum bytes <span class="optional-mark">For size only</span></label><input name="minimumBytes" type="number" min="1" max="104857600" value="100"></div><button class="secondary-button full" type="submit">Add check</button></form></details></aside></div></section>`;
    if (turnInProgress(arr(builder?.session?.turns).at(-1))) watchBuilder(w.slug);
    document.querySelector('#definition-editor').addEventListener('input', () => { state.ui.dirty = true; });
    document.querySelectorAll('form[data-form="worker-update"] input, form[data-form="worker-update"] textarea, form[data-form="manual-worker-check"] input, form[data-form="manual-worker-check"] textarea, form[data-form="manual-worker-check"] select').forEach((field) => field.addEventListener('input', () => { state.ui.manualDirty = true; }));
  }

  async function renderHistoryTab(w, panel) {
    try {
      const data = await api(`/api/workers/${enc(w.slug)}/history`);
      const entries = arr(data.entries);
      panel.innerHTML = entries.length ? `<div class="component-table"><table><thead><tr><th>Session</th><th>Detail</th></tr></thead><tbody>${entries.slice().reverse().map((e) => `<tr><th>${esc(e.session || e.path || e.id || e.line || '')}</th><td><code>${esc(JSON.stringify(e).slice(0, 400))}</code></td></tr>`).join('')}</tbody></table></div><p class="mono-label" data-mt="12">From <code>agent history ${esc(w.home)} ls</code>; replay with <code>agent history … check</code>.</p>` : `<div class="empty-state small"><span>◷</span><h2>No runs recorded yet</h2><p>Each run leaves a replayable Ask session under .agent/runs/.</p></div>`;
    } catch (error) {
      panel.innerHTML = `<div class="inline-notice warning">${esc(error.message)}</div>`;
    }
  }

  // Request page -------------------------------------------------------------

  async function renderRequest(slug, id, options = {}) {
    const data = await api(`/api/workers/${enc(slug)}/requests/${enc(id)}`);
    const r = data.request;
    const w = data.worker;
    const job = data.job;
    const attempts = arr(data.attempts);
    const children = arr(data.children);
    const plan = data.plan;
    const actions = [];
    if (['queued', 'scheduled'].includes(r.state)) actions.push(`<button class="danger-button small" data-action="request" data-do="cancel">Cancel</button>`);
    if (r.state === 'unfinished') actions.push(`<button class="primary-button" data-action="request" data-do="retry">Continue (same checkpoint)</button>`);
    if (['broken', 'failed', 'timed-out', 'boundary'].includes(r.state)) actions.push(`<button class="primary-button" data-action="request" data-do="retry">Retry (same checkpoint)</button>`);
    if (r.state === 'unknown') actions.push(`<button class="secondary-button small" data-action="request" data-do="resolve" data-decision="retry">Resolve: retry</button><button class="secondary-button small" data-action="request" data-do="resolve" data-decision="done">Resolve: done</button><button class="danger-button small" data-action="request" data-do="resolve" data-decision="fail">Resolve: fail</button>`);
    if (['done', 'cancelled', 'failed', 'broken', 'timed-out', 'boundary', 'unfinished'].includes(r.state) && r.runs) actions.push(`<button class="secondary-button small" data-action="request" data-do="rerun">Run again as a new request</button>`);
    const html = `<section class="route-page">
      <nav class="breadcrumbs"><a href="/workers" data-link>Workers</a><span>/</span><a href="/workers/${enc(w.slug)}" data-link>${esc(w.name)}</a><span>/</span><span>${esc(r.id)}</span></nav>
      <div class="page-head"><div><div class="chip-row">${stateChip(r.state)}<span class="status-chip quiet">${esc(r.kind)}</span><span class="status-chip quiet">${esc(r.source)}</span>${r.exit !== undefined && r.exit !== null ? `<span class="status-chip ${r.exit === 0 ? 'positive' : 'warning'}">exit ${r.exit}</span>` : ''}</div>
        <h1>${esc(r.title)}</h1><p class="lede">Created ${esc(formatDate(r.createdAt))}${r.state === 'scheduled' ? ` · runs ${esc(relative(r.notBefore))} (${esc(formatDate(r.notBefore))})` : ''} · ${plural(attempts.length, 'attempt')}${r.model ? ` · ${esc(r.model)}` : ''}</p></div>
        <div class="worker-head-actions"><div class="button-row">${actions.join('')}</div></div></div>
      ${r.state === 'unknown' ? `<div class="boundary-note"><span>!</span><div><strong>The attempt started but Tend never saw it finish.</strong><p>Look at the work tree, the typescript below, and any external effect before choosing. Retry re-runs the same command with the same checkpoint; done requires nothing further; fail records it as failed.</p></div></div>` : ''}
      ${r.state === 'unfinished' ? `<div class="inline-notice warning">The run stopped before <code>bin/check</code> accepted (exit 2). Continue re-runs the same command; the checkpoint carries the conversation forward.</div>` : ''}
      <div class="evidence-grid">
        <div class="evidence-card wide"><p class="kicker">Result</p><h3>work/requests/${esc(r.id)}/RESULT.md</h3>${data.result ? `<pre class="output">${esc(data.result.content)}</pre>` : '<p>Not written yet. The check accepts only when this file exists and is not empty.</p>'}</div>
        ${children.length ? `<div class="evidence-card wide"><p class="kicker">Plan</p><h3>${plural(children.length, 'action')}${plan?.fallback ? ' (no model; one action)' : plan?.model ? ` planned by ${esc(plan.model)}` : ''}</h3><div class="record-list">${children.map((c) => requestRow({ slug: w.slug }, c)).join('')}</div></div>` : ''}
        ${attempts.length ? `<div class="evidence-card wide"><p class="kicker">Attempts</p><h3>What Tend recorded</h3>${attempts.map((a) => `<div class="attempt"><header><strong>Attempt ${a.number}</strong><span>${esc(a.status)}</span>${a.exit !== undefined && a.exit !== null ? `<span>exit ${a.exit}</span>` : ''}<span>${esc(formatDate(a.startedAt))}${a.finishedAt && new Date(a.finishedAt).getFullYear() > 1971 ? ` → ${esc(formatDate(a.finishedAt))}` : ''}</span>${a.note ? `<span>${esc(a.note)}</span>` : ''}${a.truncated ? '<span class="status-chip warning">truncated</span>' : ''}</header>
          <details open><summary>Typescript (stderr)</summary><pre class="output dark">${esc(a.stderr || '(empty)')}</pre></details>
          <details><summary>Answer (stdout)</summary><pre class="output">${esc(a.stdout || '(empty)')}</pre></details></div>`).join('')}</div>` : ''}
        <div class="evidence-card"><p class="kicker">Request file</p><h3>REQUEST.md</h3><pre class="output short">${esc(data.requestFile)}</pre></div>
        <div class="evidence-card"><p class="kicker">Exact command</p><h3>What Tend runs</h3><pre class="output short">${esc(arr(data.argv).map((s) => (/\s/.test(s) ? `'${s.replaceAll("'", "'\\''")}'` : s)).join(' '))}</pre>${job ? `<p class="mono-label" data-mt="10">tend job ${esc(job.id)} · ${esc(job.status)} · cwd ${esc(job.cwd)}</p>` : '<p class="mono-label" data-mt="10">No Tend job: this request only groups its actions.</p>'}${r.check ? `<p class="mono-label">request check: <code>${esc(r.check)}</code></p>` : ''}</div>
      </div>
    </section>`;
    mount(r.title, html, 'workers');
    if (!options.noPoll && ['queued', 'scheduled', 'running', 'waiting'].includes(r.state)) poll(() => renderRequest(slug, id, { noPoll: true }), 4000);
  }

  // Setup --------------------------------------------------------------------

  async function renderSetup() {
    const data = await refreshBootstrap();
    const runtime = data.runtime;
    const model = runtime.model || {};
    const runner = data.runner || {};
    const modelCls = model.state === 'ready' ? 'ready' : ['unsupported', 'unconfigured', 'missing', 'invalid'].includes(model.state) ? 'danger' : '';
    mount('Setup', `<section class="route-page">
      <div class="page-head"><div><p class="eyebrow">Setup</p><h1>What is installed, and what is proved.</h1><p class="lede">Hire reports evidence, not configuration. A model counts as ready only after one real <code>ask</code> call answered.</p></div></div>
      <div class="runtime-overview ${runtime.ready ? 'ready' : ''}">
        <div class="runtime-orbit"><b>h</b></div>
        <div><p class="kicker">Runtime</p><h2>${esc(runtime.summary)}</h2><p>${runtime.ready ? 'Every required program answered a version check.' : 'A required program is missing; see the table.'}</p></div>
        <dl><div><dt>Data root</dt><dd>${esc(runtime.dataRoot)}</dd></div><div><dt>Hire executable</dt><dd>${esc(runtime.executable)}</dd></div><div><dt>Jobs</dt><dd>${esc(runtime.jobs)}</dd></div><div><dt>Clock</dt><dd>${esc(runtime.location)} · ${esc(formatDate(runtime.checkedAt))}</dd></div></dl>
      </div>
      <div class="setup-grid">
        <div class="setup-card ${modelCls}"><p class="kicker">Model</p><h3>${esc(model.model || 'No model configured')}</h3><p>${esc(model.message)}</p>${model.nextAction ? `<p><strong>Next:</strong> ${esc(model.nextAction)}</p>` : ''}${arr(model.proved).length ? `<p class="mono-label" data-mt="8">Proved so far: ${model.proved.map((item) => `${esc(item.model)} (${esc(formatDate(item.at))})`).join(' · ')}. Proofs are kept per model; switching the default never un-proves one.</p>` : ''}
          <form data-form="settings"><div class="field"><label for="s-model">Default provider/model</label><input id="s-model" name="model" value="${esc(data.settings?.model || '')}" placeholder="anthropic/your-model"></div>
          <div class="button-row" data-mt="12"><button class="secondary-button" type="submit">Save model</button><button class="primary-button" type="button" data-action="prove-model" ${model.model ? '' : 'disabled'}>Test the model now</button></div></form>
          <p class="mono-label" data-mt="12">The test is one bounded call: <code>ask -q -m MODEL 'Reply with exactly the word: ok'</code>. Hire runs it on its own the first time a model is needed; this button only gets the receipt early. Credentials come from the environment Hire was started in.</p></div>
        <div class="setup-card ${runner.paused ? '' : 'ready'}"><p class="kicker">Runner</p><h3>${runner.paused ? 'Paused' : 'Running'}</h3><p>${runner.workers} loop${runner.workers === 1 ? '' : 's'} calling <code>tend work</code>; ${runner.active || 0} busy now. Last transition ${esc(relative(runner.lastWork))}; routines checked ${esc(relative(runner.lastTick))}.</p>
          <div class="button-row"><button class="secondary-button" data-action="runner" data-paused="${runner.paused ? 'false' : 'true'}">${runner.paused ? 'Resume runner' : 'Pause runner'}</button></div>
          ${arr(runner.errors).length ? `<pre class="output short" data-mt="12">${esc(runner.errors.join('\n'))}</pre>` : ''}</div>
        <div class="setup-card ${runtime.cage.includes('"available":true') ? 'ready' : ''}"><p class="kicker">Cage</p><h3>Confinement</h3><p>Model-authored actions run inside Cage with writes limited to work/ and state/ and network denied unless a worker allows it.</p><pre class="output short">${esc(runtime.cage || 'cage status unavailable')}</pre></div>
        <div class="setup-card ready"><p class="kicker">Environment</p><h3>What reaches a run</h3><p>Tend scrubs the environment. Only PATH, HOME, TMPDIR, and these names pass through:</p><pre class="output short">ASK_MODEL ANTHROPIC_API_KEY ANTHROPIC_BASE_URL OPENAI_API_KEY OPENAI_BASE_URL GEMINI_API_KEY GEMINI_BASE_URL OPENROUTER_API_KEY OPENROUTER_BASE_URL OPENAI_CODEX_ACCOUNT_ID
HIRE_AGENT AGENT_PLY AGENT_BRIEF AGENT_CAGE AGENT_ASK AGENT_HONE AGENT_TRAIL</pre></div>
      </div>
      <section class="content-section"><div class="section-heading"><div><p class="eyebrow">Suite</p><h2>Programs Hire composes</h2></div></div>
        <div class="component-table"><table><thead><tr><th>Program</th><th>Version</th><th>Path</th><th>Status</th></tr></thead><tbody>${arr(runtime.tools).map((t) => `<tr><th>${esc(t.name)}</th><td>${esc(t.version || '—')}</td><td><code>${esc(t.path || '—')}</code></td><td>${t.ok ? '<span class="status-chip positive">ready</span>' : `<span class="status-chip danger">${esc(t.message || 'missing')}</span>`}</td></tr>`).join('')}</tbody></table></div></section>
    </section>`, '');
  }

  // Actions ------------------------------------------------------------------

  view.addEventListener('keydown', (event) => {
    const composer = event.target.closest('form[data-form="builder-chat"] textarea');
    if (!composer || event.key !== 'Enter' || (!event.metaKey && !event.ctrlKey)) return;
    event.preventDefault();
    composer.form.requestSubmit();
  });

  view.addEventListener('submit', async (event) => {
    const form = event.target.closest('form[data-form]');
    if (!form) return;
    event.preventDefault();
    const kind = form.dataset.form;
    const slug = state.ui.slug;
    try {
      if (kind === 'builder-chat') {
        if (state.ui.dirty || state.ui.manualDirty) {
          showToast('Save or discard the manual edit before asking the expert team.', 'warning');
          return;
        }
        const data = new FormData(form);
        const workerSlug = String(data.get('workerSlug') || '');
        const textarea = form.querySelector('textarea[name="message"]');
        const button = form.querySelector('button[type="submit"]');
        button.disabled = true;
        button.textContent = 'Assembling the review team…';
        try {
          await api('/api/builder/chat', { method: 'POST', body: { workerSlug, message: String(data.get('message') || '').trim() } });
          textarea.value = '';
          showToast('The expert team is working. The turn runs on the server, so you can leave or reload this page.');
          if (workerSlug) await renderWorker(workerSlug, { noPoll: true });
          else await renderNew();
          watchBuilder(workerSlug);
          const log = document.querySelector('.builder-log');
          log?.scrollTo({ top: log.scrollHeight });
        } finally {
          if (button.isConnected) {
            button.disabled = false;
            button.textContent = 'Assemble team and draft';
          }
        }
        return;
      }
      if (kind === 'create-worker') return await submitCreate(form);
      if (kind === 'intake') {
        const data = new FormData(form);
        const notBefore = data.get('notBefore') ? new Date(String(data.get('notBefore'))).toISOString() : '';
        const body = { text: String(data.get('text') || '').trim(), check: String(data.get('check') || '').trim(), plan: data.get('plan') === 'on', notBefore, title: '' };
        const button = form.querySelector('button[type="submit"]');
        button.disabled = true;
        button.textContent = body.plan ? 'Planning…' : 'Queuing…';
        try {
          const result = await api(`/api/workers/${enc(slug)}/requests`, { method: 'POST', body });
          const count = arr(result.actions).length + arr(result.routines).length;
          showToast(count > 1 ? `Queued ${plural(arr(result.actions).length, 'action')}${arr(result.routines).length ? ` and ${plural(arr(result.routines).length, 'routine')}` : ''}.` : 'Request queued.', result.warnings?.length ? 'warning' : '');
          arr(result.warnings).forEach((w) => showToast(w, 'warning'));
          state.ui.tab = 'work';
          await renderWorker(slug);
        } finally { button.disabled = false; button.textContent = 'Send to worker'; }
        return;
      }
      if (kind === 'routine') {
        const data = new FormData(form);
        await api(`/api/workers/${enc(slug)}/routines`, { method: 'POST', body: { title: '', instructions: String(data.get('instructions') || ''), every: String(data.get('every')), at: String(data.get('at') || '09:00'), weekday: Number(data.get('weekday') || 1), check: String(data.get('check') || '') } });
        showToast('Routine added.');
        await renderWorker(slug);
        return;
      }
      if (kind === 'new-file') {
        const path = String(new FormData(form).get('path') || '').trim();
        if (!path) return;
        await api(`/api/workers/${enc(slug)}/files`, { method: 'PUT', body: { path, content: '' } });
        state.ui.file = path;
        state.ui.dir = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : state.ui.dir;
        await renderTab(state.cache[slug]);
        return;
      }
      if (kind === 'check-assistant') {
        if (state.ui.dirty) {
          showToast('Save the definition file before asking for checks, so the assistant reads the version you see.', 'warning');
          return;
        }
        const data = new FormData(form);
        const button = form.querySelector('button[type="submit"]');
        button.disabled = true;
        button.textContent = 'Finding evidence…';
        try {
          const result = await api(`/api/workers/${enc(slug)}/checks/suggest`, { method: 'POST', body: { guidance: String(data.get('guidance') || '').trim() } });
          state.ui.checkSuggestions = arr(result.checks);
          state.ui.checkSuggestionNote = result.note || '';
          showToast(result.checks.length ? `Found ${plural(result.checks.length, 'check')} to review.` : 'The assistant did not find a reliable extra mechanical check.', result.checks.length ? '' : 'warning');
          await renderTab(state.cache[slug]);
        } finally {
          button.disabled = false;
          button.textContent = 'Suggest checks';
        }
        return;
      }
      if (kind === 'apply-check-suggestions') {
        if (state.ui.dirty) {
          showToast('Save or discard the open definition edit before changing checks.', 'warning');
          return;
        }
        const checks = [...arr(state.ui.workerChecks), ...arr(state.ui.checkSuggestions)];
        const result = await api(`/api/workers/${enc(slug)}/checks`, { method: 'PUT', body: { checks } });
        state.ui.checkSuggestions = [];
        state.ui.checkSuggestionNote = '';
        showToast(result.receipt.valid ? 'Checks added; agent check accepted the worker.' : `Checks saved, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
        await renderWorker(slug);
        return;
      }
      if (kind === 'manual-worker-check') {
        if (state.ui.dirty) {
          showToast('Save or discard the open definition edit before changing checks.', 'warning');
          return;
        }
        const data = new FormData(form);
        const check = {
          kind: String(data.get('kind') || ''),
          path: String(data.get('path') || '').trim(),
          description: String(data.get('description') || '').trim(),
          text: String(data.get('text') || '').trim(),
          minimumBytes: Number(data.get('minimumBytes') || 0),
        };
        const result = await api(`/api/workers/${enc(slug)}/checks`, { method: 'PUT', body: { checks: [...arr(state.ui.workerChecks), check] } });
        state.ui.manualDirty = false;
        showToast(result.receipt.valid ? 'Check added; agent check accepted the worker.' : `Check saved, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
        await renderWorker(slug);
        return;
      }
      if (kind === 'worker-update') {
        const data = new FormData(form);
        await api(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { name: String(data.get('name') || ''), purpose: String(data.get('purpose') || ''), network: data.get('network') === 'on' } });
        showToast('Name and summary saved.');
        state.ui.dirty = false;
        state.ui.manualDirty = false;
        await renderWorker(slug);
        return;
      }
      if (kind === 'routine-update') {
        const data = new FormData(form);
        await api(`/api/workers/${enc(slug)}/routines/${enc(form.dataset.id)}/update`, { method: 'POST', body: { title: '', instructions: String(data.get('instructions') || ''), every: String(data.get('every')), at: String(data.get('at') || '09:00'), weekday: Number(data.get('weekday') || 1), check: String(data.get('check') || '') } });
        state.ui.editRoutine = '';
        showToast('Routine saved; next run rescheduled.');
        await renderWorker(slug);
        return;
      }
      if (kind === 'worker-model') {
        const model = String(new FormData(form).get('model') || '').trim();
        await api(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { model } });
        showToast(`New requests will run with ${model || 'no model'}.`);
        await renderWorker(slug);
        return;
      }
      if (kind === 'settings') {
        const model = String(new FormData(form).get('model') || '').trim();
        const saved = await api('/api/settings', { method: 'POST', body: { model } });
        showToast(!model ? 'Default model cleared.' : saved.model?.state === 'ready' ? `Model saved. ${model} has already answered a test call.` : `Model saved. Hire will confirm ${model} the first time it is used.`);
        await renderSetup();
      }
    } catch (error) {
      showToast(error.message + (error.nextAction ? ` ${error.nextAction}` : ''), 'danger');
      if (kind === 'create-worker') {
        const box = document.querySelector('#create-error');
        if (box) { box.textContent = error.message; box.hidden = false; }
      }
    }
  });

  view.addEventListener('click', async (event) => {
    const tab = event.target.closest('button[data-tab]');
    if (tab) {
      if ((state.ui.dirty || state.ui.manualDirty) && !window.confirm('Discard unsaved manual changes?')) return;
      state.ui.tab = tab.dataset.tab;
      state.ui.dirty = false;
      state.ui.manualDirty = false;
      document.querySelectorAll('.tabs button').forEach((b) => b.classList.toggle('active', b === tab));
      await renderTab(state.cache[state.ui.slug]);
      return;
    }
    const button = event.target.closest('button[data-action]');
    if (!button) return;
    const slug = state.ui.slug || window.location.pathname.split('/')[2] || '';
    const action = button.dataset.action;
    try {
      switch (action) {
        case 'builder-apply': {
          if (state.ui.dirty || state.ui.manualDirty) {
            showToast('Save or discard the manual edit before applying the expert proposal.', 'warning');
            break;
          }
          const builder = button.closest('[data-builder-worker]');
          const workerSlug = builder?.dataset.builderWorker || '';
          button.disabled = true;
          button.textContent = 'Applying and checking…';
          const result = await api('/api/builder/apply', { method: 'POST', body: { workerSlug } });
          await refreshBootstrap();
          showToast('Applied; agent check accepted the worker.');
          navigate(`/workers/${enc(result.worker.slug)}`);
          break;
        }
        case 'builder-revert': {
          if (state.ui.dirty || state.ui.manualDirty) {
            showToast('Save or discard the manual edit before reverting.', 'warning');
            break;
          }
          if (!window.confirm('Restore the definition this worker had before the last expert apply? The applied files and checks are replaced and agent check runs again.')) break;
          button.disabled = true;
          const result = await api('/api/builder/revert', { method: 'POST', body: { workerSlug: slug } });
          await refreshBootstrap();
          showToast(`Restored the previous definition of ${result.worker?.name || 'the worker'}; agent check accepted it.`);
          await renderWorker(slug);
          break;
        }
        case 'builder-accept-assumptions': {
          const form = view.querySelector('form[data-form="builder-chat"]');
          const textarea = form?.querySelector('textarea[name="message"]');
          if (!form || !textarea) break;
          textarea.value = 'Proceed with your stated assumptions and mark the proposal ready. Do not ask another question unless the definition would be unsafe without the answer; record any remaining open point as an explicit assumption in AGENTS.md.';
          form.requestSubmit();
          break;
        }
        case 'builder-reset': {
          if (state.ui.dirty || state.ui.manualDirty) {
            showToast('Save or discard the manual edit before starting over.', 'warning');
            break;
          }
          if (!window.confirm('Start over? This deletes only the current builder draft and keeps the worker unchanged.')) break;
          const builder = button.closest('[data-builder-worker]');
          const workerSlug = builder?.dataset.builderWorker || '';
          await api(`/api/builder?worker=${enc(workerSlug)}`, { method: 'DELETE' });
          showToast('Builder draft cleared.');
          if (workerSlug) await renderWorker(workerSlug, { noPoll: true });
          else await renderNew();
          break;
        }
        case 'cd':
          state.ui.dir = button.dataset.path || 'work';
          state.ui.file = '';
          state.ui.dirty = false;
          await renderTab(state.cache[slug]);
          break;
        case 'open':
          state.ui.file = button.dataset.path;
          state.ui.dirty = false;
          await renderTab(state.cache[slug]);
          break;
        case 'save-file': {
          const content = document.querySelector('#file-editor').value;
          await api(`/api/workers/${enc(slug)}/files`, { method: 'PUT', body: { path: state.ui.file, content } });
          state.ui.dirty = false;
          showToast(`Saved ${state.ui.file}.`);
          break;
        }
        case 'delete-file':
          if (!window.confirm(`Delete ${state.ui.file}?`)) return;
          await api(`/api/workers/${enc(slug)}/files?path=${enc(state.ui.file)}`, { method: 'DELETE' });
          state.ui.file = '';
          state.ui.dirty = false;
          await renderTab(state.cache[slug]);
          break;
        case 'pick-definition':
          if ((state.ui.dirty || state.ui.manualDirty) && !window.confirm('Discard unsaved changes?')) return;
          state.ui.definition = button.dataset.name;
          state.ui.dirty = false;
          state.ui.manualDirty = false;
          await renderTab(state.cache[slug]);
          break;
        case 'save-definition': {
          const content = document.querySelector('#definition-editor').value;
          const result = await api(`/api/workers/${enc(slug)}/definition`, { method: 'PUT', body: { name: state.ui.definition, content } });
          state.ui.dirty = false;
          state.ui.manualDirty = false;
          showToast(result.receipt.valid ? `${state.ui.definition} saved; agent check accepted.` : `Saved, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
          await renderWorker(slug);
          break;
        }
        case 'remove-worker-check': {
          if (state.ui.dirty) {
            showToast('Save or discard the open definition edit before changing checks.', 'warning');
            return;
          }
          const index = Number(button.dataset.index);
          const checks = arr(state.ui.workerChecks).filter((_, i) => i !== index);
          const result = await api(`/api/workers/${enc(slug)}/checks`, { method: 'PUT', body: { checks } });
          showToast(result.receipt.valid ? 'Check removed; agent check accepted the worker.' : `Check removed, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
          await renderWorker(slug);
          break;
        }
        case 'dismiss-check-suggestion': {
          const index = Number(button.dataset.index);
          state.ui.checkSuggestions = arr(state.ui.checkSuggestions).filter((_, i) => i !== index);
          if (!state.ui.checkSuggestions.length) state.ui.checkSuggestionNote = '';
          await renderTab(state.cache[slug]);
          break;
        }
        case 'edit-worker': {
          state.ui.tab = 'definition';
          state.ui.dirty = false;
          document.querySelectorAll('.tabs button').forEach((b) => b.classList.toggle('active', b.dataset.tab === 'definition'));
          await renderTab(state.cache[slug]);
          document.querySelector('#tab-panel')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
          break;
        }
        case 'edit-routine':
          state.ui.editRoutine = button.dataset.id;
          await renderTab(state.cache[slug]);
          break;
        case 'cancel-edit-routine':
          state.ui.editRoutine = '';
          await renderTab(state.cache[slug]);
          break;
        case 'toggle-enabled': {
          const w = state.cache[slug];
          await api(`/api/workers/${enc(slug)}/enabled`, { method: 'POST', body: { enabled: !w.enabled } });
          showToast(w.enabled ? 'Intake paused. Queued work still runs.' : 'Intake resumed.');
          await renderWorker(slug);
          break;
        }
        case 'retire': {
          const w = state.cache[slug];
          if (!window.confirm(`Retire ${w.name}? Queued work is cancelled and the home moves to var/retired/. Nothing is deleted.`)) return;
          await api(`/api/workers/${enc(slug)}`, { method: 'DELETE' });
          showToast(`${w.name} retired.`);
          navigate('/workers');
          break;
        }
        case 'routine': {
          const result = await api(`/api/workers/${enc(slug)}/routines/${enc(button.dataset.id)}/${button.dataset.do}`, { method: 'POST' });
          showToast(button.dataset.do === 'run' ? `Queued ${result.request.id}.` : `Routine ${button.dataset.do}d.`);
          await renderWorker(slug);
          break;
        }
        case 'request': {
          const id = window.location.pathname.split('/')[4];
          const body = button.dataset.do === 'resolve' ? { decision: button.dataset.decision } : undefined;
          if (button.dataset.do === 'resolve' && !window.confirm(`Resolve as ${button.dataset.decision}?`)) return;
          const result = await api(`/api/workers/${enc(slug)}/requests/${enc(id)}/${button.dataset.do}`, { method: 'POST', body });
          if (button.dataset.do === 'rerun') { showToast(`Queued ${result.request.id}.`); navigate(`/workers/${enc(slug)}/requests/${enc(result.request.id)}`); return; }
          showToast('Done.');
          await renderRequest(slug, id);
          break;
        }
        case 'prove-model': {
          button.disabled = true;
          button.textContent = 'Asking…';
          try {
            const result = await api('/api/model/prove', { method: 'POST' });
            showToast(result.proof.ok ? `Model answered: ${result.proof.output}` : `Proof failed: ${result.proof.output}`, result.proof.ok ? '' : 'danger');
          } finally { await renderSetup(); }
          break;
        }
        case 'runner':
          await api('/api/runner', { method: 'POST', body: { paused: button.dataset.paused === 'true' } });
          await renderSetup();
          break;
      }
    } catch (error) {
      showToast(error.message + (error.nextAction ? ` ${error.nextAction}` : ''), 'danger');
    }
  });

  window.addEventListener('beforeunload', (event) => {
    if (state.ui.dirty) { event.preventDefault(); event.returnValue = ''; }
  });

  route();
})();
