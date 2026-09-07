/* Reusable source picker. Files live on the server; only selected IDs are kept
   in the tab's draft storage. Upload completion survives route changes. */
(() => {
  'use strict';
  const supported = '.txt,.md,.csv,.tsv,.json,.jsonl,.yaml,.yml,.xml,.html,.log,.pdf,.docx,.xlsx,.pptx,.png,.jpg,.jpeg,.gif,.webp';
  const types = new Set(['intake', 'builder-chat', 'create-worker', 'routine', 'routine-update', 'create-skill', 'source-skill', 'result-review', 'remember-fact', 'upload-library', 'skill-improve']);
  window.HireUploads = class {
    constructor({ view, api, esc, scope }) {
      Object.assign(this, { view, api, esc, scope });
      this.cache = new Map(); this.selections = new Map(); this.pending = new Map(); this.messages = new Map();
      this.drafts = new window.HireSourceDrafts(this);
      this.observer = new MutationObserver(() => this.mount());
      this.observer.observe(view, { childList: true, subtree: true });
    }
    key(form) { return `hire.sources:${location.pathname}:${form.dataset.form}:${form.dataset.id || ''}`; }
    ids(form) {
      const key = form.dataset.sourcesKey || this.key(form);
      if (!this.selections.has(key)) {
        let saved = [];
        try { saved = JSON.parse(sessionStorage.getItem(key) || form.dataset.initialSources || '[]'); } catch { /* tab storage unavailable */ }
        this.selections.set(key, Array.isArray(saved) ? saved.filter(id => typeof id === 'string') : []);
      }
      return [...this.selections.get(key)];
    }
    set(form, ids) {
      const key = form.dataset.sourcesKey || this.key(form);
      this.selections.set(key, [...new Set(ids)]);
      try { sessionStorage.setItem(key, JSON.stringify(this.selections.get(key))); } catch { /* private mode */ }
      for (const current of this.matching(form)) this.draw(current);
    }
    matching(form) {
      const key = form.dataset.sourcesKey || this.key(form);
      return [...this.view.querySelectorAll('form[data-sources-key]')].filter(current => current.dataset.sourcesKey === key);
    }
    selected(form) {
      const ids = this.ids(form);
      if (this.pending.get(form.dataset.sourcesKey || this.key(form))) throw new Error('Wait for the selected files to finish uploading.');
      if (ids.some(id => this.cache.get(id)?.state !== 'ready')) throw new Error('Wait for source reading to finish, or remove the source that needs attention.');
      return ids;
    }
    clear(form, sent) { if (JSON.stringify(this.ids(form)) === JSON.stringify(sent)) { this.set(form, []); this.status(form, ''); this.drafts.clear(form); } }
    mount() {
      for (const form of this.view.querySelectorAll('form[data-form]')) {
        if (!types.has(form.dataset.form) || form.querySelector('[data-source-picker]')) continue;
        form.dataset.sourcesKey = this.key(form);
        const box = document.createElement('section'); box.className = 'source-picker'; box.dataset.sourcePicker = '';
        box.innerHTML = `<strong>Source materials</strong><p class="muted">Upload documents or images, then use them for this ${form.dataset.form === 'remember-fact' ? 'memory' : 'work'}. Originals and citations are kept. Images and PDFs are read with AI. Up to 16 files, 16 MiB each.</p><div class="actions"><label class="button source-choose">Upload files<input type="file" multiple accept="${supported}" aria-label="Upload reference files"></label><button class="button" type="button" data-existing>Choose existing</button></div><p class="source-status" role="status"></p><ul class="source-list" data-selected></ul><div data-library hidden></div>`;
        const action = form.querySelector('button[type="submit"]');
        const anchor = action?.closest('.form-row') || action;
        if (anchor && anchor.parentNode === form) form.insertBefore(box, anchor); else form.append(box);
        const fixed = Boolean(form.elements.draftID);
        if (fixed) { box.querySelector('p.muted').textContent = 'Sources used to draft this method. Open them to check the procedure; start a new teaching draft to use different materials.'; box.querySelector('.actions').remove(); }
        box.querySelector('input')?.addEventListener('change', event => { const files = [...event.target.files]; event.target.value = ''; this.upload(form, files); });
        box.addEventListener('dragover', event => { event.preventDefault(); box.classList.add('dragging'); });
        box.addEventListener('dragleave', () => box.classList.remove('dragging'));
        box.addEventListener('drop', event => { event.preventDefault(); if (fixed) return; box.classList.remove('dragging'); this.upload(form, [...event.dataTransfer.files]); });
        box.querySelector('[data-existing]')?.addEventListener('click', () => this.library(form));
        this.drafts.mount(form, box);
        if (form.dataset.form === 'builder-chat' || form.dataset.form === 'source-skill') {
          const hint = document.createElement('p'); hint.className = 'muted';
          hint.textContent = form.dataset.form === 'builder-chat' ? 'Send your direction to have the assistant interpret these materials for the job description. Review the proposed job before applying it.' : 'Describe what the worker should learn, then draft a skill. Review its proposed method and source links before adding it.';
          box.append(hint);
        }
        if (form.dataset.form === 'upload-library') this.useLibrary(form, box);
        this.draw(form); this.status(form, this.messages.get(form.dataset.sourcesKey) || ''); this.refresh(form);
      }
    }
    status(form, text) { this.messages.set(form.dataset.sourcesKey, text); for (const current of this.matching(form)) { const el = current.querySelector('.source-status'); if (el) el.textContent = text; } }
    async refresh(form) {
      if (!form.isConnected) return;
      let waiting = false;
      for (const id of this.ids(form)) {
        if (this.cache.get(id)?.state === 'ready') continue;
        try { const result = await this.api(`/api/uploads/${encodeURIComponent(id)}`); this.cache.set(id, result.upload); waiting ||= result.upload.state === 'processing'; }
        catch (error) { if (error.name === 'AbortError') return; this.cache.set(id, { id, name: 'Source unavailable', state: 'failed', error: error.message }); }
      }
      if (!form.isConnected) return;
      this.draw(form);
      if (!waiting && this.ids(form).length && this.ids(form).every(id => this.cache.get(id)?.state === 'ready') && this.messages.get(form.dataset.sourcesKey)?.startsWith('Reading')) this.status(form, 'Sources ready. They will be included when you submit this form.');
      if (waiting) setTimeout(() => this.refresh(form), 2000);
    }
    async upload(form, files) {
      const key = form.dataset.sourcesKey;
      if (!files.length) return;
      if (this.pending.get(key)) { this.status(form, 'An upload is already in progress.'); return; }
      if (this.ids(form).length + files.length > 16) { this.status(form, 'Choose at most 16 references.'); return; }
      this.pending.set(key, true);
      const slug = form.elements.workerSlug?.value ?? this.scope();
      const failures = [];
      for (const file of files) {
        if (file.size === 0 || file.size > 16 * 1024 * 1024) { failures.push(`${file.name}: choose a nonempty file of at most 16 MiB.`); continue; }
        this.status(form, `Uploading ${file.name}…`);
        try {
          const body = new FormData(); body.append('file', file);
          const { upload } = await this.api(`/api/uploads?worker=${encodeURIComponent(slug)}`, { method: 'POST', body });
          this.cache.set(upload.id, upload); this.set(form, [...this.ids(form), upload.id]);
          this.status(form, upload.state === 'processing' ? 'Reading the source with AI. You can leave this page; the reading continues.' : 'Source ready. It will be included when you submit this form.');
        } catch (error) { failures.push(`${file.name}: ${error.message}`); }
      }
      this.pending.delete(key);
      for (const current of this.matching(form)) this.drafts.ready(current);
      if (failures.length) this.status(form, failures.join(' ' ));
      for (const current of this.matching(form)) this.refresh(current);
    }
    draw(form) {
      const list = form.querySelector('[data-selected]'); if (!list) return;
      this.drafts.ready(form);
      // Avoid replacing focused preview/removal controls during processing polls.
      const rows = this.ids(form).map(id => { const u = this.cache.get(id) || { id, name: 'Loading source…', state: 'loading' }; return `<li><div>${u.mime?.startsWith('image/') ? `<img class="source-thumb" src="/api/uploads/${encodeURIComponent(id)}/original" alt="">` : ''}<span><a href="/sources/${encodeURIComponent(id)}" target="_blank" rel="noopener">${this.esc(u.name)}</a><small>${this.esc(u.state === 'ready' ? 'Ready · source retained' : u.state === 'processing' ? 'Reading with AI…' : u.state === 'failed' ? u.error || 'Reading failed' : 'Loading…')}</small></span></div><div class="actions">${u.state === 'failed' && u.method === 'ask' ? `<button type="button" class="button small" data-retry="${this.esc(id)}">Retry reading</button>` : ''}${form.elements.draftID ? '' : `<button type="button" class="button small" data-remove="${this.esc(id)}" aria-label="Remove ${this.esc(u.name)} from this form">Remove</button>`}</div></li>`; }).join('');
      if (list.innerHTML === rows) return;
      list.innerHTML = rows;
      for (const button of list.querySelectorAll('[data-remove]')) button.addEventListener('click', () => this.set(form, this.ids(form).filter(id => id !== button.dataset.remove)));
      for (const button of list.querySelectorAll('[data-retry]')) button.addEventListener('click', async () => {
        button.disabled = true;
        try { const { upload } = await this.api(`/api/uploads/${encodeURIComponent(button.dataset.retry)}/retry`, { method: 'POST', body: {} }); this.cache.set(upload.id, upload); this.draw(form); this.refresh(form); }
        catch (error) { this.status(form, error.message); button.disabled = false; }
      });
    }
    async library(form) {
      const panel = form.querySelector('[data-library]');
      if (!panel.hidden) { panel.hidden = true; return; }
      panel.hidden = false; panel.textContent = 'Loading saved sources…';
      try {
        const slug = form.elements.workerSlug?.value ?? this.scope();
        const { uploads } = await this.api(`/api/uploads?worker=${encodeURIComponent(slug)}`);
        for (const u of uploads) this.cache.set(u.id, u);
        panel.innerHTML = uploads.length ? `<ul class="source-list">${uploads.map(u => `<li><span>${this.esc(u.name)}<small>${this.esc(u.state)}</small></span><div class="actions"><a href="/sources/${encodeURIComponent(u.id)}" target="_blank" rel="noopener">Preview</a><button type="button" class="button small" data-select="${this.esc(u.id)}" ${u.state !== 'ready' || this.ids(form).includes(u.id) ? 'disabled' : ''}>Attach</button></div></li>`).join('')}</ul>` : '<p>No saved sources yet. Upload a file above.</p>';
        for (const button of panel.querySelectorAll('[data-select]')) button.addEventListener('click', () => { if (this.ids(form).length >= 16) { this.status(form, 'Choose at most 16 references.'); return; } this.set(form, [...this.ids(form), button.dataset.select]); button.disabled = true; });
      } catch (error) { panel.textContent = error.message; }
    }
    useLibrary(form, box) {
      const section = document.createElement('div'); section.className = 'source-interpretation';
      section.innerHTML = '<p>What should these materials help with? Select sources above, then choose a purpose. You will review a draft before saving or assigning anything.</p><div class="actions"><button class="button" type="button" data-use-materials="intake">Prepare a task</button><button class="button" type="button" data-use-materials="source-skill">Teach a skill</button><button class="button" type="button" data-use-materials="remember-fact">Suggest memories</button><button class="button" type="button" data-use-materials="builder-chat">Refine the job</button></div>';
      box.append(section);
      for (const button of section.querySelectorAll('[data-use-materials]')) button.addEventListener('click', () => {
        try {
          const ids = this.selected(form);
          if (!ids.length) throw new Error('Upload or choose at least one source first.');
          const path = `/workers/${encodeURIComponent(this.scope())}`, kind = button.dataset.useMaterials;
          const key = `hire.sources:${path}:${kind}:`;
          let saved = this.selections.get(key);
          if (!saved) { try { saved = JSON.parse(sessionStorage.getItem(key) || '[]'); } catch { saved = []; } }
          const selected = [...new Set([...(saved || []), ...ids])];
          if (selected.length > 16) throw new Error('The destination already has sources selected. Keep the combined selection under 16.');
          this.selections.set(key, selected);
          try { sessionStorage.setItem(key, JSON.stringify(selected)); } catch { /* private mode */ }
          history.pushState({}, '', `${path}?tab=${kind === 'intake' ? 'work' : kind === 'builder-chat' ? 'refine' : 'capabilities'}`);
          dispatchEvent(new PopStateEvent('popstate'));
        } catch (error) { this.status(form, error.message); }
      });
    }
  };
})();
