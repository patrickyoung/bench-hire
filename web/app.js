(() => {
  'use strict';

  // Hire's front end speaks to a hiring manager: a team of workers, a job
  // description you approve, tasks you hand out, results you read, and one
  // conversation for changing a worker. Everything technical (files, digests,
  // commands, logs) stays one disclosure away and never leads a screen.

  const view = document.querySelector('#app-view');
  const main = document.querySelector('#main');
  const announcer = document.querySelector('#route-announcer');
  const toast = document.querySelector('#toast');
  const noticeBar = document.querySelector('#notice');

  const state = {
    bootstrap: null,
    token: '',
    pollTimer: null,
    builderTimer: null,
    builderGeneration: 0,
    tabGeneration: 0,
    toastTimer: null,
    hireStarted: 0,
    navigation: 0,
    pollGeneration: 0,
    ui: freshUI(''),
    cache: {},
    pendingForms: new Map(),
  };

  const reading = new window.HireViewState(view, () => window.location.pathname);
  let mountedPath = '';
  const connectionNotice = document.querySelector('#connection-notice');

  function connectionState(error) {
    connectionNotice.hidden = !error;
    connectionNotice.textContent = error ? 'Live updates are interrupted. Your place and drafts are kept. Hire will try again.' : '';
  }

  function freshUI(slug) {
    return { slug, tab: 'work', dir: 'work', file: '', definition: 'GOAL.md', dirty: false, manualDirty: false, editRoutine: '', addRoutine: false, options: false, workerChecks: [], checkSuggestions: [], checkSuggestionNote: '' };
  }

  const CADENCES = [
    ['', 'Not on a schedule'],
    ['hourly', 'Every hour'],
    ['daily', 'Every day'],
    ['weekdays', 'Weekdays'],
    ['weekly', 'Once a week'],
    ['30m', 'Every 30 minutes'],
    ['15m', 'Every 15 minutes'],
  ];
  const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday'];

  // TASK_META maps a request state to a tone, a short label, and one plain
  // sentence a manager can act on.
  const TASK_META = {
    scheduled: ['quiet', 'Scheduled', 'Scheduled to start later.'],
    queued: ['neutral', 'Waiting to start', 'Waiting for a free slot.'],
    running: ['active', 'Working', 'The worker is on it now.'],
    waiting: ['neutral', 'Waiting', 'Waiting before trying again.'],
    done: ['positive', 'Checks passed', 'The automatic checks passed. Review the result for accuracy and usefulness.'],
    review: ['warning', 'Ready for review', 'The automatic checks passed. Read the result, then accept it or explain what needs to change.'],
    accepted: ['positive', 'Accepted', 'You accepted this result after its automatic checks passed.'],
    'changes-requested': ['warning', 'Changes requested', 'Your feedback is saved. Send a revision task, or refine the worker for future tasks.'],
    'revision-sent': ['quiet', 'Revision sent', 'A linked task carries your feedback. Follow it for the improved result.'],
    unfinished: ['warning', 'Needs more work', 'The checks have not passed, or the run reached a limit. Inspect the result, then continue or ask for a change.'],
    broken: ['danger', 'Run needs a fix', 'A program or completion check failed. Read what happened before trying again.'],
    failed: ['danger', 'Failed', 'The run failed before it produced a result.'],
    'timed-out': ['danger', 'Took too long', 'The run hit its time limit and was stopped.'],
    boundary: ['danger', 'Could not run safely', 'The execution boundary could not be confirmed. Inspect the details before continuing.'],
    unknown: ['danger', 'Outcome unclear', 'The run started, but nobody recorded how it ended. You decide what happened.'],
    cancelled: ['quiet', 'Cancelled', 'Cancelled before it ran.'],
    'not-started': ['quiet', 'Did not start', 'Retirement stopped this task before the worker started.'],
    planned: ['quiet', 'Planned', 'Split into the steps below.'],
    unsubmitted: ['warning', 'Saved, waiting to queue', 'Your task is saved. Hire will retry queue delivery; you can also try it now.'],
  };
  const NEEDS_YOU = new Set(['unknown', 'unfinished', 'broken', 'failed', 'timed-out', 'boundary', 'unsubmitted', 'review', 'changes-requested']);

  class RequestError extends Error {
    constructor(message, status, code, nextAction) {
      super(message);
      this.status = status;
      this.code = code;
      this.nextAction = nextAction;
    }
  }

  // Helpers ------------------------------------------------------------------

  const esc = (value) => String(value ?? '')
    .replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&#39;');
  const enc = (value) => encodeURIComponent(String(value ?? ''));
  const arr = (value) => (Array.isArray(value) ? value : []);
  const validDate = (value) => {
    if (!value) return null;
    const date = new Date(value);
    return Number.isNaN(date.getTime()) || date.getFullYear() < 1971 ? null : date;
  };

  function formatDate(value) {
    const date = validDate(value);
    if (!date) return '—';
    return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }).format(date);
  }

  function relative(value) {
    const date = validDate(value);
    if (!date) return '—';
    const diff = (date.getTime() - Date.now()) / 1000;
    const abs = Math.abs(diff);
    if (abs < 45) return diff < 0 ? 'just now' : 'in a moment';
    const unit = abs < 3600 ? [Math.round(abs / 60), 'minute'] : abs < 86400 ? [Math.round(abs / 3600), 'hour'] : [Math.round(abs / 86400), 'day'];
    const text = `${unit[0]} ${unit[1]}${unit[0] === 1 ? '' : 's'}`;
    return diff < 0 ? `${text} ago` : `in ${text}`;
  }

  function elapsedBetween(fromIso, toIso) {
    const from = validDate(fromIso);
    if (!from) return '';
    const to = toIso ? new Date(toIso).getTime() : Date.now();
    const seconds = Math.round((to - from.getTime()) / 1000);
    if (!Number.isFinite(seconds) || seconds < 0) return '';
    return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`;
  }

  function clock(ms) {
    const s = Math.max(0, Math.floor(ms / 1000));
    return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
  }

  function plural(count, word) { return `${count} ${word}${count === 1 ? '' : 's'}`; }

  function initials(name) {
    const parts = String(name || '').trim().split(/\s+/).filter(Boolean);
    return (parts.slice(0, 2).map((p) => p[0]).join('') || 'W').toUpperCase();
  }

  const md = window.HireMarkdown;

  function pill(tone, label) { return `<span class="pill ${tone}">${esc(label)}</span>`; }

  function workerStatus(w) {
    if (w.retiredAt) return ['quiet', 'Retired'];
    if (w.retiringAt) return ['warning', 'Retiring'];
    if (w.checkState !== 'valid') return ['danger', 'Needs a fix'];
    if (!w.enabled) return ['quiet', 'Paused'];
    if (w.attention) return ['warning', 'Needs you'];
    if (w.running) return ['active', 'Working'];
    if (w.queued) return ['neutral', 'Work queued'];
    if (!w.accepted) return ['neutral', 'Try a first task'];
    return ['positive', 'Available'];
  }

  function checkSentence(c) {
    const path = c.path || '';
    const what = c.description || '';
    if (c.kind === 'file_nonempty') return `${what || 'A file is written'}: ${path} exists and is not empty.`;
    if (c.kind === 'text_contains') return `${what || 'A file says the right thing'}: ${path} contains “${c.text}”.`;
    if (c.kind === 'minimum_bytes') return `${what || 'A file is substantial'}: ${path} is at least ${c.minimumBytes} bytes.`;
    return what || c.kind;
  }

  function showToast(message, kind = '') {
    toast.textContent = message;
    toast.className = `toast ${kind}`.trim();
    toast.hidden = false;
    clearTimeout(state.toastTimer);
    state.toastTimer = setTimeout(() => { toast.hidden = true; }, 5200);
  }

  function currentView() {
    const navigation = state.navigation;
    const { tab, file, definition } = state.ui;
    return () => navigation === state.navigation && tab === state.ui.tab && file === state.ui.file && definition === state.ui.definition;
  }

  function requireCurrent(here) {
    if (!here()) throw new DOMException('The page changed while the action completed.', 'AbortError');
  }

  // Keep an in-flight form busy even if its tab is rebuilt or revisited.
  const pendingButtons = new WeakMap();
  function updatePendingForms() {
    for (const form of view.querySelectorAll('form[data-form]')) {
      const pending = state.pendingForms.get(`${reading.currentScope}:${reading.formKey(form)}`);
      if (pending) {
        if (!pendingButtons.has(form)) pendingButtons.set(form, [...form.querySelectorAll('button[type="submit"]')].map(button => ({ button, disabled: button.disabled, text: button.textContent })));
        form.setAttribute('aria-busy', 'true');
        for (const { button } of pendingButtons.get(form)) {
          button.disabled = true;
          if (button.value === pending.value) button.textContent = pending.label;
        }
      } else if (pendingButtons.has(form)) {
        form.removeAttribute('aria-busy');
        for (const { button, disabled, text } of pendingButtons.get(form)) { button.disabled = disabled; button.textContent = text; }
        pendingButtons.delete(form);
      }
    }
  }

  async function api(path, options = {}) {
    const navigation = state.navigation;
    const init = { method: options.method || 'GET', headers: { Accept: 'application/json', 'X-Hire-Token': state.token } };
    if (options.body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(options.body);
    }
    const response = await fetch(path, init);
    let payload = null;
    try { payload = await response.json(); } catch { payload = null; }
    if (init.method === 'GET' && navigation !== state.navigation) {
      throw new DOMException('The page changed while loading.', 'AbortError');
    }
    if (!response.ok) {
      const error = payload?.error || {};
      throw new RequestError(error.message || `Request failed (${response.status})`, response.status, error.code || '', error.nextAction || '');
    }
    connectionState(null);
    return payload;
  }

  async function refreshBootstrap() {
    const data = await api('/api/bootstrap');
    state.bootstrap = data;
    state.token = data.token;
    renderChrome();
    return data;
  }

  const modelUsable = () => ['ready', 'unproved'].includes(state.bootstrap?.runtime?.model?.state);

  // renderChrome keeps the top bar honest: the Needs-you link appears only
  // when something waits on a person, and one thin line says when the
  // system itself needs attention.
  function renderChrome() {
    const data = state.bootstrap;
    if (!data) return;
    const attention = arr(data.attention).length;
    document.querySelector('#nav-attention').hidden = !attention;
    document.querySelector('#attention-count').textContent = String(attention);
    const runtime = data.runtime || {};
    const model = runtime.model || {};
    let text = '';
    let link = '';
    if (!runtime.ready) { text = 'Some Bench programs are missing, so workers cannot run yet.'; link = 'See what is missing'; }
    else if (!modelUsable()) { text = model.message || 'No AI model is set yet.'; link = 'Choose a model'; }
    else if (data.runner?.paused) { text = 'All work is paused.'; link = 'Resume'; }
    const onSettings = ['/settings', '/setup'].includes(window.location.pathname.replace(/\/+$/, ''));
    noticeBar.hidden = !text || onSettings;
    noticeBar.innerHTML = text ? `<span>${esc(text)}</span><a href="/settings" data-link>${esc(link)} →</a>` : '';
  }

  function setNav(name) {
    document.querySelectorAll('[data-nav]').forEach((item) => {
      const active = item.dataset.nav === name;
      item.classList.toggle('active', active);
      if (active) item.setAttribute('aria-current', 'page');
      else item.removeAttribute('aria-current');
    });
  }

  function mount(title, html, nav, { background = false } = {}) {
    const navigation = mountedPath !== window.location.pathname;
    if (navigation) announcer.textContent = title;
    document.title = title === 'Team' ? 'Hire' : `${title} · Hire`;
    setNav(nav);
    if (!reading.replace(view, html, { navigation, background })) return false;
    updatePendingForms();
    stopBuilder();
    mountedPath = window.location.pathname;
    if (navigation) window.scrollTo({ top: 0 });
    renderChrome();
    return true;
  }

  function stopBuilder() {
    clearTimeout(state.builderTimer);
    state.builderTimer = null;
    state.builderGeneration++;
  }

  function stopPolling() {
    state.pollGeneration++;
    clearTimeout(state.pollTimer);
    state.pollTimer = null;
  }

  function editing() {
    const active = document.activeElement;
    return state.ui.dirty || state.ui.manualDirty || state.ui.editRoutine || state.ui.addRoutine
      || (active && view.contains(active) && ['TEXTAREA', 'INPUT', 'SELECT'].includes(active.tagName) && (active.value || active.tagName !== 'TEXTAREA'));
  }

  function poll(fn, ms) {
    stopPolling();
    const navigation = state.navigation;
    const generation = state.pollGeneration;
    const tick = async () => {
      if (navigation !== state.navigation || generation !== state.pollGeneration) return;
      try { await fn(); } catch (error) {
        if (error.name !== 'AbortError') connectionState(error);
      }
      if (navigation === state.navigation && generation === state.pollGeneration) state.pollTimer = setTimeout(tick, ms);
    };
    state.pollTimer = setTimeout(tick, ms);
  }

  // The empty-team prompt hands its text to the Hire page through session
  // storage, so one code path drafts, prefills, or falls back to a manual hire.
  function stash(text) { try { sessionStorage.setItem('hire.draft', text); } catch { /* private mode */ } }
  function consumeStash() {
    try {
      const text = sessionStorage.getItem('hire.draft') || '';
      sessionStorage.removeItem('hire.draft');
      return text;
    } catch { return ''; }
  }

  // Routing ------------------------------------------------------------------

  function navigate(path) {
    if (path === `${window.location.pathname}${window.location.search}`) { route(); return; }
    window.history.pushState({}, '', path);
    route();
  }

  document.addEventListener('click', (event) => {
    document.querySelectorAll('details.menu[open]').forEach((menu) => { if (!menu.contains(event.target)) menu.open = false; });
    const link = event.target.closest('a[data-link]');
    if (!link || event.metaKey || event.ctrlKey || event.shiftKey) return;
    event.preventDefault();
    navigate(link.getAttribute('href'));
  });
  window.addEventListener('popstate', route);

  async function route() {
    reading.rememberDetails(view);
    state.navigation++;
    stopPolling();
    stopBuilder();
    state.tabGeneration++;
    state.ui.dirty = false;
    state.ui.manualDirty = false;
    const path = window.location.pathname.replace(/\/+$/, '') || '/';
    const params = new URLSearchParams(window.location.search);
    try {
      if (!state.bootstrap) await refreshBootstrap();
      let match;
      if (path === '/' || path === '/workers') return await renderTeam();
      if (path === '/hire' || path === '/new') return await renderHire();
      if (path === '/needs-you' || path === '/attention') return await renderAttention();
      if (path === '/settings' || path === '/setup') return await renderSettings();
      if ((match = path.match(/^\/workers\/([a-z0-9-]+)\/(?:requests|tasks)\/([a-z0-9-]+)$/))) return await renderTask(match[1], match[2]);
      if ((match = path.match(/^\/workers\/([a-z0-9-]+)$/))) return await renderWorker(match[1], { tab: params.get('tab') || '', fix: params.get('fix') || '', perspective: params.get('perspective') || '' });
      mount('Not found', `<section class="page"><h1>Nothing here</h1><p class="lede">That address does not name a page.</p><a class="button" href="/" data-link>Back to the team</a></section>`, '');
    } catch (error) {
      if (error.name === 'AbortError') return;
      mount('Error', `<section class="page"><h1>Hire could not load this page</h1><p class="lede">${esc(error.message)}</p><a class="button" href="/" data-link>Back to the team</a></section>`, '');
    }
  }

  // Team ---------------------------------------------------------------------

  function workerRow(w) {
    const [tone, label] = workerStatus(w);
    const facts = [];
    if (w.running) facts.push(`${w.running} working`);
    if (w.queued) facts.push(`${w.queued} queued`);
    if (w.accepted) facts.push(`${w.accepted} accepted`);
    if (w.forReview) facts.push(`${w.forReview} to review`);
    if (arr(w.routines).length) facts.push(plural(arr(w.routines).length, 'recurring task'));
    return `<a class="worker-row" href="/workers/${enc(w.slug)}" data-link>
      <span class="avatar ${tone}">${esc(initials(w.name))}</span>
      <span class="worker-row-main"><strong>${esc(w.name)}</strong><span>${esc(w.purpose)}</span></span>
      <span class="worker-row-side">${pill(tone, label)}<small>${esc(facts.join(' · ') || 'No tasks yet')}</small></span>
    </a>`;
  }

  function taskRow(w, r, showWorker = false) {
    const [tone, label] = TASK_META[r.state] || ['quiet', r.state];
    const when = r.state === 'scheduled' ? `starts ${relative(r.notBefore)}` : r.state === 'running' ? `started ${relative(r.startedAt || r.updatedAt)}` : relative(r.updatedAt);
    const origin = r.source.startsWith('routine:') ? 'recurring' : r.source.startsWith('plan:') ? 'step' : r.source.startsWith('rerun:') ? 'run again' : '';
    const meta = [showWorker ? w.name : '', r.specialist ? `Perspective: ${r.specialist}` : '', origin].filter(Boolean);
    return `<a class="task-row" href="/workers/${enc(w.slug)}/requests/${enc(r.id)}" data-link>
      <span class="task-mark ${tone}" aria-hidden="true"></span>
      <span class="task-main"><strong>${esc(r.title)}</strong><span>${esc(r.summary || (r.text || '').slice(0, 140))}</span>${meta.length ? `<small>${meta.map(esc).join(' · ')}</small>` : ''}</span>
      <span class="task-side">${pill(tone, label)}<small>${esc(when)}${r.children ? ` · ${plural(r.children, 'step')}` : ''}</small></span>
    </a>`;
  }

  function quickHireForm() {
    const ready = modelUsable();
    return `<form class="hire-form" data-form="quick-hire">
      <label class="sr-only" for="quick-text">Describe the job</label>
      <textarea id="quick-text" name="text" required maxlength="16384" rows="4" placeholder="Every weekday morning, read yesterday's merged pull requests and write a short release note for the team. Keep a list of products and their owners, and stop for me when ownership is unclear."></textarea>
      <div class="form-row"><small>${ready ? 'Hire drafts a job description for your review. Nothing is hired until you approve it.' : 'No AI model is set yet, so Hire will use exactly what you write. <a href="/settings" data-link>Choose a model</a> to get a drafted job description.'}</small><button class="button primary" type="submit">${ready ? 'Draft the job description' : 'Continue'}</button></div>
    </form>`;
  }

  async function renderTeam() {
    const data = await refreshBootstrap();
    const allWorkers = arr(data.workers);
    const workers = allWorkers.filter(w => !w.retiredAt);
    const retired = allWorkers.filter(w => w.retiredAt);
    const archive = retired.length ? `<details class="block" data-disclosure="retired-workers"><summary>Retired workers (${retired.length})</summary><p class="muted">Past work, results, and history remain available.</p><div class="list">${retired.map(workerRow).join('')}</div></details>` : '';
    const attention = arr(data.attention);
    const bySlug = (slug) => workers.find((w) => w.slug === slug) || { slug, name: slug };
    if (!workers.length) {
      mount('Team', `<section class="page"><div class="hero">
        <p class="eyebrow">Hire</p>
        <h1>Hire your first digital worker.</h1>
        <p class="lede">Describe the job, review the plan, then try a first task. You stay in charge of the results.</p>
        ${quickHireForm()}
      </div>${archive}</section>`, 'team');
      if (!reading.hasDraft()) view.querySelector('#quick-text')?.focus();
      poll(renderTeam, 10000);
      return;
    }
    mount('Team', `<section class="page">
      ${attention.length ? `<section class="block"><div class="block-head"><h2>Needs you</h2>${attention.length > 4 ? `<a href="/needs-you" data-link>All ${attention.length} →</a>` : ''}</div><div class="list">${attention.slice(0, 4).map((r) => taskRow(bySlug(r.workerSlug), r, true)).join('')}</div></section>` : ''}
      <section class="block"><div class="block-head"><h1>Your team</h1></div><div class="list">${workers.map(workerRow).join('')}</div></section>
      ${archive}
    </section>`, 'team');
    poll(renderTeam, 8000);
  }

  async function renderAttention() {
    const data = await refreshBootstrap();
    const items = arr(data.attention);
    const workers = arr(data.workers);
    const bySlug = (slug) => workers.find((w) => w.slug === slug) || { slug, name: slug };
    mount('Needs you', `<section class="page">
      <header class="page-head"><div><h1>Needs you</h1><p class="lede">Results to review and tasks that need your help. Open one to see the next step.</p></div></header>
      ${items.length ? `<div class="list">${items.map((r) => taskRow(bySlug(r.workerSlug), r, true)).join('')}</div>` : '<div class="list"><p class="empty">Nothing is waiting on you.</p></div>'}
    </section>`, 'attention');
    poll(renderAttention, 8000);
  }

  // Hire (new worker) --------------------------------------------------------

  const turnInProgress = (turn) => Boolean(turn && !['complete', 'failed'].includes(turn.status));

  function chatBubble(m) {
    return `<div class="bubble ${m.role === 'user' ? 'me' : 'them'}"><span>${m.role === 'user' ? 'You' : 'Hire'}</span><p>${esc(m.text)}</p></div>`;
  }

  function progressCard(turn, team) {
    const reviewed = turn.mode !== 'single';
    const experts = arr(turn.experts).length ? arr(turn.experts) : arr(team?.permanent);
    const reports = arr(turn.reports);
    const states = turn.reviewStates || {};
    const reported = new Set(reports.map((r) => r.expert?.id));
    let line;
    switch (turn.status) {
      case 'drafting': line = 'Writing the job description from your request and the worker’s current setup.'; break;
      case 'routing': line = 'Reading your message and choosing the right specialists.'; break;
      case 'reviewing': line = `${reports.length} of ${experts.length} reviews are in. Each takes a minute or two.`; break;
      case 'synthesizing': line = 'Reviews are in. Writing the job description now.'; break;
      default: line = 'Working.';
    }
    const roster = experts.map((e) => {
      const st = turn.status === 'routing' ? 'standing by' : (states[e.id] || (reported.has(e.id) ? 'done' : 'waiting'));
      const label = { 'standing by': 'standing by', waiting: 'waiting', reviewing: 'reviewing', done: 'done', failed: 'dropped', stopped: 'stopped' }[st] || st;
      return `<li class="${esc(st.replace(' ', '-'))}"><span aria-hidden="true"></span><b>${esc(e.name)}</b><small>${esc(label)}</small></li>`;
    }).join('');
    return `<div class="progress-card"><div class="progress-head"><span class="spinner" aria-hidden="true"></span><div><strong>Drafting the job description</strong><p>${esc(line)} You can leave this page; the draft keeps going.</p></div><em>${esc(elapsedBetween(turn.startedAt, null))}</em></div>
      ${reviewed ? `<details class="progress-team" data-disclosure="builder-progress"><summary>Who is reviewing</summary><ul>${roster}</ul><p>Three Bench reviewers check the draft against how workers actually run; one to three specialists cover the job itself. A lead then writes the job description.</p></details>` : ''}</div>`;
  }

  function proposalCard(session, worker, ready, stale, example = null) {
    const p = session.proposal;
    const files = p.files || {};
    const checks = arr(p.checks);
    const reports = arr(session.reports);
    const others = [['soul', 'Character'], ['plan', 'Plan'], ['memory', 'Memory'], ['heartbeat', 'Heartbeat']].filter(([key]) => (files[key] || '').trim());
    const applyLabel = worker ? `Apply to ${worker.name}` : `Hire ${p.name || 'this worker'}`;
    const locked = !session.ready
      ? `<div class="notice warn"><span><strong>One question first.</strong> The draft is waiting on your answer. Reply below, or <button class="link" type="button" data-action="builder-accept-assumptions">tell it to go with its assumptions</button>.</span></div>`
      : '';
    return `<article class="job-card">
      <header class="job-head"><div><p class="eyebrow">Job description</p><h2>${esc(p.name || 'Unnamed worker')}</h2><p>${esc(p.purpose || '')}</p></div><div class="pills">${pill(p.network ? 'active' : 'quiet', p.network ? 'Task internet allowed' : 'Task internet off')}</div></header>
      <section class="job-section"><h3>What done looks like</h3><div class="prose">${md(files.goal)}</div></section>
      <details class="job-more" data-disclosure="builder-method"><summary>How ${esc(p.name || 'the worker')} will work</summary><div class="prose">${md(files.agents)}</div></details>
      ${others.length ? `<details class="job-more" data-disclosure="builder-notes"><summary>Other notes</summary>${others.map(([key, label]) => `<h4>${esc(label)}</h4><div class="prose">${md(files[key])}</div>`).join('')}</details>` : ''}
      <section class="job-section"><h3>What gets checked</h3><ul class="criteria"><li>A written result for every task.</li>${checks.map((c) => `<li>${esc(checkSentence(c))}</li>`).join('')}</ul><p class="muted">These are automatic checks. You review the result for accuracy and usefulness.</p></section>
      ${example ? `<section class="job-section"><h3>First task included</h3><p>${esc(example.task.title)}</p><p>${esc(example.checkScope)}</p><details data-disclosure="example-task"><summary>Read the sample task and supplied files</summary><p class="prose-plain">${esc(example.task.text)}</p><ul>${arr(example.files).map(path => `<li><code>${esc(path)}</code></li>`).join('')}</ul><p>Task check: <code>${esc(example.task.check)}</code></p></details><details data-disclosure="example-recovery"><summary>Try a correction afterwards</summary><p class="prose-plain">${esc(example.recovery)}</p></details></section>` : ''}
      ${arr(session.changes).length ? `<section class="job-section"><h3>What changed in this draft</h3><ul class="changes">${session.changes.map((c) => `<li>${esc(c)}</li>`).join('')}</ul></section>` : ''}
      ${reports.length ? `<details class="job-more" data-disclosure="builder-reviewers"><summary>Reviewed by ${plural(reports.length, 'specialist')}</summary><ul class="reviewers">${reports.map((r) => `<li><b>${esc(r.expert?.name || 'Reviewer')}</b><span>${esc(r.summary)}</span>${arr(r.risks).length ? `<small>Risks: ${r.risks.map(esc).join(' · ')}</small>` : ''}</li>`).join('')}</ul></details>` : ''}
      ${locked}
      <p class="job-section muted">Task actions can write only their work and state folders. They can read other files available to this account. The internet setting applies to task actions; model calls still use Ask.</p>
      ${example ? `<footer class="job-actions"><form data-form="example-hire" data-id="${esc(example.id)}"><div class="field"><label for="example-name">Worker name</label><input id="example-name" name="name" required maxlength="120" value="${esc(p.name)}"></div><button class="button primary" type="submit">Hire this example worker</button><p class="muted">Installs the job description, supplied inputs and task check. No AI call is needed to hire it; you send its first task afterwards.</p></form></footer>` : `<footer class="job-actions"><button class="button primary" type="button" data-action="builder-apply" ${ready && !stale ? '' : 'disabled'}>${esc(applyLabel)}</button><button class="link quiet" type="button" data-action="builder-reset">Start over</button><small>${worker ? 'Applies this job description and checks its structure. You can revert afterwards.' : 'Creates this worker and checks its structure. Then try a small, real task.'}</small></footer>`}
    </article>`;
  }

  // builderMarkup is the one conversation for creating or changing a worker.
  // Order: what was said, the job description it produced, then the reply box.
  function builderMarkup(data, worker) {
    const session = data?.session || null;
    const assistant = data?.assistant || {};
    const team = data?.team || {};
    const messages = arr(session?.messages);
    const latestTurn = arr(session?.turns).at(-1);
    const failed = latestTurn?.status === 'failed' ? latestTurn : null;
    const inProgress = turnInProgress(latestTurn);
    const scope = worker?.slug || '';
    const ready = Boolean(session?.ready && session?.proposal);
    const started = messages.length > 0 || inProgress;
    const stale = Boolean(assistant.stale) && !inProgress;
    const canSend = assistant.ready && !inProgress;
    const placeholder = worker
      ? `Tell ${worker.name} what to change. For example: "Also keep a running list of open questions in a file I can read." or "Stop and ask me when a pull request has no description instead of guessing."`
      : "Every weekday morning, read yesterday's merged pull requests and write a short release note for the team. Keep a list of products and their owners, and stop for me when ownership is unclear.";
    let notice = '';
    if (!assistant.ready) notice = `<div class="notice warn"><span>${esc(assistant.message || 'Hire needs an AI model to draft job descriptions.')}</span><a href="/settings" data-link>Choose a model →</a></div>`;
    else if (stale) notice = `<div class="notice"><span>${esc(assistant.message)}</span><button class="link" type="button" data-action="builder-reset">Start over</button></div>`;
    // Only the latest exchange stays in view; earlier turns fold away so the
    // job description, not the transcript, is what the page is about.
    const recent = messages.slice(-2);
    const earlier = messages.slice(0, -2);
    const log = started
      ? `<div class="chat" role="log" aria-live="polite">${earlier.length ? `<details class="earlier" data-disclosure="builder-earlier"><summary>${plural(earlier.length, 'earlier message')}</summary>${earlier.map(chatBubble).join('')}</details>` : ''}${recent.map(chatBubble).join('')}${inProgress ? chatBubble({ role: 'user', text: latestTurn.message }) + progressCard(latestTurn, team) : ''}${failed ? `<div class="notice danger"><span><strong>That draft did not go through.</strong> ${esc(failed.error || 'The previous draft was kept.')}</span></div>` : ''}</div>`
      : '';
    const proposal = session?.proposal && !inProgress ? proposalCard(session, worker, ready, stale) : '';
    const hint = worker ? 'Proposes a change to the standing job. Nothing changes until you apply it.' : 'Drafts the standing job. Nothing is hired until you approve it.';
    const effort = arr(session?.turns).length ? `<details class="tech block" data-disclosure="draft-effort"><summary>Drafting effort</summary><table><thead><tr><th>Turn</th><th>Method</th><th>AI calls</th><th>Elapsed</th><th>Outcome</th></tr></thead><tbody>${session.turns.map(turn => `<tr><td>${turn.number}</td><td>${turn.mode === 'single' ? 'Single author' : 'Independent reviews'}</td><td>${turn.effort?.askCalls ?? 'Not recorded'}</td><td>${turn.effort ? clock(turn.effort.elapsedMs) : 'Not recorded'}</td><td>${esc(turn.status)}</td></tr>`).join('')}</tbody></table><p>Counts calls started by Hire, including connection tests. Provider retries stay in the Ask sessions. Review count alone does not establish a better job description.</p></details>` : '';
    const composer = `<form class="chat-form" data-form="builder-chat"><input type="hidden" name="workerSlug" value="${esc(scope)}">
      <label class="sr-only" for="builder-message-${esc(scope || 'new')}">${started ? 'Reply' : 'Describe the job'}</label>
      <textarea id="builder-message-${esc(scope || 'new')}" name="message" required maxlength="16384" rows="${started ? 3 : 5}" placeholder="${esc(started ? 'Answer the question, or ask for a change…' : placeholder)}" ${canSend ? '' : 'disabled'}></textarea>
      <details data-disclosure="draft-options"><summary>Drafting options</summary><label class="check"><input type="checkbox" name="reviewTeam" ${latestTurn?.mode === 'review-team' ? 'checked' : ''} ${canSend ? '' : 'disabled'}> Ask for independent reviews</label><p class="muted">The usual draft uses one author. Reviews add other perspectives and take more time and calls.</p></details>
      <div class="form-row"><small>${hint}</small><button class="button primary" type="submit" ${canSend ? '' : 'disabled'}>${inProgress ? 'Drafting…' : started ? 'Send' : worker ? 'Propose the change' : 'Draft the job description'}</button></div></form>`;
    return `<section class="builder" data-builder-worker="${esc(scope)}">${notice}${log}${proposal}${composer}${effort}</section>`;
  }

  // watchBuilder polls the turn that runs on the server and re-renders only
  // the conversation, so the rest of the page (and any half-typed task) stays.
  function watchBuilder(scope) {
    stopBuilder();
    const generation = state.builderGeneration;
    const navigation = state.navigation;
    const tick = async () => {
      if (generation !== state.builderGeneration || navigation !== state.navigation) return;
      try {
        const data = await api(`/api/builder?worker=${enc(scope)}`);
        if (generation !== state.builderGeneration || navigation !== state.navigation) return;
        const el = view.querySelector(`[data-builder-worker="${scope}"]`);
        if (!el) return;
        const latest = arr(data.session?.turns).at(-1);
        const replaced = reading.replace(el, builderMarkup(data, scope ? state.cache[scope] || null : null), { outer: true, background: true });
        if (replaced && !turnInProgress(latest)) {
          stopBuilder();
          if (latest?.status === 'complete') showToast(scope ? 'The proposed change is ready to review.' : 'The job description is ready to review.');
          else if (latest?.status === 'failed') showToast('The draft did not go through. The previous one was kept.', 'warning');
          return;
        }
      } catch (error) {
        if (error.name !== 'AbortError') connectionState(error);
      }
      if (generation === state.builderGeneration && navigation === state.navigation) state.builderTimer = setTimeout(tick, 2500);
    };
    state.builderTimer = setTimeout(tick, 2500);
  }

  async function sendBuilderMessage(workerSlug, message, mode = 'single') {
    const here = currentView();
    const form = view.querySelector('form[data-form="builder-chat"]');
    const submission = form && reading.formSubmission(form);
    await api('/api/builder/chat', { method: 'POST', body: { workerSlug, message, mode } });
    if (form) reading.forgetForm(form, submission);
    requireCurrent(here);
    if (workerSlug) await renderWorker(workerSlug, { noPoll: true, tab: 'refine' });
    else await renderHire({ keepStash: true });
    watchBuilder(workerSlug);
  }

  function manualHireForm(text) {
    const model = state.bootstrap?.settings?.model || '';
    return `<form class="manual-form" data-form="create-worker">
      <div id="create-error" class="form-error" hidden></div>
      <div class="field"><label for="w-name">Name</label><input id="w-name" name="name" required maxlength="120" placeholder="Release notes clerk" autocomplete="off"></div>
      <div class="field"><label for="w-purpose">Job description</label><textarea id="w-purpose" name="purpose" required maxlength="8192" rows="5" placeholder="Turn merged pull requests into a weekly release note in our house style. Keep a list of products and owners.">${esc(text)}</textarea><small>Describe its ongoing responsibility and working standards. Give it individual tasks after hiring.</small></div>
      <details class="sub"><summary>Recurring task (optional)</summary>
        <div class="field"><label for="w-routine">What to do on a schedule</label><textarea id="w-routine" name="routineInstructions" rows="3" placeholder="Every weekday morning, summarise yesterday's merged pull requests."></textarea></div>
        <div class="field-grid">
          <div class="field"><label for="w-every">How often</label><select id="w-every" name="every">${CADENCES.map(([v, l]) => `<option value="${v}">${l}</option>`).join('')}</select></div>
          <div class="field"><label for="w-at">Time</label><input id="w-at" name="at" type="time" value="09:00"></div>
          <div class="field"><label for="w-weekday">Day</label><select id="w-weekday" name="weekday">${WEEKDAYS.map((d, i) => `<option value="${i}" ${i === 1 ? 'selected' : ''}>${d}</option>`).join('')}</select></div>
        </div>
        <div class="field"><label for="w-rcheck">Done check (optional shell, runs from the worker's deliverables folder)</label><input id="w-rcheck" name="routineCheck" placeholder="test -s daily/$(date +%F).md" autocomplete="off"></div>
      </details>
      <details class="sub"><summary>Advanced</summary>
        <div class="field"><label for="w-model">AI model</label><input id="w-model" name="model" value="${esc(model)}" placeholder="provider/model" autocomplete="off"></div>
        <label class="check"><input id="w-net" name="network" type="checkbox"> Allow internet access</label>
      </details>
      <div class="form-row"><small>Creates the worker and checks its structure. Writes stay in work and state; other readable files on this account remain readable. Try a real task next.</small><button class="button primary" type="submit">Hire</button></div>
    </form>`;
  }

  function startHireTimer() {
    stopPolling();
    const timer = document.querySelector('#hire-timer');
    if (!timer) return;
    const tick = () => { timer.textContent = clock(Date.now() - state.hireStarted); };
    tick();
    state.pollTimer = setInterval(tick, 1000);
  }

  async function renderHire(options = {}) {
    await refreshBootstrap();
    const builder = await api('/api/builder');
    const examples = arr((await api('/api/examples')).examples);
    const exampleID = new URLSearchParams(window.location.search).get('example');
    const example = examples.find(item => item.id === exampleID);
    if (exampleID && !example) throw new Error('That example is not available. Open Hire to choose another.');
    if (!state.hireStarted) state.hireStarted = Date.now();
    const draft = options.keepStash || example ? '' : consumeStash();
    const session = builder.session;
    const latest = arr(session?.turns).at(-1);
    const inProgress = turnInProgress(latest);
    const modelReady = Boolean(builder.assistant?.ready);
    const openManual = !modelReady;
    mount('Hire a worker', `<section class="page">
      <header class="page-head"><div><p class="eyebrow">New hire</p><h1>Hire a worker</h1><p class="lede">Describe the job, review its draft, then try a first task. Independent reviews are available when you need another perspective.</p></div><span class="timer" id="hire-timer" title="Time since you started">0:00</span></header>
      ${example ? `<p><a href="/hire" data-link>← Describe another job or choose an example</a></p>${proposalCard({ proposal: example.definition, ready: true }, null, true, false, example)}` : `${builderMarkup(builder, null)}
      <details class="block" data-disclosure="hire-examples"><summary>Try a supplied example</summary><p>Each includes local inputs, a first task, and an executable check. Inspect the job before hiring it.</p><ul class="criteria">${examples.map(item => `<li><a href="/hire?example=${enc(item.id)}" data-link>${esc(item.definition.name)}</a><p>${esc(item.summary)}</p></li>`).join('')}</ul></details>
      <details class="manual-hire block" id="manual-hire" ${openManual ? 'open' : ''}><summary><span><strong>Hire without a draft</strong><small>Use exactly what you write. No AI involved.</small></span></summary>${manualHireForm(!modelReady ? draft : '')}</details>`}
    </section>`, 'hire');
    startHireTimer();
    if (example) return;
    if (draft && modelReady) {
      // A description typed on the Team page is a new hire. An old, unapplied
      // draft is set aside for it; a draft still being written is never killed.
      if (!inProgress) {
        try {
          if (session) await api('/api/builder?worker=', { method: 'DELETE' });
          await sendBuilderMessage('', draft);
        } catch (error) { showToast(error.message, 'danger'); }
        return;
      }
      const textarea = view.querySelector('form[data-form="builder-chat"] textarea');
      if (textarea && !textarea.disabled) textarea.value = draft;
    }
    if (inProgress) watchBuilder('');
    if (!session && modelReady) view.querySelector('form[data-form="builder-chat"] textarea')?.focus();
  }

  async function submitCreate(form, request) {
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
    button.textContent = 'Hiring…';
    try {
      const result = await request('/api/workers', { method: 'POST', body });
      finishHire(result.worker);
    } catch (err) {
      if (err.name === 'AbortError') return;
      error.textContent = err.message + (err.nextAction ? ` ${err.nextAction}` : '');
      error.hidden = false;
      button.disabled = false;
      button.textContent = 'Hire';
    }
  }

  function finishHire(w) {
    const elapsed = state.hireStarted ? clock(Date.now() - state.hireStarted) : '';
    state.hireStarted = 0;
    stopPolling();
    showToast(w.checkState === 'valid' ? `${w.name} is on the team${elapsed ? ` (hired in ${elapsed})` : ''}. Give them their first task.` : `${w.name} was created, but needs a fix before taking tasks.`, w.checkState === 'valid' ? '' : 'warning');
    navigate(`/workers/${enc(w.slug)}`);
  }

  // Worker page --------------------------------------------------------------

  async function loadWorker(slug) {
    const w = await api(`/api/workers/${enc(slug)}`);
    state.cache[slug] = w;
    return w;
  }

  async function renderWorker(slug, options = {}) {
    if (state.ui.slug !== slug) state.ui = freshUI(slug);
    if (options.tab && ['work', 'refine', 'capabilities', 'files', 'details'].includes(options.tab)) state.ui.tab = options.tab;
    const w = await loadWorker(slug);
    if (!state.bootstrap) await refreshBootstrap();
    const [tone, label] = workerStatus(w);
    const archived = Boolean(w.retiredAt || w.retiringAt);
    const canWork = w.enabled && w.checkState === 'valid' && !archived;
    const planOK = Boolean(w.model);
    const tabs = [['work', 'Work', w.requests.length], ...(!archived ? [['refine', 'Improve', 0]] : []), ['capabilities', 'Capabilities', 0], ['files', 'Files', 0], ['details', 'Details', 0]];
    if (archived && state.ui.tab === 'refine') state.ui.tab = 'work';
    mount(w.name, `<section class="page">
      <a class="back" href="/" data-link>← Team</a>
      <header class="worker-head">
        <span class="avatar large ${tone}">${esc(initials(w.name))}</span>
        <div class="worker-head-main"><h1>${esc(w.name)}</h1><p>${esc(w.purpose)}</p></div>
        <div class="worker-head-side"><span id="worker-status">${pill(tone, label)}</span>
          ${!archived ? `<details class="menu"><summary aria-label="Manage ${esc(w.name)}">⋯</summary><div class="menu-list"><button type="button" data-action="toggle-enabled">${w.enabled ? 'Pause new tasks' : 'Resume tasks'}</button><button type="button" class="danger" data-action="retire">Retire ${esc(w.name)}</button></div></details>` : ''}</div>
      </header>
      <div id="worker-notices">${workerNotices(w)}</div>
      ${!archived ? `<section class="composer">
        <form data-form="intake">
          ${arr(w.specialists).length ? `<div class="field"><label for="task-specialist">Who should do this task?</label><select id="task-specialist" name="specialist"><option value="">${esc(w.name)}</option>${w.specialists.map(s => `<option value="${esc(s.name)}">${esc(s.name)} · specialist</option>`).join('')}</select></div><div data-specialist-options hidden><p class="muted">Uses its own instructions, skills, memory, files, and conversation. Include the evidence it should use. Its result appears with these tasks.</p><label class="check"><input name="specialistNetwork" type="checkbox"> Allow internet access for this specialist task</label></div>` : ''}
          <label for="intake-text"><strong>Give ${esc(w.name)} a task</strong></label>
          <textarea id="intake-text" name="text" required maxlength="65536" rows="3" placeholder="What should be done this time? Include the inputs and the result you need." ${canWork ? '' : 'disabled'}></textarea>
          ${w.starterTask && !arr(w.requests).length ? '<p><button class="link" type="button" data-action="sample-task">Use the supplied first task</button></p>' : ''}
          <div class="form-row"><button class="link quiet" type="button" data-action="toggle-options" aria-expanded="${state.ui.options}">Options</button><button class="button primary" type="submit" ${canWork ? '' : 'disabled'}>Send</button></div>
          <div class="composer-options" ${state.ui.options ? '' : 'hidden'}>
            <label class="check"><input type="checkbox" name="plan" ${planOK ? '' : 'disabled'}> Ask AI to separate independent tasks and timings${planOK ? '' : ' (needs a model)'}</label>
            <label class="check">Start no earlier than <input type="datetime-local" name="notBefore"></label>
            <label class="check">Check for this task <input name="check" placeholder="optional shell, runs from the deliverables folder" autocomplete="off"></label>
          </div>
        </form>
      </section>` : ''}
      <nav class="tabs" role="tablist" aria-label="Worker information">${tabs.map(([id, name, count]) => `<button role="tab" id="tab-${id}" aria-controls="tab-panel" data-tab="${id}" tabindex="${state.ui.tab === id ? 0 : -1}" aria-selected="${state.ui.tab === id}" class="${state.ui.tab === id ? 'active' : ''}">${name}${count ? `<em>${count}</em>` : ''}</button>`).join('')}</nav>
      <div class="tab-panel" id="tab-panel" role="tabpanel" aria-labelledby="tab-${state.ui.tab}"></div>
    </section>`, 'team');
    updateTaskTarget();
    await renderTab(w, options);
    if (options.perspective && /^[a-z0-9-]+$/.test(options.perspective) && !archived) {
      const source = await api(`/api/workers/${enc(slug)}/requests/${enc(options.perspective)}`);
      const form = view.querySelector('form[data-form="intake"]');
      const text = form?.querySelector('[name="text"]');
      if (source.request?.specialist && source.request.resultSha256 && text) {
        if (text.value.trim()) showToast('Your current task draft is kept. Clear it before loading a specialist’s findings.', 'warning');
        else {
          text.value = `Consider the perspective from ${source.request.specialist} on task “${source.request.title}”.\nRead ${source.resultPath}.\nEvaluate its findings against the source evidence; explain which findings you use and why.\n\nTask for you:\n`;
          if (form.elements.specialist) form.elements.specialist.value = '';
          form.elements.plan.checked = false;
          form.elements.check.value = '';
          form.elements.notBefore.value = '';
          text.dispatchEvent(new Event('input', { bubbles: true }));
          updateTaskTarget();
          text.focus();
          const url = new URL(window.location.href);
          url.searchParams.delete('perspective');
          window.history.replaceState({}, '', url);
        }
      }
    }
    if (!options.noPoll) poll(() => refreshWorker(slug), 5000);
  }

  function updateTaskTarget() {
    const form = view.querySelector('form[data-form="intake"]');
    if (!form) return;
    const specialist = form.elements.specialist?.value || '';
    const options = form.querySelector('[data-specialist-options]');
    if (options) options.hidden = !specialist;
    const planner = form.elements.plan;
    if (specialist) planner.checked = false;
    planner.disabled = Boolean(specialist) || !state.cache[state.ui.slug]?.model;
    if (specialist && form.elements.check.value) {
      state.ui.options = true;
      form.querySelector('.composer-options').hidden = false;
      form.querySelector('[data-action="toggle-options"]').setAttribute('aria-expanded', 'true');
    }
    form.querySelector('label[for="intake-text"] strong').textContent = `Give ${specialist || state.cache[state.ui.slug]?.name || 'the worker'} a task`;
  }

  function workerNotices(w) {
    if (w.retiredAt) return `<div class="notice"><span>Retired ${esc(formatDate(w.retiredAt))}. Work, results, and history are kept here for reference.</span></div>`;
    if (w.retiringAt) return `<div class="notice warn"><span><strong>Retirement is in progress.</strong> New tasks and schedules are stopped. Active work can finish; any unclear outcome needs your decision.</span></div>`;
    const attention = arr(w.requests).filter((r) => NEEDS_YOU.has(r.state));
    return `${w.checkState !== 'valid' ? `<div class="notice danger"><span><strong>${esc(w.name)} cannot take tasks right now.</strong> ${esc(w.checkMessage || 'The job description did not pass its check.')}</span><button class="link" type="button" data-action="go-tab" data-tab="refine">Fix it →</button></div>` : ''}
      ${!w.enabled && w.checkState === 'valid' ? `<div class="notice"><span>New tasks are paused. Anything already queued still runs.</span><button class="link" type="button" data-action="toggle-enabled">Resume</button></div>` : ''}
      ${attention.length ? `<div class="notice warn"><span><strong>${plural(attention.length, 'task')} need${attention.length === 1 ? 's' : ''} you:</strong> ${attention.slice(0, 3).map((r) => `<a href="/workers/${enc(w.slug)}/requests/${enc(r.id)}" data-link>${esc(r.title)}</a>`).join(', ')}${attention.length > 3 ? ', …' : ''}</span></div>` : ''}
      ${!w.accepted && !w.requests.length && w.enabled ? '<div class="notice"><span><strong>Start with one small, real task.</strong> Include the information it needs and describe a useful result. Review that result before relying on the worker for a larger job.</span></div>' : ''}`;
  }

  // refreshWorker updates the live parts of the page without touching the
  // composer, so a half-written task survives the poll.
  async function refreshWorker(slug) {
    const w = await loadWorker(slug);
    const notices = document.querySelector('#worker-notices');
    if (notices) reading.replace(notices, workerNotices(w), { background: true });
    const workTab = document.querySelector('.tabs button[data-tab="work"]');
    if (workTab) workTab.innerHTML = `Work${w.requests.length ? `<em>${w.requests.length}</em>` : ''}`;
    const status = document.querySelector('#worker-status');
    if (status) status.innerHTML = pill(...workerStatus(w));
    if (state.ui.tab === 'work') await renderTab(w, { background: true });
    try { await refreshBootstrap(); } catch { /* counts refresh on the next tick */ }
  }

  async function renderTab(w, options = {}) {
    const panel = document.querySelector('#tab-panel');
    if (!panel) return;
    const generation = ++state.tabGeneration;
    const tab = state.ui.tab;
    const content = document.createElement('div');
    switch (tab) {
      case 'work': content.innerHTML = renderWorkTab(w); break;
      case 'refine': await renderRefineTab(w, content, options); break;
      case 'capabilities': await renderCapabilitiesTab(w, content); break;
      case 'files': await renderFilesTab(w, content); break;
      case 'details': await renderDetailsTab(w, content); break;
    }
    if (!panel.isConnected || generation !== state.tabGeneration || state.ui.tab !== tab) return;
    reading.replace(panel, content, { background: Boolean(options.background) });
    updatePendingForms();
  }

  function renderWorkTab(w) {
    const requests = arr(w.requests);
    const routines = arr(w.routines);
    const tasks = `<section class="block"><div class="block-head"><h2>Tasks</h2></div>${requests.length ? `<div class="list">${requests.map((r) => taskRow(w, r)).join('')}</div>` : '<div class="list"><p class="empty">No tasks yet. Send the first one above.</p></div>'}</section>`;
    if (w.retiredAt || w.retiringAt) return tasks;
    const recurring = `<section class="block"><div class="block-head"><h2>Recurring</h2>${state.ui.addRoutine ? '' : '<button class="link" type="button" data-action="add-routine">Add a recurring task</button>'}</div>
      ${routines.length || state.ui.addRoutine ? `<div class="list">${routines.map((r) => (r.id === state.ui.editRoutine ? routineForm(r) : routineRow(w, r))).join('')}${state.ui.addRoutine ? routineForm(null) : ''}</div>` : '<p class="muted">Nothing on a schedule. A recurring task runs on its own and shows up above each time.</p>'}</section>`;
    return routines.length || state.ui.addRoutine ? recurring + tasks : tasks + recurring;
  }

  function routineRow(w, r) {
    return `<div class="routine-row ${r.enabled ? '' : 'off'}">
      <div class="routine-main"><strong title="${esc(r.instructions)}">${esc(r.instructions)}</strong><small>${esc(r.cadence)} · ${r.enabled ? `next ${esc(relative(r.nextDue))}` : 'paused'}${r.lastRequest ? ` · <a href="/workers/${enc(w.slug)}/requests/${enc(r.lastRequest)}" data-link>last run</a>` : ''}</small></div>
      <div class="routine-actions"><button class="link" type="button" data-action="routine" data-id="${esc(r.id)}" data-do="run">Run now</button><button class="link" type="button" data-action="routine" data-id="${esc(r.id)}" data-do="${r.enabled ? 'disable' : 'enable'}">${r.enabled ? 'Pause' : 'Resume'}</button><button class="link" type="button" data-action="edit-routine" data-id="${esc(r.id)}">Edit</button><button class="link danger" type="button" data-action="routine" data-id="${esc(r.id)}" data-do="delete">Remove</button></div>
    </div>`;
  }

  function cadenceOptions(selected) {
    const options = CADENCES.slice(1).map(([v, l]) => [v, l]);
    if (selected && !options.some(([v]) => v === selected)) options.push([selected, `Every ${selected}`]);
    return options.map(([v, l]) => `<option value="${esc(v)}" ${v === selected ? 'selected' : ''}>${esc(l)}</option>`).join('');
  }

  function routineForm(r) {
    const editing = Boolean(r);
    return `<form class="routine-form" data-form="${editing ? 'routine-update' : 'routine'}" ${editing ? `data-id="${esc(r.id)}"` : ''}>
      <div class="field"><label for="routine-text">What to do</label><textarea id="routine-text" name="instructions" required rows="2" placeholder="Every weekday morning, summarise yesterday's merged pull requests.">${esc(r?.instructions || '')}</textarea></div>
      <div class="field-grid"><div class="field"><label for="routine-every">How often</label><select id="routine-every" name="every">${cadenceOptions(r?.every || 'daily')}</select></div><div class="field"><label for="routine-at">Time</label><input id="routine-at" name="at" type="time" value="${esc(r?.at || '09:00')}"></div><div class="field"><label for="routine-weekday">Day</label><select id="routine-weekday" name="weekday">${WEEKDAYS.map((d, i) => `<option value="${i}" ${i === (r?.weekday ?? 1) ? 'selected' : ''}>${d}</option>`).join('')}</select></div></div>
      <details class="sub" ${r?.check ? 'open' : ''}><summary>Done check (optional)</summary><div class="field"><label class="sr-only" for="routine-check">Completion check</label><input id="routine-check" name="check" value="${esc(r?.check || '')}" placeholder="shell, runs from the deliverables folder" autocomplete="off"></div></details>
      <div class="form-row"><button class="link quiet" type="button" data-action="cancel-routine">Cancel</button><button class="button primary" type="submit">${editing ? 'Save' : 'Add'}</button></div>
    </form>`;
  }

  // Refine tab: the conversation leads; direct editing of the files, the
  // name, and the done criteria stays available behind one disclosure.
  async function renderRefineTab(w, panel, options = {}) {
    const [def, builder] = await Promise.all([
      api(`/api/workers/${enc(w.slug)}/definition`),
      api(`/api/builder?worker=${enc(w.slug)}`),
    ]);
    state.ui.workerChecks = arr(def.checks);
    const revert = builder?.revertable
      ? `<div class="notice"><span><strong>A change was applied ${esc(relative(builder.revertable.appliedAt))}.</strong> If ${esc(w.name)} now stops or refuses work it used to do, you can put the previous job description back.</span><button class="link" type="button" data-action="builder-revert">Revert</button></div>`
      : '';
    panel.innerHTML = `${builderMarkup(builder, w)}
      ${revert}
      <details class="direct-editor block"><summary><span><strong>Edit the job description by hand</strong><small>Change the files, the name, or the done criteria directly. Saving checks the worker again.</small></span></summary>${directEditorMarkup(w, def)}</details>`;
    if (turnInProgress(arr(builder?.session?.turns).at(-1))) watchBuilder(w.slug);
    if (options.fix) {
      const r = arr(w.requests).find((item) => item.id === options.fix);
      const textarea = panel.querySelector('form[data-form="builder-chat"] textarea');
      if (r && textarea && !textarea.disabled && !textarea.value) {
        const [, label] = TASK_META[r.state] || ['', r.state];
        textarea.value = `The task "${r.title}" ended as "${label}". Change the job description so this kind of task succeeds next time.\n\nThe task was: ${r.text.slice(0, 600)}`;
        textarea.focus();
      }
    }
    panel.querySelector('#definition-editor')?.addEventListener('input', () => { state.ui.dirty = true; });
    panel.querySelectorAll('form[data-form="worker-update"] input, form[data-form="worker-update"] textarea, form[data-form="manual-worker-check"] input, form[data-form="manual-worker-check"] select').forEach((field) => field.addEventListener('input', () => { state.ui.manualDirty = true; }));
  }

  function directEditorMarkup(w, def) {
    const names = ['GOAL.md', 'AGENTS.md', 'SOUL.md', 'PLAN.md', 'MEMORY.md', 'HEARTBEAT.md'];
    const labels = { 'GOAL.md': 'What done looks like', 'AGENTS.md': 'How to work', 'SOUL.md': 'Character', 'PLAN.md': 'Plan', 'MEMORY.md': 'Memory', 'HEARTBEAT.md': 'Heartbeat' };
    if (!(state.ui.definition in def.files)) state.ui.definition = 'GOAL.md';
    const current = state.ui.definition;
    const checks = arr(state.ui.workerChecks);
    const suggestions = arr(state.ui.checkSuggestions);
    const criterion = (check, index, suggested) => `<div class="criterion ${suggested ? 'suggested' : ''}"><div><strong>${esc(check.description || labels[check.kind] || check.kind)}</strong><span>${esc(checkSentence({ ...check, description: '' }).replace(/^[^:]*:\s*/, ''))}</span></div><button class="link ${suggested ? '' : 'danger'}" type="button" data-action="${suggested ? 'dismiss-check-suggestion' : 'remove-worker-check'}" data-index="${index}">${suggested ? 'Dismiss' : 'Remove'}</button></div>`;
    return `<form data-form="worker-update" class="block">
        <h3>Name and summary</h3>
        <div class="field"><label for="d-name">Name</label><input id="d-name" name="name" value="${esc(w.name)}" maxlength="120" required></div>
        <div class="field"><label for="d-purpose">One-line summary</label><textarea id="d-purpose" name="purpose" rows="2" maxlength="8192" required>${esc(w.purpose)}</textarea></div>
        <div class="form-row"><small>Shown on the team page. The worker itself reads the files below.</small><button class="button small" type="submit">Save</button></div>
      </form>
      <div class="block"><h3>Files the worker reads</h3>
        <div class="editor-grid">
          <div class="editor-files">${names.map((n) => `<button type="button" data-action="pick-definition" data-name="${n}" class="${n === current ? 'active' : ''}"><span>${n}</span><small>${n in def.files ? labels[n] : 'absent'}</small></button>`).join('')}</div>
          <div class="editor"><div class="editor-head"><span><strong>${esc(labels[current] || current)}</strong> <small>${esc(current)}</small></span><button class="button small primary" type="button" data-action="save-definition">Save and check</button></div>
            <p class="muted" data-draft-notice hidden>Unsaved draft restored. <button class="link" type="button" data-action="discard-editor" data-editor="definition-editor">Discard draft and load saved file</button></p><textarea id="definition-editor" data-draft="definition:${esc(current)}" data-base-sha256="${esc(def.hashes?.[current] || '')}" aria-label="${esc(current)}">${esc(def.files[current] ?? '')}</textarea></div>
        </div>
      </div>
      <div class="block"><h3>Standards for every task</h3>
        <p class="muted">These automatic checks apply to every task. Put requirements for one particular result on that task instead.</p>
        <div class="criteria-list">${checks.map((check, index) => criterion(check, index, false)).join('')}${suggestions.map((check, index) => criterion(check, index, true)).join('')}</div>
        ${suggestions.length ? `<form data-form="apply-check-suggestions" class="form-row"><small>${esc(state.ui.checkSuggestionNote || 'Suggested from the job description. Nothing is added until you say so.')}</small><button class="button primary small" type="submit">Add ${plural(suggestions.length, 'suggestion')}</button></form>` : state.ui.checkSuggestionNote ? `<p class="muted">${esc(state.ui.checkSuggestionNote)}</p>` : ''}
        <form data-form="check-assistant" class="assist"><label class="sr-only" for="check-guidance">Anything the suggestions should consider</label><input id="check-guidance" name="guidance" maxlength="8192" placeholder="Optional hint, e.g. the weekly note always lands at release-notes/latest.md"><button class="button small" type="submit" ${def.checkAssistant?.ready ? '' : 'disabled'}>${def.checkAssistant?.ready ? 'Suggest done criteria' : 'Needs a model'}</button></form>
        <details class="sub"><summary>Add one yourself</summary><form data-form="manual-worker-check">
          <div class="field-grid">
            <div class="field"><label for="manual-check-kind">Condition</label><select id="manual-check-kind" name="kind"><option value="file_nonempty">A file exists and is not empty</option><option value="text_contains">A file contains exact text</option><option value="minimum_bytes">A file has a minimum size</option></select></div>
            <div class="field"><label for="manual-check-path">File (under the deliverables folder)</label><input id="manual-check-path" name="path" required placeholder="release-notes/latest.md"></div>
          </div>
          <div class="field"><label for="manual-check-description">What it proves</label><input id="manual-check-description" name="description" required maxlength="240" placeholder="The latest release note was written"></div>
          <div class="field-grid">
            <div class="field"><label for="manual-check-text">Exact text (contains-text only)</label><input id="manual-check-text" name="text" maxlength="512"></div>
            <div class="field"><label for="manual-check-bytes">Minimum bytes (size only)</label><input id="manual-check-bytes" name="minimumBytes" type="number" min="1" max="104857600" value="100"></div>
          </div>
          <div class="form-row"><span></span><button class="button small" type="submit">Add</button></div></form></details>
      </div>`;
  }

  async function renderCapabilitiesTab(w, panel) {
    const scope = reading.currentScope;
    const data = await api(`/api/workers/${enc(w.slug)}/capabilities`);
    const history = data.learning?.available ? await api(`/api/workers/${enc(w.slug)}/history`).catch(error => {
      if (error.name === 'AbortError') throw error;
      return { error: error.message };
    }) : {};
    const sessions = arr(history.entries).filter(entry => entry.kind === 'session' && entry.path).reverse();
    const archived = Boolean(w.retiredAt || w.retiringAt);
    const files = items => arr(items).map(item => `<li><button class="link" type="button" data-action="capability-file" data-path="${esc(item.path)}" data-dir="${Boolean(item.dir)}">${esc(item.name)}</button></li>`).join('');
    panel.innerHTML = `<p class="lede">The methods, memory, tools, and other minds ${esc(w.name)} can draw on across tasks.</p>
      ${arr(data.warnings).map(message => `<p class="notice warn">${esc(message)}</p>`).join('')}
      <section class="block"><h2>Skills</h2><p class="muted">Reusable methods, selected when the job calls for them.</p>
        ${arr(data.skills).length ? `<ul class="criteria">${arr(data.skills).map(skill => `<li><button class="link" type="button" data-action="capability-file" data-path="${esc(skill.path)}">${esc(skill.name)}</button><p>${esc(skill.description)}</p></li>`).join('')}</ul>` : '<p>No skills installed yet.</p>'}
        ${!archived ? `<details data-disclosure="new-skill"><summary>Add a skill</summary><p>Use this for a method you already know. Learning from a run is a separate, evidence-based path below.</p><form data-form="create-skill"><div class="field"><label for="skill-name">Name</label><input id="skill-name" name="name" required pattern="[a-z][a-z0-9-]*" maxlength="63" placeholder="review-sales-report"></div><div class="field"><label for="skill-description">When should the worker use it?</label><input id="skill-description" name="description" required maxlength="1024" placeholder="Check a sales report against its source records."></div><div class="field"><label for="skill-method">Method</label><textarea id="skill-method" name="method" required rows="5" maxlength="30000" placeholder="Describe the useful procedure, its inputs, and how to check the result."></textarea></div><button class="button primary" type="submit">Add skill and check</button></form></details>` : ''}
      </section>
      <section class="block"><h2>Memory</h2><p class="muted">Facts and lessons kept between tasks. These are working notes; check their source before treating them as established facts.</p>${arr(data.memory).length ? `<ul class="criteria">${files(data.memory)}</ul>` : '<p>No remembered facts yet.</p>'}
        ${!archived ? `<details data-disclosure="remember-fact"><summary>Give the worker something to remember</summary><form data-form="remember-fact"><div class="field"><label for="memory-name">Topic</label><input id="memory-name" name="name" required pattern="[a-z][a-z0-9-]*" maxlength="63" placeholder="report-audience"></div><div class="field"><label for="memory-content">What should it remember?</label><textarea id="memory-content" name="content" required rows="3" placeholder="Include the fact, where it came from, and when it should be checked again."></textarea></div><button class="button" type="submit">Save memory</button></form></details>` : ''}
      </section>
      <section class="block"><h2>Tools</h2><p class="muted">Programs installed for this worker, alongside the host tools available through Agent.</p>${arr(data.tools).length ? `<ul class="criteria">${files(data.tools)}</ul>` : '<p>No worker-specific tools installed.</p>'}<p class="muted">Writes are limited to its work and state folders. Other readable files on this account are not hidden by that limit. Internet access is ${w.network ? 'allowed' : 'denied'}.</p><details data-disclosure="action-proposals"><summary>Actions needing your approval</summary><p>The worker can prepare external actions for review. Approval and execution use Agent, Action, and May from a terminal.</p><button class="link" type="button" data-action="capability-file" data-path="work/actions" data-dir="true">Read proposed actions</button></details></section>
      <section class="block"><h2>Specialists</h2><p class="muted">A separate perspective for a focused investigation or review. Each has its own standing job, skills, memory, files, and results.</p>${arr(data.specialists).length ? `<ul class="criteria">${arr(data.specialists).map(s => `<li><button class="link" type="button" data-action="capability-file" data-path="${esc(s.path)}" data-dir="true">${esc(s.name)}</button>${!archived && s.dir ? ` · <button class="link" type="button" data-action="assign-specialist" data-name="${esc(s.name)}">Give a task</button>` : ''}</li>`).join('')}</ul>` : '<p>No specialists yet.</p>'}
        ${!archived ? `<details data-disclosure="new-specialist"><summary>Create a specialist</summary><form data-form="create-specialist"><div class="field"><label for="specialist-name">Name</label><input id="specialist-name" name="name" required pattern="[a-z][a-z0-9-]*" maxlength="63" placeholder="source-reviewer"></div><div class="field"><label for="specialist-purpose">Standing job description</label><textarea id="specialist-purpose" name="purpose" required rows="4" maxlength="8192" placeholder="Review claims against their sources. Identify missing evidence and conflicting records; explain uncertainty."></textarea></div><p class="muted">Creates and checks its home without calling a model. Assign a task separately and include the evidence it should examine.</p><button class="button" type="submit">Create specialist</button></form></details>` : ''}
        <details data-disclosure="specialist-method"><summary>How specialist work runs</summary><p>Agent passes only the assigned task, using the specialist’s own context and check. Hire queues it with this worker’s other tasks; one runs at a time. Parent pause and retirement also apply to its specialists.</p><p>Each task uses the parent’s current model and asks separately about internet access. Write limits cover the specialist’s own work and state; other readable files on this account remain readable.</p><code>agent specialist ${esc(w.home)} NAME -- TASK</code></details></section>
      <section class="block"><h2>Learn from experience</h2><p class="muted">A failed attempt followed by a verified success can suggest a reusable lesson. Review the proposed skill before adding it.</p>
        ${!data.learning?.available ? '<p class="notice">Hone is not available in this Bench suite. Install it to inspect verified recoveries and prepare learning proposals.</p>' : ''}
        ${!archived && data.learning?.available ? `<details data-disclosure="learn-from-run"><summary>Find a lesson in a run</summary>${sessions.length ? `<form data-form="learn-from-run"><div class="field"><label for="learn-session">Run to learn from</label><select id="learn-session" name="session" required><option value="">Choose a recorded run</option>${sessions.map(entry => `<option value="${esc(entry.path.split('/').at(-1))}">${esc(formatDate(entry.started))} · ${esc((entry.summary || entry.id || 'Recorded run').slice(0, 120))}</option>`).join('')}</select></div><div class="field"><label for="learn-skill">Skill to create or improve</label><input id="learn-skill" name="skill" required pattern="[a-z][a-z0-9-]*" maxlength="63"></div><div class="actions"><button class="button" type="submit" name="operation" value="inspect">Inspect evidence</button><button class="button primary" type="submit" name="operation" value="prepare">Draft a lesson</button></div><p class="muted">Inspecting makes no model call. Drafting asks for up to three lessons and keeps the current skill unchanged.</p></form>` : `<p>${esc(history.error || 'Run a task first. A recorded recovery can then supply a lesson.')}</p>`}</details>` : ''}
        ${arr(data.learning?.proposals).map(proposal => `<p><button class="link" type="button" data-action="show-learning" data-proposal="${esc(proposal)}">Review ${esc(proposal)}</button></p>`).join('')}
        <div id="learning-result"></div>
      </section>`;
    const selected = reading.selection('learning', scope);
    if (selected) {
      try {
        const result = selected.proposal
          ? await api(`/api/workers/${enc(w.slug)}/learning/${enc(selected.proposal)}`)
          : await api(`/api/workers/${enc(w.slug)}/learning`, { method: 'POST', body: { operation: 'inspect', skill: selected.skill, session: selected.session } });
        panel.querySelector('#learning-result').innerHTML = learningResultMarkup(result, archived);
      } catch (error) {
        if (error.name === 'AbortError') throw error;
        panel.querySelector('#learning-result').innerHTML = `<p class="notice warn">${esc(error.message)}</p>`;
      }
    }
  }

  function learningResultMarkup(result, archived) {
    return `<h3 tabindex="-1">${result.operation === 'inspect' ? 'Recovery evidence' : 'Proposed lesson'}</h3><pre>${esc(result.output || 'No output returned.')}</pre>${result.operation === 'show' && !archived ? `<button class="button primary" type="button" data-action="admit-learning" data-proposal="${esc(result.proposal)}" data-sha256="${esc(result.sha256)}">Add this exact lesson</button>` : ''}`;
  }

  function showLearningResult(result, { focus = false } = {}) {
    const target = document.querySelector('#learning-result');
    if (!target) return;
    const worker = state.cache[state.ui.slug];
    reading.replace(target, learningResultMarkup(result, Boolean(worker?.retiredAt || worker?.retiringAt)));
    if (focus) target.querySelector('h3')?.focus();
  }

  async function renderFilesTab(w, panel) {
    const archived = Boolean(w.retiredAt || w.retiringAt);
    const dir = state.ui.dir || 'work';
    let listing;
    try { listing = await api(`/api/workers/${enc(w.slug)}/files?path=${enc(dir)}`); } catch (error) { listing = { entries: [], error: error.message }; }
    let file = null;
    if (state.ui.file) {
      try { file = await api(`/api/workers/${enc(w.slug)}/files?path=${enc(state.ui.file)}`); } catch (error) { file = { path: state.ui.file, error: error.message }; }
    }
    if (file && (archived || file.truncated)) file.writable = false;
    const crumbs = dir.split('/').filter(Boolean);
    const roots = [['work', 'Deliverables'], ['state', 'Memory'], ['tools', 'Tools'], ['skills', 'Skills']];
    if (w.exampleId) roots.push(['inputs', 'Supplied inputs']);
    panel.innerHTML = `<p class="muted">Deliverables are what ${esc(w.name)} produces; memory is what it keeps between tasks. ${archived ? 'The archive is read only.' : 'Both can be edited here.'}</p><div class="file-layout">
      <div class="file-tree">
        <div class="file-roots">${roots.map(([r, label]) => `<button type="button" data-action="cd" data-path="${r}" class="${dir.split('/')[0] === r ? 'active' : ''}">${label}</button>`).join('')}</div>
        ${crumbs.length > 1 ? `<div class="file-crumbs">${crumbs.map((c, i) => `<button type="button" data-action="cd" data-path="${esc(crumbs.slice(0, i + 1).join('/'))}">${esc(c)}</button>`).join('<span>/</span>')}</div>` : ''}
        ${listing.error ? `<p class="muted">${esc(listing.error)}</p>` : ''}
        <ul>${dir.includes('/') ? `<li><button type="button" data-action="cd" data-path="${esc(crumbs.slice(0, -1).join('/'))}"><span>↑</span><span>..</span><small></small></button></li>` : ''}
        ${arr(listing.entries).map((e) => `<li><button type="button" data-action="${e.dir ? 'cd' : 'open'}" data-path="${esc(e.path)}" class="${state.ui.file === e.path ? 'active' : ''}"><span>${e.dir ? '▸' : '·'}</span><span>${esc(e.name)}${e.dir ? '/' : ''}</span><small>${e.dir ? '' : `${e.size} B`}</small></button></li>`).join('')}
        ${!arr(listing.entries).length && !listing.error ? '<li><small>Empty</small></li>' : ''}</ul>
        ${!archived ? `<form data-form="new-file"><input name="path" placeholder="${esc(dir)}/notes.md" aria-label="New file path" autocomplete="off"><div class="form-row"><span></span><button class="button small" type="submit">New file</button></div></form>` : ''}
      </div>
      <div class="file-view">${file ? (file.error ? `<p class="muted">${esc(file.error)}</p>` : `<div class="file-view-head"><code>${esc(file.path)}</code><div class="actions">${file.writable ? `<button class="button small primary" type="button" data-action="save-file">Save</button><button class="button small danger-ghost" type="button" data-action="delete-file">Delete</button>` : pill('quiet', 'read only')}</div></div>
        ${file.binary ? `<p class="muted">Binary file, ${file.size} bytes.</p>` : `<p class="muted" data-draft-notice hidden>Unsaved draft restored. <button class="link" type="button" data-action="discard-editor" data-editor="file-editor">Discard draft and load saved file</button></p><textarea id="file-editor" data-draft="file:${esc(file.path)}" data-base-sha256="${esc(file.contentSha256 || '')}" aria-label="${esc(file.path)}" ${file.writable ? '' : 'readonly'}>${esc(file.content)}</textarea>${file.truncated ? '<p class="muted">Showing the first 512 KiB. This partial preview cannot be edited.</p>' : ''}`}`) : '<p class="muted">Pick a file on the left.</p>'}</div>
    </div>`;
    panel.querySelector('#file-editor')?.addEventListener('input', () => { state.ui.dirty = true; });
  }

  async function renderDetailsTab(w, panel) {
    const [def, history] = await Promise.all([
      api(`/api/workers/${enc(w.slug)}/definition`).catch((error) => ({ error: error.message })),
      api(`/api/workers/${enc(w.slug)}/history`).catch((error) => ({ error: error.message })),
    ]);
    const archived = Boolean(w.retiredAt || w.retiringAt);
    const receipt = w.receipt || {};
    const entries = arr(history?.entries);
    panel.innerHTML = `<section class="block settings-card"><h2>Settings</h2>
        ${archived ? `<p>AI model: <strong>${esc(w.model || 'none')}</strong></p><p>Internet access: ${w.network ? 'allowed' : 'denied'}</p>` : `<form data-form="worker-model" class="inline-form"><label for="wm-model"><strong>AI model</strong></label><input id="wm-model" name="model" value="${esc(w.model || '')}" placeholder="provider/model" autocomplete="off"><button class="button small" type="submit">Change</button></form>
        <p class="muted">New tasks use this model. Tasks already queued keep the one they started with.</p>
        <label class="check"><input type="checkbox" data-action="toggle-network" ${w.network ? 'checked' : ''}> Allow internet access</label>`}
      </section>
      <section class="block settings-card"><h2>Under the hood</h2>
        <dl class="facts">
          <dt>Folder</dt><dd><code>${esc(w.home)}</code></dd>
          <dt>Hired</dt><dd>${esc(formatDate(w.createdAt))}</dd>
          <dt>Last check</dt><dd>${esc(receipt.message || '—')} · ${esc(formatDate(receipt.checkedAt))}</dd>
          <dt>Permissions</dt><dd>${esc(receipt.authority || 'writes limited to deliverables and memory')}${w.network ? ' · internet allowed' : ''}</dd>
          <dt>Definition digest</dt><dd><code>${esc(receipt.definitionSha256 || '—')}</code></dd>
          <dt>Check digest</dt><dd><code>${esc(receipt.checkSha256 || '—')}</code></dd>
        </dl>
        <p class="muted">Every worker is an ordinary <code>agent</code> home. From a terminal: <code>agent show ${esc(w.home)}</code></p>
        <div class="tech"><details><summary>Done check script (bin/check)</summary><pre>${esc(def?.check || def?.error || '')}</pre></details>
        ${arr(def?.drafting).length ? `<details data-disclosure="applied-drafting"><summary>Job description history and drafting effort</summary><p>${w.accepted || 0} accepted results and ${arr(w.requests).filter(r => r.revisionOf).length} revision tasks across this worker’s history. Compare similar tasks when judging a drafting method.</p><table><thead><tr><th>Applied</th><th>Method</th><th>Follow-up messages</th><th>AI calls</th><th>Drafting time</th></tr></thead><tbody>${def.drafting.map(record => `<tr><td>${esc(formatDate(record.appliedAt))}${record.revertedAt ? '<br>Reverted' : ''}<br><small>${esc(record.model)}</small></td><td>${arr(record.modes).map(mode => mode === 'single' ? 'Single author' : 'Independent reviews').join(', ')}</td><td>${record.followups}</td><td>${record.effort?.askCalls ?? 'Not recorded'}</td><td>${record.effort ? clock(record.effort.elapsedMs) : 'Not recorded'}</td></tr>`).join('')}</tbody></table><p>Elapsed time covers drafting turns, including failed attempts. Time spent reviewing between turns is separate. These counts do not prove that one method gives better results.</p></details>` : ''}
        <details><summary>agent show</summary><pre>${esc(def?.show || def?.error || '')}</pre></details>
        <details><summary>Run history (${entries.length})</summary>${entries.length ? `<table><thead><tr><th>Session</th><th>Detail</th></tr></thead><tbody>${entries.slice().reverse().map((e) => `<tr><th>${esc(e.session || e.path || e.id || e.line || '')}</th><td><code>${esc(JSON.stringify(e).slice(0, 300))}</code></td></tr>`).join('')}</tbody></table>` : `<p class="muted">${esc(history?.error || 'No runs recorded yet.')}</p>`}</details></div>
      </section>`;
  }

  // Task page ----------------------------------------------------------------

  function attemptLabel(a) {
    if (a.status === 'running') return ['active', 'running'];
    if (a.status === 'done') return ['positive', 'finished'];
    const exit = a.exit;
    if (exit === 2) return ['warning', 'stopped early'];
    if (exit === 20) return ['quiet', 'did not start'];
    if (exit === 1) return ['danger', 'program or check failed'];
    if (exit === 124) return ['danger', 'timed out'];
    if (exit === 125) return ['danger', 'boundary'];
    if (a.status === 'unknown') return ['danger', 'outcome unclear'];
    return ['quiet', a.status || 'ended'];
  }

  async function renderTask(slug, id, options = {}) {
    const full = new URLSearchParams(window.location.search).get('full') === '1';
    const data = await api(`/api/workers/${enc(slug)}/requests/${enc(id)}${full ? '?full=1' : ''}`);
    if (state.ui.slug !== slug) state.ui = freshUI(slug);
    const r = data.request;
    const w = data.worker;
    const job = data.job;
    const attempts = arr(data.attempts);
    const children = arr(data.children);
    const plan = data.plan;
    const [tone, label, sentence] = TASK_META[r.state] || ['quiet', r.state, ''];
    const actions = [];
    const act = (text, doWhat, cls = 'button', extra = '') => `<button class="${cls}" type="button" data-action="request" data-do="${doWhat}" ${extra}>${text}</button>`;
    if (['queued', 'scheduled'].includes(r.state)) actions.push(act('Cancel', 'cancel', 'button danger-ghost'));
    if (r.state === 'unsubmitted' && w.enabled) actions.push(act('Queue saved task', 'submit', 'button primary'));
    if (r.state === 'unfinished') actions.push(act('Continue', 'retry', 'button primary'));
    if (['broken', 'failed', 'timed-out', 'boundary'].includes(r.state)) actions.push(act('Try again', 'retry', 'button primary'));
    if (r.state === 'unknown') {
      if (arr(job?.check_argv).length) actions.push(act('Check the result', 'resolve', 'button primary', 'data-decision="done"'));
      actions.push(act('Run it again', 'resolve', 'button', 'data-decision="retry"'));
      actions.push(act('Mark failed', 'resolve', 'button danger-ghost', 'data-decision="fail"'));
    }
    if (NEEDS_YOU.has(r.state) && !w.retiredAt && !w.retiringAt) actions.push(`<a class="button" href="/workers/${enc(w.slug)}?tab=refine&fix=${enc(r.id)}" data-link>Improve ${esc(w.name)}</a>`);
    if (['done', 'review', 'accepted', 'changes-requested', 'cancelled', 'failed', 'broken', 'timed-out', 'boundary', 'unfinished'].includes(r.state) && r.runs && w.enabled) actions.push(act('Give this task again', 'rerun', 'button'));
    const origin = r.specialist ? `perspective from ${r.specialist}` : r.revisionOf ? 'a revision you requested' : r.source.startsWith('routine:') ? 'from a recurring task' : r.source.startsWith('plan:') ? 'one step of a larger task' : r.source.startsWith('rerun:') ? 'run again' : 'from you';
    const html = `<section class="page">
      <a class="back" href="/workers/${enc(w.slug)}" data-link>← ${esc(w.name)}</a>
      <header class="page-head"><div><p class="eyebrow">Task</p><h1>${esc(r.title)}</h1><p class="lede">${pill(tone, label)} ${esc(sentence)}${r.state === 'scheduled' ? ` Starts ${esc(relative(r.notBefore))} (${esc(formatDate(r.notBefore))}).` : ''}</p></div><div class="actions">${actions.join('')}</div></header>
      ${r.specialist ? `<p class="notice">Assigned to <strong>${esc(r.specialist)}</strong>. Its own job description and checks apply. You decide which findings to use in ${esc(w.name)}’s work.</p>` : ''}
      ${r.specialist && r.resultSha256 && ['review', 'accepted', 'changes-requested', 'revision-sent'].includes(r.state) && w.enabled && !w.retiredAt && !w.retiringAt ? `<p><a class="button" href="/workers/${enc(w.slug)}?perspective=${enc(r.id)}" data-link>Use this perspective in a task for ${esc(w.name)}</a></p>` : ''}
      ${r.revisionOf ? `<p><a href="/workers/${enc(w.slug)}/requests/${enc(r.revisionOf)}" data-link>Original result and your feedback</a></p>` : ''}
      ${r.revisionId ? `<p><a class="button primary" href="/workers/${enc(w.slug)}/requests/${enc(r.revisionId)}" data-link>Follow revision task</a></p>` : ''}
      ${r.state === 'unknown' ? `<div class="notice danger"><span>Look at the result and the log below, and at anything the task may have changed elsewhere, before deciding. <strong>Check the result</strong> runs the task’s completion check; <strong>run it again</strong> continues the same conversation; <strong>mark failed</strong> records it and stops.</span></div>` : ''}
      ${r.state === 'unfinished' ? `<div class="notice warn"><span>Continue picks up the same conversation, so the worker keeps what it already did.</span></div>` : ''}
      <section class="block"><div class="block-head"><h2>Result</h2>${data.result ? `<small>${esc(formatDate(r.updatedAt))}</small>` : ''}</div>
        ${data.result ? `<div class="result-box prose">${md(data.result.content)}</div>${data.result.truncated ? `<p class="notice warn">This is a partial preview. ${!full && data.result.size <= 33554432 ? `<a href="/workers/${enc(w.slug)}/requests/${enc(id)}?full=1" data-link>Read the complete result before reviewing it</a>.` : 'The result exceeds the 32 MiB review limit. Ask for a smaller summary to review.'}</p>` : ''}` : `<div class="list"><p class="empty">Nothing written yet. A task counts as done only once ${esc(r.specialist || w.name)} writes a result and it passes the done check.</p></div>`}</section>
      ${r.reviewStale ? '<div class="notice warn"><span>This result changed after your last review. Read it again before accepting it.</span></div>' : ''}
      ${r.review && !r.reviewStale ? `<section class="block"><h2>Your review</h2><p>${r.review.decision === 'accepted' ? 'Accepted' : 'Changes requested'} ${esc(formatDate(r.review.reviewedAt))}.</p>${r.review.note ? `<p class="prose-plain">${esc(r.review.note)}</p>` : ''}${r.state === 'changes-requested' && w.enabled ? '<button class="button primary" type="button" data-action="revise-result">Send a revision task</button>' : ''}</section>` : ''}
      ${['review', 'changes-requested'].includes(r.state) && !w.retiredAt && !w.retiringAt && r.resultSha256 ? `<section class="block review-box"><h2>Is this useful?</h2><p>The automatic checks passed. Your acceptance records that this result meets your needs.</p><form data-form="result-review"><details data-disclosure="result-feedback"><summary>Leave feedback or request a change</summary><div class="field"><label for="result-feedback">What should change?</label><textarea id="result-feedback" name="note" rows="3" maxlength="8192" placeholder="Be specific: what is missing, wrong, or needs a different approach?"></textarea></div><button class="button" type="submit" name="decision" value="changes-requested">Save feedback</button></details><div class="form-row"><button class="button primary" type="submit" name="decision" value="accepted">Accept result</button></div></form></section>` : ''}
      <details class="block" data-disclosure="task-checks"><summary>What the automatic checks cover</summary><p>${esc(data.evidence?.scope || '')}</p>${data.evidence?.notice ? `<p class="notice warn">${esc(data.evidence.notice)}</p>` : ''}${data.evidence?.standardCheck ? `<ul class="criteria"><li>A nonempty written result for this task.</li>${arr(data.evidence?.checks).map(c => `<li>${esc(checkSentence(c))}</li>`).join('')}${r.check ? `<li>Your task-specific check: <code>${esc(r.check)}</code></li>` : ''}</ul>` : data.evidence?.recorded ? '<p>The run used a custom executable check. Read its recorded script below to see what it established.</p>' : ''}${arr(data.evidence?.runs).map((run) => `<details data-disclosure="run-definition-${esc(run.id)}"><summary>Job description used ${esc(formatDate(run.startedAt))}</summary><h3>${esc(run.definition.name)}</h3><p>${esc(run.definition.purpose)}</p><div class="prose">${md(run.definition.files.goal)}</div><details><summary>Working instructions</summary><pre>${esc(run.definition.files.agents)}</pre></details>${run.checkScript ? `<details><summary>Recorded completion check</summary><pre>${esc(run.checkScript)}</pre></details>` : ''}<small>Definition: <code>${esc(run.definitionSha256)}</code><br>Check: <code>${esc(run.checkSha256)}</code></small></details>`).join('')}</details>
      ${children.length ? `<section class="block"><div class="block-head"><h2>Steps</h2><small>${plan?.fallback ? 'one step, no AI planning' : plan?.model ? `planned by ${esc(plan.model)}` : ''}</small></div><div class="list">${children.map((c) => taskRow({ slug: w.slug }, c)).join('')}</div></section>` : ''}
      <section class="block"><div class="block-head"><h2>What you asked</h2><small>${esc(formatDate(r.createdAt))} · ${esc(origin)}</small></div><p class="prose-plain">${esc(r.text)}</p>${r.check ? `<p class="muted">Done check: <code>${esc(r.check)}</code></p>` : ''}</section>
      ${attempts.length ? `<section class="block"><div class="block-head"><h2>What happened</h2><small>${plural(attempts.length, 'attempt')}</small></div>${attempts.map((a) => {
        const [aTone, aLabel] = attemptLabel(a);
        return `<article class="attempt"><header><strong>Attempt ${a.number}</strong>${pill(aTone, aLabel)}<small>${esc(formatDate(a.startedAt))}${validDate(a.finishedAt) ? ` → ${esc(formatDate(a.finishedAt))}` : ''}</small>${a.note ? `<small>${esc(a.note)}</small>` : ''}${a.truncated ? pill('warning', 'log truncated') : ''}</header>
          <details data-disclosure="attempt-${a.number}-log" ${NEEDS_YOU.has(r.state) && a.number === attempts.length ? 'open' : ''}><summary>Worker's log</summary><pre class="log">${esc(a.stderr || '(empty)')}</pre></details>
          <details data-disclosure="attempt-${a.number}-output"><summary>Output</summary><pre class="log light">${esc(a.stdout || '(empty)')}</pre></details></article>`;
      }).join('')}</section>` : ''}
      <details class="tech block"><summary>Under the hood</summary>
        <dl class="facts"><dt>Model</dt><dd>${esc(r.model || 'none')}</dd><dt>Job</dt><dd>${job ? `${esc(job.id)} · ${esc(job.status)}` : 'no job; this task only groups its steps'}</dd>${r.exit !== undefined && r.exit !== null ? `<dt>Exit</dt><dd>${esc(r.exit)}</dd>` : ''}</dl>
        <details><summary>Exact command</summary><pre>${esc(arr(data.argv).map((s) => (/\s/.test(s) ? `'${s.replaceAll("'", "'\\''")}'` : s)).join(' '))}</pre></details>
        <details><summary>Request file (REQUEST.md)</summary><pre>${esc(data.requestFile)}</pre></details>
      </details>
    </section>`;
    // Review actions must use the bytes actually displayed. A background
    // replacement may wait for an active text selection, keeping the old view.
    if (mount(r.title, html, 'team', { background: Boolean(options.noPoll) })) state.task = data;
    if (!options.noPoll) poll(() => renderTask(slug, id, { noPoll: true }), ['queued', 'scheduled', 'running', 'waiting'].includes(r.state) ? 4000 : 8000);
  }

  // Settings -----------------------------------------------------------------

  async function renderSettings() {
    const data = await refreshBootstrap();
    const runtime = data.runtime;
    const model = runtime.model || {};
    const runner = data.runner || {};
    mount('Settings', `<section class="page">
      <header class="page-head"><div><h1>Settings</h1></div></header>
      <section class="settings-card"><h2>AI model</h2>
        <p class="muted">${esc(model.message || 'Choose the model your workers think with.')}${model.nextAction ? ` ${esc(model.nextAction)}` : ''}</p>
        <form data-form="settings" class="inline-form"><label class="sr-only" for="s-model">Model</label><input id="s-model" name="model" value="${esc(data.settings?.model || '')}" placeholder="openai-codex/gpt-5.6-sol" list="model-hints" autocomplete="off"><datalist id="model-hints"><option value="openai-codex/gpt-5.6-sol"><option value="openai-codex/gpt-5.4-mini"><option value="anthropic/"><option value="openai/"><option value="gemini/"><option value="openrouter/"></datalist><button class="button primary small" type="submit">Save</button><button class="button small" type="button" data-action="prove-model" ${model.model ? '' : 'disabled'}>Test</button></form>
        ${arr(model.proved).length ? `<p class="muted">Tested and working: ${model.proved.map((item) => `${esc(item.model)} (${esc(formatDate(item.at))})`).join(', ')}.</p>` : '<p class="muted">Hire tests a model with one small call the first time it is needed. Credentials come from the environment Hire was started in.</p>'}
      </section>
      <section class="settings-card"><h2>Work</h2>
        <p class="muted">${runner.paused ? 'All work is paused. Tasks wait until you resume.' : `Tasks run ${runner.workers} at a time; ${runner.active || 0} running now.`}</p>
        <button class="button small" type="button" data-action="runner" data-paused="${runner.paused ? 'false' : 'true'}">${runner.paused ? 'Resume work' : 'Pause all work'}</button>
        ${arr(runner.errors).length ? `<pre class="log light">${esc(runner.errors.join('\n'))}</pre>` : ''}
      </section>
      <details class="tech block"><summary>Under the hood</summary>
        <dl class="facts"><dt>Programs</dt><dd>${runtime.ready ? 'every required program answered' : 'a required program is missing'}</dd><dt>Data folder</dt><dd><code>${esc(runtime.dataRoot)}</code></dd><dt>Hire</dt><dd><code>${esc(runtime.executable)}</code></dd><dt>Jobs</dt><dd>${esc(runtime.jobs)}</dd><dt>Clock</dt><dd>${esc(runtime.location)} · checked ${esc(formatDate(runtime.checkedAt))}</dd></dl>
        <table><thead><tr><th>Program</th><th>Version</th><th>Status</th></tr></thead><tbody>${arr(runtime.tools).map((t) => `<tr><th>${esc(t.name)}<br><small>${esc(t.purpose || '')}</small></th><td>${esc(t.version || '—')}<br><small>${esc(t.path || '')}</small></td><td>${t.ok ? pill('positive', 'ready') : pill(t.required ? 'danger' : 'quiet', t.message || 'not installed')}</td></tr>`).join('')}</tbody></table>
        <details><summary>Confinement (cage status)</summary><pre>${esc(runtime.cage || 'cage status unavailable')}</pre></details>
        <details><summary>Environment passed to runs</summary><pre>${esc(arr(runtime.passedEnvironment).join(' ' ))}</pre></details>
      </details>
    </section>`, 'settings');
  }

  // Actions ------------------------------------------------------------------

  view.addEventListener('keydown', (event) => {
    const textarea = event.target.closest('form[data-form="builder-chat"] textarea, form[data-form="intake"] textarea, form[data-form="quick-hire"] textarea');
    if (!textarea || event.key !== 'Enter' || (!event.metaKey && !event.ctrlKey)) return;
    event.preventDefault();
    textarea.form.requestSubmit();
  });

  view.addEventListener('change', async (event) => {
    if (event.target.id === 'task-specialist') { updateTaskTarget(); return; }
    const toggle = event.target.closest('input[data-action="toggle-network"]');
    if (!toggle) return;
    const slug = state.ui.slug;
    const here = currentView();
    const network = toggle.checked;
    toggle.disabled = true;
    try {
      await api(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { network } });
      requireCurrent(here);
      showToast(network ? 'Internet access allowed for new tasks.' : 'Internet access turned off for new tasks.');
      await loadWorker(slug);
    } catch (error) {
      if (error.name === 'AbortError' || !here()) return;
      toggle.checked = !network;
      showToast(error.message, 'danger');
    } finally { toggle.disabled = false; }
  });

  view.addEventListener('submit', async (event) => {
    const form = event.target.closest('form[data-form]');
    if (!form) return;
    event.preventDefault();
    const kind = form.dataset.form;
    const slug = state.ui.slug;
    const submission = reading.formSubmission(form);
    const here = currentView();
    const clearsDraft = ['example-hire', 'create-worker', 'create-specialist', 'create-skill', 'remember-fact', 'result-review', 'intake', 'routine', 'routine-update', 'new-file', 'worker-update', 'worker-model', 'settings'].includes(kind);
    const request = async (path, options) => {
      const result = await api(path, options);
      if (clearsDraft) reading.forgetForm(form, submission);
      requireCurrent(here);
      return result;
    };
    const pendingKey = `${submission.scope}:${reading.formKey(form)}`;
    if (state.pendingForms.has(pendingKey)) return;
    const managedPending = ['example-hire', 'create-specialist', 'create-skill', 'remember-fact', 'learn-from-run', 'result-review', 'new-file'].includes(kind);
    if (managedPending) {
      const operation = event.submitter?.value || '';
      const label = kind === 'learn-from-run' ? (operation === 'prepare' ? 'Drafting lesson…' : 'Inspecting evidence…') : 'Saving…';
      state.pendingForms.set(pendingKey, { value: operation, label });
      updatePendingForms();
    }
    try {
      if (kind === 'quick-hire') {
        const text = String(new FormData(form).get('text') || '').trim();
        if (!text) return;
        stash(text);
        state.hireStarted = Date.now();
        navigate('/hire');
        return;
      }
      if (kind === 'builder-chat') {
        if (state.ui.dirty || state.ui.manualDirty) {
          showToast('Save or discard your direct edits first.', 'warning');
          return;
        }
        const data = new FormData(form);
        const workerSlug = String(data.get('workerSlug') || '');
        const message = String(data.get('message') || '').trim();
        if (!message) return;
        const button = form.querySelector('button[type="submit"]');
        button.disabled = true;
        button.textContent = 'Sending…';
        try {
          await sendBuilderMessage(workerSlug, message, data.get('reviewTeam') === 'on' ? 'review-team' : 'single');
        } finally {
          if (button.isConnected) { button.disabled = false; button.textContent = 'Send'; }
        }
        return;
      }
      if (kind === 'create-worker') return await submitCreate(form, request);
      if (kind === 'example-hire') {
        const result = await request('/api/workers', { method: 'POST', body: { name: String(new FormData(form).get('name') || ''), exampleId: form.dataset.id } });
        showToast('Example worker hired. Its supplied task is ready to try.');
        navigate(`/workers/${enc(result.worker.slug)}`);
        return;
      }
      if (kind === 'create-specialist') {
        await request(`/api/workers/${enc(slug)}/specialists`, { method: 'POST', body: Object.fromEntries(new FormData(form)) });
        await renderWorker(slug, { tab: 'capabilities' });
        requireCurrent(here);
        showToast('Specialist created. Give it one focused task when you need its perspective.');
        return;
      }
      if (kind === 'create-skill') {
        const body = Object.fromEntries(new FormData(form));
        await request(`/api/workers/${enc(slug)}/skills`, { method: 'POST', body });
        showToast('Skill added. Agent checked the home; try it on a representative task.');
        await renderTab(state.cache[slug]);
        return;
      }
      if (kind === 'remember-fact') {
        const data = new FormData(form);
        const name = String(data.get('name') || '');
        if (!/^[a-z][a-z0-9-]{0,62}$/.test(name)) return;
        await request(`/api/workers/${enc(slug)}/files`, { method: 'PUT', body: { path: `state/kv/${name}.md`, content: String(data.get('content') || ''), createOnly: true } });
        showToast('Memory saved.');
        await renderTab(state.cache[slug]);
        return;
      }
      if (kind === 'learn-from-run') {
        const data = new FormData(form);
        const operation = event.submitter?.value || 'inspect';
        const result = await api(`/api/workers/${enc(slug)}/learning`, { method: 'POST', body: { operation, skill: data.get('skill'), session: data.get('session') } });
        reading.select('learning', operation === 'prepare' ? { proposal: result.proposal } : { skill: data.get('skill'), session: data.get('session') }, submission.scope);
        requireCurrent(here);
        if (operation === 'prepare') {
          await renderTab(state.cache[slug]);
          requireCurrent(here);
          document.querySelector('#learning-result h3')?.focus();
          showToast('Lesson prepared. Review the exact skill change below.');
        } else showLearningResult(result, { focus: true });
        return;
      }
      if (kind === 'result-review') {
        const task = state.task;
        const decision = event.submitter?.value || 'accepted';
        const note = String(new FormData(form).get('note') || '').trim();
        if (decision === 'changes-requested' && !note) {
          form.querySelector('details').open = true;
          form.querySelector('textarea').focus();
          showToast('Explain what needs to change.', 'warning');
          return;
        }
        await request(`/api/workers/${enc(task.worker.slug)}/requests/${enc(task.request.id)}/review`, {
          method: 'POST', body: { decision, note, resultSha256: task.request.resultSha256, jobUpdatedUs: task.job.updated_us },
        });
        showToast(decision === 'accepted' ? 'Result accepted.' : 'Feedback saved. Send a revision task when you are ready.');
        await renderTask(task.worker.slug, task.request.id);
        return;
      }
      if (kind === 'intake') {
        const data = new FormData(form);
        const notBefore = data.get('notBefore') ? new Date(String(data.get('notBefore'))).toISOString() : '';
        const body = { text: String(data.get('text') || '').trim(), check: String(data.get('check') || '').trim(), plan: data.get('plan') === 'on', notBefore, title: '', specialist: String(data.get('specialist') || ''), specialistNetwork: data.get('specialistNetwork') === 'on' };
        const button = form.querySelector('button[type="submit"]');
        button.disabled = true;
        button.textContent = body.plan ? 'Planning…' : 'Sending…';
        try {
          const result = await request(`/api/workers/${enc(slug)}/requests`, { method: 'POST', body });
          const steps = arr(result.actions).length;
          const routines = arr(result.routines).length;
          showToast(steps > 1 || routines ? `Sent as ${plural(steps, 'step')}${routines ? ` and ${plural(routines, 'recurring task')}` : ''}.` : 'Task sent.', result.warnings?.length ? 'warning' : '');
          arr(result.warnings).forEach((warning) => showToast(warning, 'warning'));
          state.ui.tab = 'work';
          state.ui.options = false;
          await renderWorker(slug);
        } finally { if (button.isConnected) { button.disabled = false; button.textContent = 'Send'; } }
        return;
      }
      if (kind === 'routine' || kind === 'routine-update') {
        const data = new FormData(form);
        const body = { title: '', instructions: String(data.get('instructions') || ''), every: String(data.get('every')), at: String(data.get('at') || '09:00'), weekday: Number(data.get('weekday') || 1), check: String(data.get('check') || '') };
        if (kind === 'routine') await request(`/api/workers/${enc(slug)}/routines`, { method: 'POST', body });
        else await request(`/api/workers/${enc(slug)}/routines/${enc(form.dataset.id)}/update`, { method: 'POST', body });
        state.ui.editRoutine = '';
        state.ui.addRoutine = false;
        showToast(kind === 'routine' ? 'Recurring task added.' : 'Recurring task saved.');
        await renderWorker(slug);
        return;
      }
      if (kind === 'new-file') {
        const path = String(new FormData(form).get('path') || '').trim();
        if (!path) return;
        await request(`/api/workers/${enc(slug)}/files`, { method: 'PUT', body: { path, content: '', createOnly: true } });
        state.ui.file = path;
        state.ui.dir = path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : state.ui.dir;
        await renderTab(state.cache[slug]);
        return;
      }
      if (kind === 'check-assistant') {
        if (state.ui.dirty) {
          showToast('Save the file you are editing first, so the suggestions read what you see.', 'warning');
          return;
        }
        const data = new FormData(form);
        const button = form.querySelector('button[type="submit"]');
        button.disabled = true;
        button.textContent = 'Looking…';
        try {
          const result = await request(`/api/workers/${enc(slug)}/checks/suggest`, { method: 'POST', body: { guidance: String(data.get('guidance') || '').trim() } });
          state.ui.checkSuggestions = arr(result.checks);
          state.ui.checkSuggestionNote = result.note || '';
          showToast(result.checks.length ? `${plural(result.checks.length, 'suggestion')} to review.` : 'No reliable extra criteria were found.', result.checks.length ? '' : 'warning');
          await renderTab(state.cache[slug]);
          view.querySelector('.criteria-list')?.scrollIntoView({ behavior: 'smooth', block: 'center' });
        } finally {
          if (button.isConnected) { button.disabled = false; button.textContent = 'Suggest done criteria'; }
        }
        return;
      }
      if (kind === 'apply-check-suggestions' || kind === 'manual-worker-check') {
        if (state.ui.dirty) {
          showToast('Save or discard the file you are editing first.', 'warning');
          return;
        }
        let checks;
        if (kind === 'apply-check-suggestions') {
          checks = [...arr(state.ui.workerChecks), ...arr(state.ui.checkSuggestions)];
        } else {
          const data = new FormData(form);
          checks = [...arr(state.ui.workerChecks), {
            kind: String(data.get('kind') || ''),
            path: String(data.get('path') || '').trim(),
            description: String(data.get('description') || '').trim(),
            text: String(data.get('text') || '').trim(),
            minimumBytes: Number(data.get('minimumBytes') || 0),
          }];
        }
        const result = await request(`/api/workers/${enc(slug)}/checks`, { method: 'PUT', body: { checks } });
        state.ui.checkSuggestions = [];
        state.ui.checkSuggestionNote = '';
        state.ui.manualDirty = false;
        showToast(result.receipt.valid ? 'Done criteria updated.' : `Saved, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
        await renderWorker(slug, { noPoll: false });
        return;
      }
      if (kind === 'worker-update') {
        const data = new FormData(form);
        await request(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { name: String(data.get('name') || ''), purpose: String(data.get('purpose') || '') } });
        showToast('Name and summary saved.');
        state.ui.dirty = false;
        state.ui.manualDirty = false;
        await renderWorker(slug);
        return;
      }
      if (kind === 'worker-model') {
        const model = String(new FormData(form).get('model') || '').trim();
        await request(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { model } });
        showToast(`New tasks will use ${model || 'no model'}.`);
        await renderWorker(slug);
        return;
      }
      if (kind === 'settings') {
        const model = String(new FormData(form).get('model') || '').trim();
        const saved = await request('/api/settings', { method: 'POST', body: { model } });
        showToast(!model ? 'Default model cleared.' : saved.model?.state === 'ready' ? `Saved. ${model} is tested and working.` : `Saved. Hire will test ${model} the first time it is needed.`);
        await renderSettings();
      }
    } catch (error) {
      if (error.name === 'AbortError' || !here()) return;
      if (kind === 'learn-from-run') {
        const target = document.querySelector('#learning-result');
        if (target) reading.replace(target, `<p class="notice warn" role="alert">${esc(error.message)}</p>`);
      }
      showToast(error.message + (error.nextAction ? ` ${error.nextAction}` : ''), 'danger');
      if (kind === 'create-worker') {
        const box = document.querySelector('#create-error');
        if (box) { box.textContent = error.message; box.hidden = false; }
      }
    } finally {
      if (managedPending) {
        state.pendingForms.delete(pendingKey);
        updatePendingForms();
      }
    }
  });

  async function switchTab(tab) {
    const navigation = state.navigation;
    state.ui.tab = tab;
    const url = new URL(window.location.href);
    url.searchParams.set('tab', tab);
    window.history.replaceState({}, '', url);
    state.ui.dirty = false;
    state.ui.manualDirty = false;
    document.querySelectorAll('.tabs button').forEach((b) => {
      const active = b.dataset.tab === tab;
      b.classList.toggle('active', active);
      b.setAttribute('aria-selected', String(active));
      b.tabIndex = active ? 0 : -1;
    });
    document.querySelector('#tab-panel')?.setAttribute('aria-labelledby', `tab-${tab}`);
    try {
      await renderTab(state.cache[state.ui.slug]);
      return navigation === state.navigation && state.ui.tab === tab;
    } catch (error) {
      if (error.name === 'AbortError' || navigation !== state.navigation || state.ui.tab !== tab) return false;
      const panel = document.querySelector('#tab-panel');
      if (panel) reading.replace(panel, `<p class="notice warn" role="alert">${esc(error.message)}</p><button class="button" type="button" data-action="go-tab" data-tab="${esc(tab)}">Try loading this tab again</button>`);
      return false;
    }
  }

  view.addEventListener('click', async (event) => {
    const tab = event.target.closest('button[data-tab]');
    if (tab && tab.closest('.tabs')) { await switchTab(tab.dataset.tab); return; }
    const button = event.target.closest('button[data-action]');
    if (!button) return;
    const slug = state.ui.slug || window.location.pathname.split('/')[2] || '';
    const action = button.dataset.action;
    const here = currentView();
    const scope = reading.currentScope;
    const request = async (path, options) => {
      const result = await api(path, options);
      requireCurrent(here);
      return result;
    };
    try {
      switch (action) {
        case 'assign-specialist': {
          const select = view.querySelector('#task-specialist');
          if (!select) break;
          select.value = button.dataset.name;
          select.dispatchEvent(new Event('change', { bubbles: true }));
          view.querySelector('#intake-text')?.focus();
          break;
        }
        case 'sample-task': {
          const task = state.cache[slug]?.starterTask;
          const form = view.querySelector('form[data-form="intake"]');
          const text = form?.querySelector('textarea[name="text"]');
          if (!task || !text) break;
          if (text.value.trim()) {
            showToast('Clear the current draft before loading the supplied task.', 'warning');
            break;
          }
          text.value = task.text;
          form.querySelector('input[name="check"]').value = task.check || '';
          form.querySelector('input[name="plan"]').checked = false;
          form.querySelector('input[name="notBefore"]').value = '';
          text.dispatchEvent(new Event('input', { bubbles: true }));
          state.ui.options = true;
          form.querySelector('.composer-options').hidden = false;
          form.querySelector('[data-action="toggle-options"]').setAttribute('aria-expanded', 'true');
          text.focus();
          break;
        }
        case 'capability-file': {
          const path = button.dataset.path;
          state.ui.file = button.dataset.dir === 'true' ? '' : path;
          state.ui.dir = button.dataset.dir === 'true' ? path : path.slice(0, path.lastIndexOf('/'));
          await switchTab('files');
          break;
        }
        case 'show-learning': {
          const result = await request(`/api/workers/${enc(slug)}/learning/${enc(button.dataset.proposal)}`);
          reading.select('learning', { proposal: result.proposal }, scope);
          showLearningResult(result, { focus: true });
          break;
        }
        case 'admit-learning': {
          if (button.disabled) break;
          button.disabled = true;
          try {
            await api(`/api/workers/${enc(slug)}/learning`, { method: 'POST', body: { operation: 'admit', proposal: button.dataset.proposal, sha256: button.dataset.sha256 } });
            if (reading.selection('learning', scope)?.proposal === button.dataset.proposal) reading.select('learning', null, scope);
            requireCurrent(here);
            showToast('The reviewed lesson was added to the skill. Try it on a similar task next.');
            await renderTab(state.cache[slug]);
          } finally { button.disabled = false; }
          break;
        }
        case 'revise-result': {
          const task = state.task;
          button.disabled = true;
          try {
            const result = await request(`/api/workers/${enc(task.worker.slug)}/requests/${enc(task.request.id)}/revision`, { method: 'POST', body: { reviewId: task.request.review.id } });
            showToast(result.warnings?.length ? result.warnings.join(' ') : 'Revision task sent.', result.warnings?.length ? 'warning' : '');
            navigate(`/workers/${enc(task.worker.slug)}/requests/${enc(result.request.id)}`);
          } finally { button.disabled = false; }
          break;
        }
        case 'go-tab': {
          if (await switchTab(button.dataset.tab)) document.querySelector('#tab-panel')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
          break;
        }
        case 'toggle-options': {
          state.ui.options = !state.ui.options;
          const options = view.querySelector('.composer-options');
          if (options) options.hidden = !state.ui.options;
          button.setAttribute('aria-expanded', String(state.ui.options));
          break;
        }
        case 'builder-apply': {
          if (state.ui.dirty || state.ui.manualDirty) {
            showToast('Save or discard your direct edits first.', 'warning');
            break;
          }
          const builder = button.closest('[data-builder-worker]');
          const workerSlug = builder?.dataset.builderWorker || '';
          button.disabled = true;
          button.textContent = workerSlug ? 'Applying…' : 'Hiring…';
          const result = await request('/api/builder/apply', { method: 'POST', body: { workerSlug } });
          await refreshBootstrap();
          requireCurrent(here);
          if (workerSlug) {
            showToast(`${result.worker.name} updated and checked.`);
            await renderWorker(workerSlug, { tab: 'refine' });
          } else {
            finishHire(result.worker);
          }
          break;
        }
        case 'builder-revert': {
          if (state.ui.dirty || state.ui.manualDirty) {
            showToast('Save or discard your direct edits first.', 'warning');
            break;
          }
          if (!window.confirm('Put back the job description this worker had before the last change?')) break;
          button.disabled = true;
          const result = await request('/api/builder/revert', { method: 'POST', body: { workerSlug: slug } });
          await refreshBootstrap();
          requireCurrent(here);
          showToast(`${result.worker?.name || 'The worker'} is back on its previous job description.`);
          await renderWorker(slug, { tab: 'refine' });
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
            showToast('Save or discard your direct edits first.', 'warning');
            break;
          }
          if (!window.confirm('Start over? This clears the draft conversation. The worker itself is not changed.')) break;
          const builder = button.closest('[data-builder-worker]');
          const workerSlug = builder?.dataset.builderWorker || '';
          await request(`/api/builder?worker=${enc(workerSlug)}`, { method: 'DELETE' });
          showToast('Draft cleared.');
          if (workerSlug) await renderWorker(workerSlug, { noPoll: true, tab: 'refine' });
          else { state.hireStarted = Date.now(); await renderHire(); }
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
        case 'discard-editor': {
          reading.forgetEditor(document.getElementById(button.dataset.editor));
          state.ui.dirty = false;
          await renderTab(state.cache[slug]);
          break;
        }
        case 'save-file': {
          const editor = document.querySelector('#file-editor');
          const submission = reading.editorSubmission(editor);
          const path = state.ui.file;
          const result = await api(`/api/workers/${enc(slug)}/files`, { method: 'PUT', body: { path, content: submission.value, baseSha256: submission.baseSha256 } });
          const remaining = reading.savedEditor(submission, result.contentSha256);
          requireCurrent(here);
          if (editor.isConnected && editor.dataset.baseSha256 === submission.baseSha256) editor.dataset.baseSha256 = result.contentSha256;
          state.ui.dirty = remaining;
          await renderTab(state.cache[slug]);
          requireCurrent(here);
          showToast(`Saved ${path}.${remaining ? ' Your newer edits are still a draft.' : ''}`);
          break;
        }
        case 'delete-file':
          if (!window.confirm(`Delete ${state.ui.file}?`)) return;
          await request(`/api/workers/${enc(slug)}/files?path=${enc(state.ui.file)}`, { method: 'DELETE' });
          state.ui.file = '';
          state.ui.dirty = false;
          await renderTab(state.cache[slug]);
          break;
        case 'pick-definition': {
          state.ui.definition = button.dataset.name;
          state.ui.dirty = false;
          state.ui.manualDirty = false;
          const wasOpen = view.querySelector('.direct-editor')?.open;
          await renderTab(state.cache[slug]);
          const editor = view.querySelector('.direct-editor');
          if (editor && wasOpen) editor.open = true;
          break;
        }
        case 'save-definition': {
          const editor = document.querySelector('#definition-editor');
          const submission = reading.editorSubmission(editor);
          const name = state.ui.definition;
          const result = await api(`/api/workers/${enc(slug)}/definition`, { method: 'PUT', body: { name, content: submission.value, baseSha256: submission.baseSha256 } });
          const remaining = reading.savedEditor(submission, result.contentSha256);
          requireCurrent(here);
          if (editor.isConnected && editor.dataset.baseSha256 === submission.baseSha256) editor.dataset.baseSha256 = result.contentSha256;
          state.ui.dirty = remaining;
          state.ui.manualDirty = false;
          await renderWorker(slug, { tab: 'refine' });
          requireCurrent(here);
          showToast(result.receipt.valid ? `${name} saved; the worker checks out.${remaining ? ' Your newer edits are still a draft.' : ''}` : `Saved, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
          break;
        }
        case 'remove-worker-check': {
          if (state.ui.dirty) {
            showToast('Save or discard the file you are editing first.', 'warning');
            return;
          }
          const index = Number(button.dataset.index);
          const checks = arr(state.ui.workerChecks).filter((_, i) => i !== index);
          const result = await request(`/api/workers/${enc(slug)}/checks`, { method: 'PUT', body: { checks } });
          showToast(result.receipt.valid ? 'Criterion removed.' : `Removed, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
          await renderWorker(slug, { tab: 'refine' });
          break;
        }
        case 'dismiss-check-suggestion': {
          const index = Number(button.dataset.index);
          state.ui.checkSuggestions = arr(state.ui.checkSuggestions).filter((_, i) => i !== index);
          if (!state.ui.checkSuggestions.length) state.ui.checkSuggestionNote = '';
          await renderTab(state.cache[slug]);
          view.querySelector('.direct-editor')?.setAttribute('open', '');
          break;
        }
        case 'add-routine':
          state.ui.addRoutine = true;
          state.ui.editRoutine = '';
          await renderTab(state.cache[slug]);
          view.querySelector('#routine-text')?.focus();
          break;
        case 'edit-routine':
          state.ui.editRoutine = button.dataset.id;
          state.ui.addRoutine = false;
          await renderTab(state.cache[slug]);
          break;
        case 'cancel-routine':
          reading.forgetForm('routine');
          reading.forgetForm('routine-update');
          state.ui.editRoutine = '';
          state.ui.addRoutine = false;
          await renderTab(state.cache[slug]);
          break;
        case 'toggle-enabled': {
          const w = state.cache[slug];
          await request(`/api/workers/${enc(slug)}/enabled`, { method: 'POST', body: { enabled: !w.enabled } });
          showToast(w.enabled ? 'New tasks paused. Anything already queued still runs.' : 'Taking tasks again.');
          await renderWorker(slug);
          break;
        }
        case 'retire': {
          const w = state.cache[slug];
          if (!window.confirm(`Retire ${w.name}? Pending tasks and schedules stop. Active work can finish; unclear outcomes need your decision. Files and history stay available.`)) return;
          const result = await request(`/api/workers/${enc(slug)}`, { method: 'DELETE' });
          showToast(result.worker?.retiredAt ? `${w.name} is retired. Its history is kept here.` : 'Retirement started. Active or uncertain work still needs to be settled.');
          await renderWorker(slug, { tab: 'work' });
          break;
        }
        case 'routine': {
          if (button.dataset.do === 'delete' && !window.confirm('Remove this recurring task?')) return;
          const result = await request(`/api/workers/${enc(slug)}/routines/${enc(button.dataset.id)}/${button.dataset.do}`, { method: 'POST' });
          showToast({ run: 'Started a run.', enable: 'Recurring task resumed.', disable: 'Recurring task paused.', delete: 'Recurring task removed.' }[button.dataset.do] || 'Done.');
          if (button.dataset.do === 'run' && result?.request?.id) navigate(`/workers/${enc(slug)}/requests/${enc(result.request.id)}`);
          else await renderWorker(slug);
          break;
        }
        case 'request': {
          const id = window.location.pathname.split('/')[4];
          const body = button.dataset.do === 'resolve' ? { decision: button.dataset.decision } : undefined;
          const confirmText = { done: 'Run this task’s completion check and record whether it passed?', retry: 'Run this task again with the same conversation?', fail: 'Mark this task as failed?' }[button.dataset.decision];
          if (button.dataset.do === 'resolve' && !window.confirm(confirmText)) return;
          const result = await request(`/api/workers/${enc(slug)}/requests/${enc(id)}/${button.dataset.do}`, { method: 'POST', body });
          if (button.dataset.do === 'rerun') { showToast('Started a new run.'); navigate(`/workers/${enc(slug)}/requests/${enc(result.request.id)}`); return; }
          showToast({ submit: 'Saved task queued.', retry: 'Back in the queue.', cancel: 'Cancelled.', resolve: 'Recorded.' }[button.dataset.do] || 'Done.');
          await renderTask(slug, id);
          break;
        }
        case 'prove-model': {
          button.disabled = true;
          button.textContent = 'Testing…';
          try {
            const result = await request('/api/model/prove', { method: 'POST' });
            showToast(result.proof.ok ? `The model answered: ${result.proof.output}` : `The test failed: ${result.proof.output}`, result.proof.ok ? '' : 'danger');
          } finally { if (here()) await renderSettings(); }
          break;
        }
        case 'runner':
          await request('/api/runner', { method: 'POST', body: { paused: button.dataset.paused === 'true' } });
          await renderSettings();
          break;
      }
    } catch (error) {
      if (error.name === 'AbortError' || !here()) return;
      showToast(error.message + (error.nextAction ? ` ${error.nextAction}` : ''), 'danger');
    }
  });

  window.addEventListener('beforeunload', (event) => {
    if (state.ui.dirty) { event.preventDefault(); event.returnValue = ''; }
  });

  view.addEventListener('keydown', async (event) => {
    const tab = event.target.closest('[role="tab"]');
    if (!tab || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const tabs = [...view.querySelectorAll('[role="tab"]')];
    const index = tabs.indexOf(tab);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1
      : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    if (await switchTab(tabs[next].dataset.tab)) tabs[next].focus();
  });

  route();
})();
