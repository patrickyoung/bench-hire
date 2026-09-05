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
    toastTimer: null,
    hireStarted: 0,
    ui: freshUI(''),
    cache: {},
  };

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
    done: ['positive', 'Done', 'Finished and checked.'],
    unfinished: ['warning', 'Stopped early', 'The worker ran out of room before finishing. Continue to pick up where it left off.'],
    broken: ['danger', 'Not accepted', 'The worker finished, but the result did not pass the done check.'],
    failed: ['danger', 'Failed', 'The run failed before it produced a result.'],
    'timed-out': ['danger', 'Took too long', 'The run hit its time limit and was stopped.'],
    boundary: ['danger', 'Stopped at a boundary', 'The worker tried something outside its permissions, so the run stopped.'],
    unknown: ['danger', 'Outcome unclear', 'The run started, but nobody recorded how it ended. You decide what happened.'],
    cancelled: ['quiet', 'Cancelled', 'Cancelled before it ran.'],
    planned: ['quiet', 'Planned', 'Split into the steps below.'],
    unsubmitted: ['danger', 'Not started', 'This task never made it into the queue.'],
  };
  const NEEDS_YOU = new Set(['unknown', 'unfinished', 'broken', 'failed', 'timed-out', 'boundary']);

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

  // md renders the small Markdown subset the job description files use:
  // headings, bullets, paragraphs, inline code, and bold. Every character is
  // escaped before any tag is added, so model-written text stays text.
  function md(text) {
    const inline = (s) => esc(s).replace(/`([^`]+)`/g, '<code>$1</code>').replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    let html = '';
    let list = false;
    let para = [];
    const flush = () => { if (para.length) { html += `<p>${inline(para.join(' '))}</p>`; para = []; } };
    const closeList = () => { if (list) { html += '</ul>'; list = false; } };
    for (const raw of String(text || '').split('\n')) {
      const line = raw.trimEnd();
      const heading = line.match(/^(#{1,6})\s+(.*)$/);
      const item = line.match(/^\s*(?:[-*]|\d+\.)\s+(.*)$/);
      if (heading) {
        flush(); closeList();
        const level = Math.min(heading[1].length + 2, 6);
        html += `<h${level}>${inline(heading[2])}</h${level}>`;
      } else if (item) {
        flush();
        if (!list) { html += '<ul>'; list = true; }
        html += `<li>${inline(item[1])}</li>`;
      } else if (!line.trim()) {
        flush(); closeList();
      } else {
        closeList();
        para.push(line.trim());
      }
    }
    flush(); closeList();
    return html || '<p class="muted">Empty.</p>';
  }

  function pill(tone, label) { return `<span class="pill ${tone}">${esc(label)}</span>`; }

  function workerStatus(w) {
    if (w.checkState !== 'valid') return ['danger', 'Needs a fix'];
    if (!w.enabled) return ['quiet', 'Paused'];
    if (w.attention) return ['warning', 'Needs you'];
    if (w.running) return ['active', 'Working'];
    if (w.queued) return ['neutral', 'Work queued'];
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
    document.querySelectorAll('[data-nav]').forEach((item) => item.classList.toggle('active', item.dataset.nav === name));
  }

  function mount(title, html, nav) {
    clearInterval(state.builderTimer);
    state.builderTimer = null;
    announcer.textContent = title;
    document.title = title === 'Team' ? 'Hire' : `${title} · Hire`;
    setNav(nav);
    view.innerHTML = html;
    main.scrollTo({ top: 0 });
    renderChrome();
  }

  function stopPolling() {
    clearInterval(state.pollTimer);
    state.pollTimer = null;
  }

  function editing() {
    const active = document.activeElement;
    return state.ui.dirty || state.ui.manualDirty || state.ui.editRoutine || state.ui.addRoutine
      || (active && view.contains(active) && ['TEXTAREA', 'INPUT', 'SELECT'].includes(active.tagName) && (active.value || active.tagName !== 'TEXTAREA'));
  }

  function poll(fn, ms) {
    stopPolling();
    state.pollTimer = setInterval(async () => {
      if (editing()) return;
      try { await fn(); } catch (error) { console.warn(error); }
    }, ms);
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
    if ((state.ui.dirty || state.ui.manualDirty) && !window.confirm('Leave without saving your changes?')) return;
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
    stopPolling();
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
      if ((match = path.match(/^\/workers\/([a-z0-9-]+)$/))) return await renderWorker(match[1], { tab: params.get('tab') || '', fix: params.get('fix') || '' });
      mount('Not found', `<section class="page"><h1>Nothing here</h1><p class="lede">That address does not name a page.</p><a class="button" href="/" data-link>Back to the team</a></section>`, '');
    } catch (error) {
      mount('Error', `<section class="page"><h1>Hire could not load this page</h1><p class="lede">${esc(error.message)}</p><a class="button" href="/" data-link>Back to the team</a></section>`, '');
    }
  }

  // Team ---------------------------------------------------------------------

  function workerRow(w) {
    const [tone, label] = workerStatus(w);
    const facts = [];
    if (w.running) facts.push(`${w.running} working`);
    if (w.queued) facts.push(`${w.queued} queued`);
    if (w.done) facts.push(`${w.done} done`);
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
    const meta = [showWorker ? w.name : '', origin].filter(Boolean);
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
      <div class="form-row"><small>${ready ? 'Hire drafts a job description with a small review team. Nothing is hired until you approve it.' : 'No AI model is set yet, so Hire will use exactly what you write. <a href="/settings" data-link>Choose a model</a> to get a drafted job description.'}</small><button class="button primary" type="submit">${ready ? 'Draft the job description' : 'Continue'}</button></div>
    </form>`;
  }

  async function renderTeam() {
    const data = await refreshBootstrap();
    const workers = arr(data.workers);
    const attention = arr(data.attention);
    const bySlug = (slug) => workers.find((w) => w.slug === slug) || { slug, name: slug };
    if (!workers.length) {
      mount('Team', `<section class="page"><div class="hero">
        <p class="eyebrow">Hire</p>
        <h1>Hire your first digital worker.</h1>
        <p class="lede">Describe the job in a sentence or two. Hire drafts the job description, you approve it, and the worker starts taking tasks. About five minutes.</p>
        ${quickHireForm()}
      </div></section>`, 'team');
      view.querySelector('#quick-text')?.focus();
      poll(renderTeam, 10000);
      return;
    }
    mount('Team', `<section class="page">
      ${attention.length ? `<section class="block"><div class="block-head"><h2>Needs you</h2>${attention.length > 4 ? `<a href="/needs-you" data-link>All ${attention.length} →</a>` : ''}</div><div class="list">${attention.slice(0, 4).map((r) => taskRow(bySlug(r.workerSlug), r, true)).join('')}</div></section>` : ''}
      <section class="block"><div class="block-head"><h1>Your team</h1></div><div class="list">${workers.map(workerRow).join('')}</div></section>
    </section>`, 'team');
    poll(renderTeam, 8000);
  }

  async function renderAttention() {
    const data = await refreshBootstrap();
    const items = arr(data.attention);
    const workers = arr(data.workers);
    const bySlug = (slug) => workers.find((w) => w.slug === slug) || { slug, name: slug };
    mount('Needs you', `<section class="page">
      <header class="page-head"><div><h1>Needs you</h1><p class="lede">Tasks that stopped and are waiting for a decision. Nothing here is retried on its own.</p></div></header>
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
    const experts = arr(turn.experts).length ? arr(turn.experts) : arr(team?.permanent);
    const reports = arr(turn.reports);
    const states = turn.reviewStates || {};
    const reported = new Set(reports.map((r) => r.expert?.id));
    let line;
    switch (turn.status) {
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
      <details class="progress-team"><summary>Who is reviewing</summary><ul>${roster}</ul><p>Three Bench reviewers check the draft against how workers actually run; one to three specialists cover the job itself. A lead then writes the job description.</p></details></div>`;
  }

  function proposalCard(session, worker, ready, stale) {
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
      <header class="job-head"><div><p class="eyebrow">Job description</p><h2>${esc(p.name || 'Unnamed worker')}</h2><p>${esc(p.purpose || '')}</p></div><div class="pills">${pill(p.network ? 'active' : 'quiet', p.network ? 'Can use the internet' : 'Works offline')}</div></header>
      <section class="job-section"><h3>What done looks like</h3><div class="prose">${md(files.goal)}</div></section>
      <details class="job-more"><summary>How ${esc(p.name || 'the worker')} will work</summary><div class="prose">${md(files.agents)}</div></details>
      ${others.length ? `<details class="job-more"><summary>Other notes</summary>${others.map(([key, label]) => `<h4>${esc(label)}</h4><div class="prose">${md(files[key])}</div>`).join('')}</details>` : ''}
      <section class="job-section"><h3>How Hire knows a task is done</h3><ul class="criteria"><li>A written result for every task.</li>${checks.map((c) => `<li>${esc(checkSentence(c))}</li>`).join('')}</ul></section>
      ${arr(session.changes).length ? `<section class="job-section"><h3>What changed in this draft</h3><ul class="changes">${session.changes.map((c) => `<li>${esc(c)}</li>`).join('')}</ul></section>` : ''}
      ${reports.length ? `<details class="job-more"><summary>Reviewed by ${plural(reports.length, 'specialist')}</summary><ul class="reviewers">${reports.map((r) => `<li><b>${esc(r.expert?.name || 'Reviewer')}</b><span>${esc(r.summary)}</span>${arr(r.risks).length ? `<small>Risks: ${r.risks.map(esc).join(' · ')}</small>` : ''}</li>`).join('')}</ul></details>` : ''}
      ${locked}
      <footer class="job-actions"><button class="button primary" type="button" data-action="builder-apply" ${ready && !stale ? '' : 'disabled'}>${esc(applyLabel)}</button><button class="link quiet" type="button" data-action="builder-reset">Start over</button><small>${worker ? 'Applies this job description and checks the worker again. You can revert afterwards.' : 'Creates the worker with exactly this job description and checks that it is ready.'}</small></footer>
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
      ? `<div class="chat" role="log" aria-live="polite">${earlier.length ? `<details class="earlier"><summary>${plural(earlier.length, 'earlier message')}</summary>${earlier.map(chatBubble).join('')}</details>` : ''}${recent.map(chatBubble).join('')}${inProgress ? chatBubble({ role: 'user', text: latestTurn.message }) + progressCard(latestTurn, team) : ''}${failed ? `<div class="notice danger"><span><strong>That draft did not go through.</strong> ${esc(failed.error || 'The previous draft was kept.')}</span></div>` : ''}</div>`
      : '';
    const proposal = session?.proposal && !inProgress ? proposalCard(session, worker, ready, stale) : '';
    const hint = started
      ? 'Each reply is reviewed again. Nothing changes until you approve.'
      : worker ? 'A small review team reads the current job description and proposes the change. Nothing changes until you apply it.' : 'A small review team drafts the job description. Nothing is hired until you approve it.';
    const composer = `<form class="chat-form" data-form="builder-chat"><input type="hidden" name="workerSlug" value="${esc(scope)}">
      <label class="sr-only" for="builder-message-${esc(scope || 'new')}">${started ? 'Reply' : 'Describe the job'}</label>
      <textarea id="builder-message-${esc(scope || 'new')}" name="message" required maxlength="16384" rows="${started ? 3 : 5}" placeholder="${esc(started ? 'Answer the question, or ask for a change…' : placeholder)}" ${canSend ? '' : 'disabled'}></textarea>
      <div class="form-row"><small>${hint}</small><button class="button primary" type="submit" ${canSend ? '' : 'disabled'}>${inProgress ? 'Drafting…' : started ? 'Send' : worker ? 'Propose the change' : 'Draft the job description'}</button></div></form>`;
    return `<section class="builder" data-builder-worker="${esc(scope)}">${notice}${log}${proposal}${composer}</section>`;
  }

  // watchBuilder polls the turn that runs on the server and re-renders only
  // the conversation, so the rest of the page (and any half-typed task) stays.
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
          if (latest?.status === 'complete') showToast(scope ? 'The proposed change is ready to review.' : 'The job description is ready to review.');
          else if (latest?.status === 'failed') showToast('The draft did not go through. The previous one was kept.', 'warning');
          view.querySelector('.job-card')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
        }
      } catch (error) {
        console.warn(error);
      }
    }, 2500);
  }

  async function sendBuilderMessage(workerSlug, message) {
    await api('/api/builder/chat', { method: 'POST', body: { workerSlug, message } });
    if (workerSlug) await renderWorker(workerSlug, { noPoll: true, tab: 'refine' });
    else await renderHire({ keepStash: true });
    watchBuilder(workerSlug);
  }

  function manualHireForm(text) {
    const model = state.bootstrap?.settings?.model || '';
    return `<form class="manual-form" data-form="create-worker">
      <div id="create-error" class="form-error" hidden></div>
      <div class="field"><label for="w-name">Name</label><input id="w-name" name="name" required maxlength="120" placeholder="Release notes clerk" autocomplete="off"></div>
      <div class="field"><label for="w-purpose">What this worker does</label><textarea id="w-purpose" name="purpose" required maxlength="8192" rows="5" placeholder="Turn merged pull requests into a weekly release note in our house style. Keep a list of products and owners.">${esc(text)}</textarea><small>Say what done looks like and what must never change. This becomes the job description.</small></div>
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
      <div class="form-row"><small>Creates the worker and checks that it is ready. No AI involved.</small><button class="button primary" type="submit">Hire</button></div>
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
    if (!state.hireStarted) state.hireStarted = Date.now();
    const draft = options.keepStash ? '' : consumeStash();
    const session = builder.session;
    const latest = arr(session?.turns).at(-1);
    const inProgress = turnInProgress(latest);
    const modelReady = Boolean(builder.assistant?.ready);
    const openManual = !modelReady;
    mount('Hire a worker', `<section class="page">
      <header class="page-head"><div><p class="eyebrow">New hire</p><h1>Hire a worker</h1><p class="lede">Describe the job. Hire drafts a job description with a small review team; you approve it, and the worker is ready for tasks.</p></div><span class="timer" id="hire-timer" title="Time since you started">0:00</span></header>
      ${builderMarkup(builder, null)}
      <details class="manual-hire block" id="manual-hire" ${openManual ? 'open' : ''}><summary><span><strong>Hire without a draft</strong><small>Use exactly what you write. No AI involved.</small></span></summary>${manualHireForm(!modelReady ? draft : '')}</details>
    </section>`, 'hire');
    startHireTimer();
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
    button.textContent = 'Hiring…';
    try {
      const result = await api('/api/workers', { method: 'POST', body });
      finishHire(result.worker);
    } catch (err) {
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
    if (options.tab && ['work', 'refine', 'files', 'details'].includes(options.tab)) state.ui.tab = options.tab;
    const w = await loadWorker(slug);
    if (!state.bootstrap) await refreshBootstrap();
    const [tone, label] = workerStatus(w);
    const canWork = w.enabled && w.checkState === 'valid';
    const planOK = Boolean(w.model);
    const tabs = [['work', 'Work', w.requests.length], ['refine', 'Refine', 0], ['files', 'Files', 0], ['details', 'Details', 0]];
    mount(w.name, `<section class="page">
      <a class="back" href="/" data-link>← Team</a>
      <header class="worker-head">
        <span class="avatar large ${tone}">${esc(initials(w.name))}</span>
        <div class="worker-head-main"><h1>${esc(w.name)}</h1><p>${esc(w.purpose)}</p></div>
        <div class="worker-head-side"><span id="worker-status">${pill(tone, label)}</span>
          <details class="menu"><summary aria-label="More actions">⋯</summary><div class="menu-list"><button type="button" data-action="toggle-enabled">${w.enabled ? 'Pause new tasks' : 'Resume tasks'}</button><button type="button" class="danger" data-action="retire">Retire ${esc(w.name)}</button></div></details></div>
      </header>
      <div id="worker-notices">${workerNotices(w)}</div>
      <section class="composer">
        <form data-form="intake">
          <label class="sr-only" for="intake-text">Give ${esc(w.name)} a task</label>
          <textarea id="intake-text" name="text" required maxlength="65536" rows="3" placeholder="Give ${esc(w.name)} something to do…" ${canWork ? '' : 'disabled'}></textarea>
          <div class="form-row"><button class="link quiet" type="button" data-action="toggle-options" aria-expanded="${state.ui.options}">Options</button><button class="button primary" type="submit" ${canWork ? '' : 'disabled'}>Send</button></div>
          <div class="composer-options" ${state.ui.options ? '' : 'hidden'}>
            <label class="check"><input type="checkbox" name="plan" ${planOK ? 'checked' : 'disabled'}> Let AI split this into steps and timings${planOK ? '' : ' (needs a model)'}</label>
            <label class="check">Start no earlier than <input type="datetime-local" name="notBefore"></label>
            <label class="check">Done check <input name="check" placeholder="optional shell, runs from the deliverables folder" autocomplete="off"></label>
          </div>
        </form>
      </section>
      <nav class="tabs" role="tablist">${tabs.map(([id, name, count]) => `<button role="tab" data-tab="${id}" aria-selected="${state.ui.tab === id}" class="${state.ui.tab === id ? 'active' : ''}">${name}${count ? `<em>${count}</em>` : ''}</button>`).join('')}</nav>
      <div class="tab-panel" id="tab-panel"></div>
    </section>`, 'team');
    await renderTab(w, options);
    if (!options.noPoll) poll(() => refreshWorker(slug), 5000);
  }

  function workerNotices(w) {
    const attention = arr(w.requests).filter((r) => NEEDS_YOU.has(r.state));
    return `${w.checkState !== 'valid' ? `<div class="notice danger"><span><strong>${esc(w.name)} cannot take tasks right now.</strong> ${esc(w.checkMessage || 'The job description did not pass its check.')}</span><button class="link" type="button" data-action="go-tab" data-tab="refine">Fix it →</button></div>` : ''}
      ${!w.enabled && w.checkState === 'valid' ? `<div class="notice"><span>New tasks are paused. Anything already queued still runs.</span><button class="link" type="button" data-action="toggle-enabled">Resume</button></div>` : ''}
      ${attention.length ? `<div class="notice warn"><span><strong>${plural(attention.length, 'task')} need${attention.length === 1 ? 's' : ''} you:</strong> ${attention.slice(0, 3).map((r) => `<a href="/workers/${enc(w.slug)}/requests/${enc(r.id)}" data-link>${esc(r.title)}</a>`).join(', ')}${attention.length > 3 ? ', …' : ''}</span></div>` : ''}`;
  }

  // refreshWorker updates the live parts of the page without touching the
  // composer, so a half-written task survives the poll.
  async function refreshWorker(slug) {
    const w = await loadWorker(slug);
    const notices = document.querySelector('#worker-notices');
    if (notices) notices.innerHTML = workerNotices(w);
    const workTab = document.querySelector('.tabs button[data-tab="work"]');
    if (workTab) workTab.innerHTML = `Work${w.requests.length ? `<em>${w.requests.length}</em>` : ''}`;
    const status = document.querySelector('#worker-status');
    if (status) status.innerHTML = pill(...workerStatus(w));
    if (state.ui.tab === 'work') await renderTab(w);
    try { await refreshBootstrap(); } catch { /* counts refresh on the next tick */ }
  }

  async function renderTab(w, options = {}) {
    const panel = document.querySelector('#tab-panel');
    if (!panel) return;
    switch (state.ui.tab) {
      case 'work': panel.innerHTML = renderWorkTab(w); break;
      case 'refine': await renderRefineTab(w, panel, options); break;
      case 'files': await renderFilesTab(w, panel); break;
      case 'details': await renderDetailsTab(w, panel); break;
    }
  }

  function renderWorkTab(w) {
    const requests = arr(w.requests);
    const routines = arr(w.routines);
    const tasks = `<section class="block"><div class="block-head"><h2>Tasks</h2></div>${requests.length ? `<div class="list">${requests.map((r) => taskRow(w, r)).join('')}</div>` : '<div class="list"><p class="empty">No tasks yet. Send the first one above.</p></div>'}</section>`;
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
      <div class="field-grid"><div class="field"><label>How often</label><select name="every">${cadenceOptions(r?.every || 'daily')}</select></div><div class="field"><label>Time</label><input name="at" type="time" value="${esc(r?.at || '09:00')}"></div><div class="field"><label>Day</label><select name="weekday">${WEEKDAYS.map((d, i) => `<option value="${i}" ${i === (r?.weekday ?? 1) ? 'selected' : ''}>${d}</option>`).join('')}</select></div></div>
      <details class="sub" ${r?.check ? 'open' : ''}><summary>Done check (optional)</summary><div class="field"><input name="check" value="${esc(r?.check || '')}" placeholder="shell, runs from the deliverables folder" autocomplete="off"></div></details>
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
            <textarea id="definition-editor" aria-label="${esc(current)}">${esc(def.files[current] ?? '')}</textarea></div>
        </div>
      </div>
      <div class="block"><h3>How Hire knows a task is done</h3>
        <p class="muted">Every task must end with a written result. Add evidence this worker should always produce, and Hire refuses a task as done without it.</p>
        <div class="criteria-list">${checks.map((check, index) => criterion(check, index, false)).join('')}${suggestions.map((check, index) => criterion(check, index, true)).join('')}</div>
        ${suggestions.length ? `<form data-form="apply-check-suggestions" class="form-row"><small>${esc(state.ui.checkSuggestionNote || 'Suggested from the job description. Nothing is added until you say so.')}</small><button class="button primary small" type="submit">Add ${plural(suggestions.length, 'suggestion')}</button></form>` : state.ui.checkSuggestionNote ? `<p class="muted">${esc(state.ui.checkSuggestionNote)}</p>` : ''}
        <form data-form="check-assistant" class="assist"><label class="sr-only" for="check-guidance">Anything the suggestions should consider</label><input id="check-guidance" name="guidance" maxlength="8192" placeholder="Optional hint, e.g. the weekly note always lands at release-notes/latest.md"><button class="button small" type="submit" ${def.checkAssistant?.ready ? '' : 'disabled'}>${def.checkAssistant?.ready ? 'Suggest done criteria' : 'Needs a model'}</button></form>
        <details class="sub"><summary>Add one yourself</summary><form data-form="manual-worker-check">
          <div class="field-grid">
            <div class="field"><label>Condition</label><select name="kind"><option value="file_nonempty">A file exists and is not empty</option><option value="text_contains">A file contains exact text</option><option value="minimum_bytes">A file has a minimum size</option></select></div>
            <div class="field"><label>File (under the deliverables folder)</label><input name="path" required placeholder="release-notes/latest.md"></div>
          </div>
          <div class="field"><label>What it proves</label><input name="description" required maxlength="240" placeholder="The latest release note was written"></div>
          <div class="field-grid">
            <div class="field"><label>Exact text (contains-text only)</label><input name="text" maxlength="512"></div>
            <div class="field"><label>Minimum bytes (size only)</label><input name="minimumBytes" type="number" min="1" max="104857600" value="100"></div>
          </div>
          <div class="form-row"><span></span><button class="button small" type="submit">Add</button></div></form></details>
      </div>`;
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
    const roots = [['work', 'Deliverables'], ['state', 'Memory'], ['tools', 'Tools'], ['skills', 'Skills']];
    panel.innerHTML = `<p class="muted">Deliverables are what ${esc(w.name)} produces; memory is what it keeps between tasks. Both can be edited here.</p><div class="file-layout">
      <div class="file-tree">
        <div class="file-roots">${roots.map(([r, label]) => `<button type="button" data-action="cd" data-path="${r}" class="${dir.split('/')[0] === r ? 'active' : ''}">${label}</button>`).join('')}</div>
        ${crumbs.length > 1 ? `<div class="file-crumbs">${crumbs.map((c, i) => `<button type="button" data-action="cd" data-path="${esc(crumbs.slice(0, i + 1).join('/'))}">${esc(c)}</button>`).join('<span>/</span>')}</div>` : ''}
        ${listing.error ? `<p class="muted">${esc(listing.error)}</p>` : ''}
        <ul>${dir.includes('/') ? `<li><button type="button" data-action="cd" data-path="${esc(crumbs.slice(0, -1).join('/'))}"><span>↑</span><span>..</span><small></small></button></li>` : ''}
        ${arr(listing.entries).map((e) => `<li><button type="button" data-action="${e.dir ? 'cd' : 'open'}" data-path="${esc(e.path)}" class="${state.ui.file === e.path ? 'active' : ''}"><span>${e.dir ? '▸' : '·'}</span><span>${esc(e.name)}${e.dir ? '/' : ''}</span><small>${e.dir ? '' : `${e.size} B`}</small></button></li>`).join('')}
        ${!arr(listing.entries).length && !listing.error ? '<li><small>Empty</small></li>' : ''}</ul>
        <form data-form="new-file"><input name="path" placeholder="${esc(dir)}/notes.md" aria-label="New file path" autocomplete="off"><div class="form-row"><span></span><button class="button small" type="submit">New file</button></div></form>
      </div>
      <div class="file-view">${file ? (file.error ? `<p class="muted">${esc(file.error)}</p>` : `<div class="file-view-head"><code>${esc(file.path)}</code><div class="actions">${file.writable ? `<button class="button small primary" type="button" data-action="save-file">Save</button><button class="button small danger-ghost" type="button" data-action="delete-file">Delete</button>` : pill('quiet', 'read only')}</div></div>
        ${file.binary ? `<p class="muted">Binary file, ${file.size} bytes.</p>` : `<textarea id="file-editor" aria-label="${esc(file.path)}" ${file.writable ? '' : 'readonly'}>${esc(file.content)}</textarea>${file.truncated ? '<p class="muted">Showing the first 512 KiB.</p>' : ''}`}`) : '<p class="muted">Pick a file on the left.</p>'}</div>
    </div>`;
    panel.querySelector('#file-editor')?.addEventListener('input', () => { state.ui.dirty = true; });
  }

  async function renderDetailsTab(w, panel) {
    const [def, history] = await Promise.all([
      api(`/api/workers/${enc(w.slug)}/definition`).catch((error) => ({ error: error.message })),
      api(`/api/workers/${enc(w.slug)}/history`).catch((error) => ({ error: error.message })),
    ]);
    const receipt = w.receipt || {};
    const entries = arr(history?.entries);
    panel.innerHTML = `<section class="block settings-card"><h2>Settings</h2>
        <form data-form="worker-model" class="inline-form"><label for="wm-model"><strong>AI model</strong></label><input id="wm-model" name="model" value="${esc(w.model || '')}" placeholder="provider/model" autocomplete="off"><button class="button small" type="submit">Change</button></form>
        <p class="muted">New tasks use this model. Tasks already queued keep the one they started with.</p>
        <label class="check"><input type="checkbox" data-action="toggle-network" ${w.network ? 'checked' : ''}> Allow internet access</label>
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
    if (exit === 1) return ['danger', 'not accepted'];
    if (exit === 124) return ['danger', 'timed out'];
    if (exit === 125) return ['danger', 'boundary'];
    if (a.status === 'unknown') return ['danger', 'outcome unclear'];
    return ['quiet', a.status || 'ended'];
  }

  async function renderTask(slug, id, options = {}) {
    const data = await api(`/api/workers/${enc(slug)}/requests/${enc(id)}`);
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
    if (r.state === 'unfinished') actions.push(act('Continue', 'retry', 'button primary'));
    if (['broken', 'failed', 'timed-out', 'boundary'].includes(r.state)) actions.push(act('Try again', 'retry', 'button primary'));
    if (r.state === 'unknown') {
      actions.push(act('It finished, mark done', 'resolve', 'button primary', 'data-decision="done"'));
      actions.push(act('Run it again', 'resolve', 'button', 'data-decision="retry"'));
      actions.push(act('Mark failed', 'resolve', 'button danger-ghost', 'data-decision="fail"'));
    }
    if (NEEDS_YOU.has(r.state)) actions.push(`<a class="button" href="/workers/${enc(w.slug)}?tab=refine&fix=${enc(r.id)}" data-link>Refine ${esc(w.name)}</a>`);
    if (['done', 'cancelled', 'failed', 'broken', 'timed-out', 'boundary', 'unfinished'].includes(r.state) && r.runs) actions.push(act('Run again', 'rerun', 'button'));
    const origin = r.source.startsWith('routine:') ? 'from a recurring task' : r.source.startsWith('plan:') ? 'one step of a larger task' : r.source.startsWith('rerun:') ? 'run again' : 'from you';
    const html = `<section class="page">
      <a class="back" href="/workers/${enc(w.slug)}" data-link>← ${esc(w.name)}</a>
      <header class="page-head"><div><p class="eyebrow">Task</p><h1>${esc(r.title)}</h1><p class="lede">${pill(tone, label)} ${esc(sentence)}${r.state === 'scheduled' ? ` Starts ${esc(relative(r.notBefore))} (${esc(formatDate(r.notBefore))}).` : ''}</p></div><div class="actions">${actions.join('')}</div></header>
      ${r.state === 'unknown' ? `<div class="notice danger"><span>Look at the result and the log below, and at anything the task may have changed elsewhere, before deciding. <strong>Mark done</strong> needs nothing more; <strong>run it again</strong> continues the same conversation; <strong>mark failed</strong> records it and stops.</span></div>` : ''}
      ${r.state === 'unfinished' ? `<div class="notice warn"><span>Continue picks up the same conversation, so the worker keeps what it already did.</span></div>` : ''}
      <section class="block"><div class="block-head"><h2>Result</h2>${data.result ? `<small>${esc(formatDate(r.updatedAt))}</small>` : ''}</div>
        ${data.result ? `<div class="result-box prose">${md(data.result.content)}</div>` : `<div class="list"><p class="empty">Nothing written yet. A task counts as done only once ${esc(w.name)} writes a result and it passes the done check.</p></div>`}</section>
      ${children.length ? `<section class="block"><div class="block-head"><h2>Steps</h2><small>${plan?.fallback ? 'one step, no AI planning' : plan?.model ? `planned by ${esc(plan.model)}` : ''}</small></div><div class="list">${children.map((c) => taskRow({ slug: w.slug }, c)).join('')}</div></section>` : ''}
      <section class="block"><div class="block-head"><h2>What you asked</h2><small>${esc(formatDate(r.createdAt))} · ${esc(origin)}</small></div><p class="prose-plain">${esc(r.text)}</p>${r.check ? `<p class="muted">Done check: <code>${esc(r.check)}</code></p>` : ''}</section>
      ${attempts.length ? `<section class="block"><div class="block-head"><h2>What happened</h2><small>${plural(attempts.length, 'attempt')}</small></div>${attempts.map((a) => {
        const [aTone, aLabel] = attemptLabel(a);
        return `<article class="attempt"><header><strong>Attempt ${a.number}</strong>${pill(aTone, aLabel)}<small>${esc(formatDate(a.startedAt))}${validDate(a.finishedAt) ? ` → ${esc(formatDate(a.finishedAt))}` : ''}</small>${a.note ? `<small>${esc(a.note)}</small>` : ''}${a.truncated ? pill('warning', 'log truncated') : ''}</header>
          <details ${NEEDS_YOU.has(r.state) && a.number === attempts.length ? 'open' : ''}><summary>Worker's log</summary><pre class="log">${esc(a.stderr || '(empty)')}</pre></details>
          <details><summary>Output</summary><pre class="log light">${esc(a.stdout || '(empty)')}</pre></details></article>`;
      }).join('')}</section>` : ''}
      <details class="tech block"><summary>Under the hood</summary>
        <dl class="facts"><dt>Model</dt><dd>${esc(r.model || 'none')}</dd><dt>Job</dt><dd>${job ? `${esc(job.id)} · ${esc(job.status)}` : 'no job; this task only groups its steps'}</dd>${r.exit !== undefined && r.exit !== null ? `<dt>Exit</dt><dd>${esc(r.exit)}</dd>` : ''}</dl>
        <details><summary>Exact command</summary><pre>${esc(arr(data.argv).map((s) => (/\s/.test(s) ? `'${s.replaceAll("'", "'\\''")}'` : s)).join(' '))}</pre></details>
        <details><summary>Request file (REQUEST.md)</summary><pre>${esc(data.requestFile)}</pre></details>
      </details>
    </section>`;
    mount(r.title, html, 'team');
    if (!options.noPoll && ['queued', 'scheduled', 'running', 'waiting'].includes(r.state)) poll(() => renderTask(slug, id, { noPoll: true }), 4000);
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
        <table><thead><tr><th>Program</th><th>Version</th><th>Status</th></tr></thead><tbody>${arr(runtime.tools).map((t) => `<tr><th>${esc(t.name)}</th><td>${esc(t.version || '—')}<br><small>${esc(t.path || '')}</small></td><td>${t.ok ? pill('positive', 'ready') : pill('danger', t.message || 'missing')}</td></tr>`).join('')}</tbody></table>
        <details><summary>Confinement (cage status)</summary><pre>${esc(runtime.cage || 'cage status unavailable')}</pre></details>
        <details><summary>Environment passed to runs</summary><pre>ASK_MODEL ANTHROPIC_API_KEY ANTHROPIC_BASE_URL OPENAI_API_KEY OPENAI_BASE_URL GEMINI_API_KEY GEMINI_BASE_URL OPENROUTER_API_KEY OPENROUTER_BASE_URL OPENAI_CODEX_ACCOUNT_ID
HIRE_AGENT AGENT_PLY AGENT_BRIEF AGENT_CAGE AGENT_ASK AGENT_HONE AGENT_TRAIL</pre></details>
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
    const toggle = event.target.closest('input[data-action="toggle-network"]');
    if (!toggle) return;
    const slug = state.ui.slug;
    try {
      await api(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { network: toggle.checked } });
      showToast(toggle.checked ? 'Internet access allowed for new tasks.' : 'Internet access turned off for new tasks.');
      await loadWorker(slug);
    } catch (error) {
      toggle.checked = !toggle.checked;
      showToast(error.message, 'danger');
    }
  });

  view.addEventListener('submit', async (event) => {
    const form = event.target.closest('form[data-form]');
    if (!form) return;
    event.preventDefault();
    const kind = form.dataset.form;
    const slug = state.ui.slug;
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
          await sendBuilderMessage(workerSlug, message);
        } finally {
          if (button.isConnected) { button.disabled = false; button.textContent = 'Send'; }
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
        button.textContent = body.plan ? 'Planning…' : 'Sending…';
        try {
          const result = await api(`/api/workers/${enc(slug)}/requests`, { method: 'POST', body });
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
        if (kind === 'routine') await api(`/api/workers/${enc(slug)}/routines`, { method: 'POST', body });
        else await api(`/api/workers/${enc(slug)}/routines/${enc(form.dataset.id)}/update`, { method: 'POST', body });
        state.ui.editRoutine = '';
        state.ui.addRoutine = false;
        showToast(kind === 'routine' ? 'Recurring task added.' : 'Recurring task saved.');
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
          showToast('Save the file you are editing first, so the suggestions read what you see.', 'warning');
          return;
        }
        const data = new FormData(form);
        const button = form.querySelector('button[type="submit"]');
        button.disabled = true;
        button.textContent = 'Looking…';
        try {
          const result = await api(`/api/workers/${enc(slug)}/checks/suggest`, { method: 'POST', body: { guidance: String(data.get('guidance') || '').trim() } });
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
        const result = await api(`/api/workers/${enc(slug)}/checks`, { method: 'PUT', body: { checks } });
        state.ui.checkSuggestions = [];
        state.ui.checkSuggestionNote = '';
        state.ui.manualDirty = false;
        showToast(result.receipt.valid ? 'Done criteria updated.' : `Saved, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
        await renderWorker(slug, { noPoll: false });
        return;
      }
      if (kind === 'worker-update') {
        const data = new FormData(form);
        await api(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { name: String(data.get('name') || ''), purpose: String(data.get('purpose') || '') } });
        showToast('Name and summary saved.');
        state.ui.dirty = false;
        state.ui.manualDirty = false;
        await renderWorker(slug);
        return;
      }
      if (kind === 'worker-model') {
        const model = String(new FormData(form).get('model') || '').trim();
        await api(`/api/workers/${enc(slug)}/update`, { method: 'POST', body: { model } });
        showToast(`New tasks will use ${model || 'no model'}.`);
        await renderWorker(slug);
        return;
      }
      if (kind === 'settings') {
        const model = String(new FormData(form).get('model') || '').trim();
        const saved = await api('/api/settings', { method: 'POST', body: { model } });
        showToast(!model ? 'Default model cleared.' : saved.model?.state === 'ready' ? `Saved. ${model} is tested and working.` : `Saved. Hire will test ${model} the first time it is needed.`);
        await renderSettings();
      }
    } catch (error) {
      showToast(error.message + (error.nextAction ? ` ${error.nextAction}` : ''), 'danger');
      if (kind === 'create-worker') {
        const box = document.querySelector('#create-error');
        if (box) { box.textContent = error.message; box.hidden = false; }
      }
    }
  });

  async function switchTab(tab) {
    if ((state.ui.dirty || state.ui.manualDirty) && !window.confirm('Leave without saving your changes?')) return false;
    state.ui.tab = tab;
    state.ui.dirty = false;
    state.ui.manualDirty = false;
    document.querySelectorAll('.tabs button').forEach((b) => {
      const active = b.dataset.tab === tab;
      b.classList.toggle('active', active);
      b.setAttribute('aria-selected', String(active));
    });
    await renderTab(state.cache[state.ui.slug]);
    return true;
  }

  view.addEventListener('click', async (event) => {
    const tab = event.target.closest('button[data-tab]');
    if (tab && tab.closest('.tabs')) { await switchTab(tab.dataset.tab); return; }
    const button = event.target.closest('button[data-action]');
    if (!button) return;
    const slug = state.ui.slug || window.location.pathname.split('/')[2] || '';
    const action = button.dataset.action;
    try {
      switch (action) {
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
          const result = await api('/api/builder/apply', { method: 'POST', body: { workerSlug } });
          await refreshBootstrap();
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
          const result = await api('/api/builder/revert', { method: 'POST', body: { workerSlug: slug } });
          await refreshBootstrap();
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
          await api(`/api/builder?worker=${enc(workerSlug)}`, { method: 'DELETE' });
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
        case 'pick-definition': {
          if ((state.ui.dirty || state.ui.manualDirty) && !window.confirm('Leave without saving your changes?')) return;
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
          const content = document.querySelector('#definition-editor').value;
          const result = await api(`/api/workers/${enc(slug)}/definition`, { method: 'PUT', body: { name: state.ui.definition, content } });
          state.ui.dirty = false;
          state.ui.manualDirty = false;
          showToast(result.receipt.valid ? `${state.ui.definition} saved; the worker checks out.` : `Saved, but ${result.receipt.message}`, result.receipt.valid ? '' : 'danger');
          await renderWorker(slug, { tab: 'refine' });
          break;
        }
        case 'remove-worker-check': {
          if (state.ui.dirty) {
            showToast('Save or discard the file you are editing first.', 'warning');
            return;
          }
          const index = Number(button.dataset.index);
          const checks = arr(state.ui.workerChecks).filter((_, i) => i !== index);
          const result = await api(`/api/workers/${enc(slug)}/checks`, { method: 'PUT', body: { checks } });
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
          state.ui.editRoutine = '';
          state.ui.addRoutine = false;
          await renderTab(state.cache[slug]);
          break;
        case 'toggle-enabled': {
          const w = state.cache[slug];
          await api(`/api/workers/${enc(slug)}/enabled`, { method: 'POST', body: { enabled: !w.enabled } });
          showToast(w.enabled ? 'New tasks paused. Anything already queued still runs.' : 'Taking tasks again.');
          await renderWorker(slug);
          break;
        }
        case 'retire': {
          const w = state.cache[slug];
          if (!window.confirm(`Retire ${w.name}? Queued tasks are cancelled. The worker's folder is kept, nothing is deleted.`)) return;
          await api(`/api/workers/${enc(slug)}`, { method: 'DELETE' });
          showToast(`${w.name} has been retired.`);
          navigate('/');
          break;
        }
        case 'routine': {
          if (button.dataset.do === 'delete' && !window.confirm('Remove this recurring task?')) return;
          const result = await api(`/api/workers/${enc(slug)}/routines/${enc(button.dataset.id)}/${button.dataset.do}`, { method: 'POST' });
          showToast({ run: 'Started a run.', enable: 'Recurring task resumed.', disable: 'Recurring task paused.', delete: 'Recurring task removed.' }[button.dataset.do] || 'Done.');
          if (button.dataset.do === 'run' && result?.request?.id) navigate(`/workers/${enc(slug)}/requests/${enc(result.request.id)}`);
          else await renderWorker(slug);
          break;
        }
        case 'request': {
          const id = window.location.pathname.split('/')[4];
          const body = button.dataset.do === 'resolve' ? { decision: button.dataset.decision } : undefined;
          const confirmText = { done: 'Mark this task as done?', retry: 'Run this task again with the same conversation?', fail: 'Mark this task as failed?' }[button.dataset.decision];
          if (button.dataset.do === 'resolve' && !window.confirm(confirmText)) return;
          const result = await api(`/api/workers/${enc(slug)}/requests/${enc(id)}/${button.dataset.do}`, { method: 'POST', body });
          if (button.dataset.do === 'rerun') { showToast('Started a new run.'); navigate(`/workers/${enc(slug)}/requests/${enc(result.request.id)}`); return; }
          showToast({ retry: 'Back in the queue.', cancel: 'Cancelled.', resolve: 'Recorded.' }[button.dataset.do] || 'Done.');
          await renderTask(slug, id);
          break;
        }
        case 'prove-model': {
          button.disabled = true;
          button.textContent = 'Testing…';
          try {
            const result = await api('/api/model/prove', { method: 'POST' });
            showToast(result.proof.ok ? `The model answered: ${result.proof.output}` : `The test failed: ${result.proof.output}`, result.proof.ok ? '' : 'danger');
          } finally { await renderSettings(); }
          break;
        }
        case 'runner':
          await api('/api/runner', { method: 'POST', body: { paused: button.dataset.paused === 'true' } });
          await renderSettings();
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
