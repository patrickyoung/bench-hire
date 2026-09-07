/* Interpret sources for the current form. Preparing and reviewing a proposal
   never submits the enclosing form. Only the usual save/assign action does. */
(() => {
  'use strict';
  const configs = {
    'remember-fact': { kind: 'memory', field: 'content', label: 'Suggest memories', title: 'Suggested memories', help: 'Extract useful facts, preferences and working rules. Review each suggestion; inferred memories start unselected.' },
    'create-skill': { kind: 'skill', field: 'method', label: 'Draft skill from sources', title: 'Proposed skill', help: 'Turn these materials into steps, inputs and checks for a reusable skill.' },
    intake: { kind: 'task', field: 'text', label: 'Draft task from sources', title: 'Proposed task brief', help: 'Turn these materials and your direction into a clear task brief with a useful deliverable.' },
    routine: { kind: 'routine', field: 'instructions', label: 'Draft recurring instructions', title: 'Proposed recurring instructions', help: 'Prepare repeatable instructions from these materials. Choose the schedule in the form.' },
    'routine-update': { kind: 'routine', field: 'instructions', label: 'Draft recurring instructions', title: 'Proposed recurring instructions', help: 'Prepare repeatable instructions from these materials. Choose the schedule in the form.' },
    'result-review': { kind: 'feedback', field: 'note', label: 'Draft feedback from sources', title: 'Proposed feedback', help: 'Turn corrections or examples into specific feedback for you to review and send.' },
    'create-worker': { kind: 'job', field: 'purpose', label: 'Draft job from sources', title: 'Proposed job description', help: 'Extract responsibilities, working standards and boundaries for this role.' },
  };
  window.HireSourceDrafts = class {
    constructor(uploads) { this.uploads = uploads; this.states = new Map(); this.loading = new Set(); }
    key(form) { return `${form.dataset.sourcesKey || this.uploads.key(form)}:interpretation`; }
    config(form) { return form.elements.draftID ? null : configs[form.dataset.form]; }
    state(form) {
      const key = this.key(form);
      if (!this.states.has(key)) {
        let saved = null;
        try { saved = JSON.parse(sessionStorage.getItem(key) || 'null'); } catch { /* private mode */ }
        this.states.set(key, saved || {});
      }
      return this.states.get(key);
    }
    keep(form, value) {
      this.states.set(this.key(form), value);
      // The durable proposal lives on the server. Keep only its ID and review
      // choices in tab storage, never a second copy of the source material.
      const { id, worker, selected, applied } = value;
      try { sessionStorage.setItem(this.key(form), JSON.stringify({ id, worker, selected, applied })); } catch { /* private mode */ }
    }
    clear(form) {
      this.states.delete(this.key(form));
      try { sessionStorage.removeItem(this.key(form)); } catch { /* private mode */ }
      for (const current of this.uploads.matching(form)) this.draw(current);
    }
    input(form) {
      const config = this.config(form);
      return { name: form.elements.name?.value || '', description: form.elements.description?.value || '', content: form.elements[config.field]?.value || '' };
    }
    mount(form, box) {
      const config = this.config(form);
      if (!config) return;
      const section = document.createElement('div'); section.className = 'source-interpretation';
      section.innerHTML = `<p>${this.uploads.esc(config.help)}</p><div class="actions"><button class="button" type="button" data-prepare-source>${this.uploads.esc(config.label)}</button><button class="link" type="button" data-previous-source>Previous drafts</button></div><div data-source-review aria-live="polite"></div>`;
      box.append(section);
      section.querySelector('[data-prepare-source]').addEventListener('click', () => this.prepare(form));
      section.querySelector('[data-previous-source]').addEventListener('click', () => this.previous(form));
      this.draw(form);
      const state = this.state(form);
      if (state.id && (!state.draft || state.draft.state === 'drafting') && !state.applied) this.refresh(form);
    }
    ready(form) {
      const button = form.querySelector('[data-prepare-source]');
      if (!button) return;
      let ready = false;
      try { ready = this.uploads.selected(form).length > 0; } catch { /* still reading */ }
      button.disabled = !ready || this.state(form).busy || this.state(form).draft?.state === 'drafting';
    }
    async prepare(form) {
      let pending;
      try {
        const uploadIDs = this.uploads.selected(form);
        if (!uploadIDs.length) throw new Error('Upload or choose a source first.');
        const config = this.config(form), input = this.input(form);
        const worker = form.elements.workerSlug?.value ?? this.uploads.scope();
        pending = { busy: true, worker };
        this.keep(form, pending); this.drawAll(form);
        const { draft } = await this.uploads.api('/api/source-drafts', { method: 'POST', body: { kind: config.kind, workerSlug: worker, uploadIDs, input } });
        if (this.state(form) !== pending) return;
        this.keep(form, { id: draft.id, worker, draft }); this.drawAll(form);
        this.refresh(form);
      } catch (error) {
        if (pending && this.state(form) !== pending) return;
        this.keep(form, { ...this.state(form), busy: false, error: error.message }); this.drawAll(form);
      }
    }
    async refresh(form) {
      const state = this.state(form), key = this.key(form);
      if (!state.id || this.loading.has(key)) return;
      this.loading.add(key);
      try {
        const { draft } = await this.uploads.api(`/api/source-drafts/${encodeURIComponent(state.id)}?worker=${encodeURIComponent(state.worker || '')}`);
        if (this.state(form).id !== state.id) return;
        this.keep(form, { ...this.state(form), draft, error: '' }); this.drawAll(form);
      } catch (error) {
        if (error.name !== 'AbortError' && this.state(form).id === state.id) { this.state(form).error = error.message; this.drawAll(form); }
      } finally {
        this.loading.delete(key);
        const latest = this.state(form);
        if (latest.id === state.id && !latest.applied && (!latest.draft || latest.draft.state === 'drafting') && !latest.error) setTimeout(() => {
          const current = this.uploads.matching(form)[0];
          if (current && this.state(current).id === state.id) this.refresh(current);
        }, 2000);
      }
    }
    drawAll(form) { for (const current of this.uploads.matching(form)) this.draw(current); }
    draw(form) {
      const target = form.querySelector('[data-source-review]'), config = this.config(form);
      if (!target || !config) return;
      const state = this.state(form), d = state.draft, esc = this.uploads.esc, md = window.HireMarkdown;
      let html = '';
      if (state.error) html = `<p class="notice warn">${esc(state.error)}</p>${state.id ? '<button class="button" type="button" data-refresh-source>Check draft again</button>' : ''}`;
      else if (state.applied) html = '<p class="notice">Draft placed in the form. Edit it as needed, then use the form’s save or assign button.</p>';
      else if (state.busy || (state.id && !d) || d?.state === 'drafting') html = '<p role="status">Preparing a cited proposal. You can leave this page and return to review it. Your writing is kept.</p>';
      else if (d?.state === 'failed') html = `<p class="notice warn">${esc(d.error)} Your writing is kept. Use the draft button to start again.</p>`;
      else if (d?.state === 'ready') {
        const p = d.proposal;
        html = `<section class="source-proposal"><h3 tabindex="-1">${esc(config.title)}</h3><p class="muted">Review the interpretation and its sources. Source links are checked; claims and assumptions still need your judgment.</p>`;
        if (['job', 'skill', 'memory'].includes(config.kind)) html += `<p><strong>${esc(p.name)}</strong>${p.description ? ` · ${esc(p.description)}` : ''}</p>`;
        if (config.kind === 'memory') {
          html += p.memories.length ? p.memories.map((m, i) => `<article class="memory-candidate"><label class="check"><input type="checkbox" data-memory="${i}" ${(state.selected?.includes(i) ?? (m.basis === 'stated')) ? 'checked' : ''}><strong>${esc(m.topic)}</strong></label><p class="memory-basis">${m.basis === 'stated' ? 'Stated in source · not independently verified' : 'Inferred · confirm before remembering'}</p><div class="prose">${md(m.content)}</div><div class="prose"><strong>${m.basis === 'inferred' ? 'Reasoning' : 'Why it matters'}:</strong>${md(m.reason)}<strong>Check again:</strong>${md(m.reviewAfter)}</div></article>`).join('') : '<p>No durable memories were supported by these materials. The original remains available as a reference.</p>';
        } else html += `<article class="prose">${md(p.content)}</article>`;
        for (const [title, values] of [['Assumptions to confirm', p.assumptions], ['Questions to resolve', p.questions]]) if (values?.length) html += `<h4>${title}</h4><ul>${values.map(value => `<li>${md(value)}</li>`).join('')}</ul>`;
        html += `<p class="muted">Using this draft replaces the writing fields above. You can edit the result before saving.</p><p class="source-apply-notice" role="status"></p><div class="actions">${config.kind !== 'memory' || p.memories.length ? `<button class="button primary" type="button" data-use-source>${config.kind === 'memory' ? 'Use selected memories' : 'Use draft in form'}</button>` : ''}<button class="button" type="button" data-discard-source>Discard draft</button></div></section>`;
      }
      if (target.innerHTML !== html) {
        target.innerHTML = html;
        target.querySelector('[data-refresh-source]')?.addEventListener('click', () => this.refresh(form));
        target.querySelector('[data-use-source]')?.addEventListener('click', () => this.use(form));
        target.querySelector('[data-discard-source]')?.addEventListener('click', () => { this.keep(form, {}); this.drawAll(form); });
        for (const checkbox of target.querySelectorAll('[data-memory]')) checkbox.addEventListener('change', () => {
          this.keep(form, { ...this.state(form), selected: [...target.querySelectorAll('[data-memory]:checked')].map(el => Number(el.dataset.memory)) });
        });
      }
      this.ready(form);
    }
    use(form) {
      const config = this.config(form), state = this.state(form), d = state.draft, p = d.proposal;
      const notice = form.querySelector('.source-apply-notice');
      try {
        const ids = this.uploads.selected(form);
        if (JSON.stringify(ids) !== JSON.stringify(d.uploadIDs)) throw new Error('The selected sources changed. Reattach the draft’s sources or prepare a new draft.');
        let content = p.content;
        if (config.kind === 'memory') {
          const selected = [...form.querySelectorAll('[data-memory]:checked')].map(el => p.memories[Number(el.dataset.memory)]);
          if (!selected.length) throw new Error('Select at least one memory, or discard this draft.');
          content = '# Working memory\n\nProposed from uploaded materials and selected by the manager. Source statements and inferences are distinguished below.\n\n' + selected.map(m => `## ${m.topic}\n\nBasis: ${m.basis === 'stated' ? 'Stated in source; not independently verified.' : 'Inferred; confirm before relying on it.'}\n\n${m.content}\n\n${m.basis === 'inferred' ? 'Reasoning' : 'Why it matters'}: ${m.reason}\n\nCheck again: ${m.reviewAfter}`).join('\n\n');
        }
        for (const [title, values] of [['Assumptions to confirm', p.assumptions], ['Questions to resolve', p.questions]]) if (values?.length) content += `\n\n## ${title}\n\n${values.map(value => `- ${value}`).join('\n')}`;
        const field = form.elements[config.field];
        if (!field || field.disabled) throw new Error('This form is not currently accepting changes.');
        if (field.maxLength > 0 && content.length > field.maxLength) throw new Error('This draft is too long for the form. Prepare a shorter draft with more focused sources.');
        // Clicking Use is the explicit choice to replace the writing, not an
        // automatic callback from a late model response.
        for (const [key, value] of Object.entries({ [config.field]: content, ...(config.kind === 'skill' || config.kind === 'memory' || config.kind === 'job' ? { name: p.name } : {}), ...(config.kind === 'skill' ? { description: p.description } : {}) })) {
          const el = form.elements[key]; if (!el) continue;
          el.value = value; el.dispatchEvent(new Event('input', { bubbles: true }));
        }
        this.keep(form, { ...state, applied: true }); this.drawAll(form);
        field.focus(); field.scrollIntoView({ block: 'center', behavior: 'auto' });
      } catch (error) { notice.textContent = error.message; }
    }
    async previous(form) {
      const config = this.config(form), target = form.querySelector('[data-source-review]'), esc = this.uploads.esc;
      try {
        const worker = form.elements.workerSlug?.value ?? this.uploads.scope();
        const { drafts } = await this.uploads.api(`/api/source-drafts?worker=${encodeURIComponent(worker)}&kind=${config.kind}`);
        if (!target.isConnected) return;
        target.innerHTML = `<h3>Previous drafts</h3>${drafts.length ? `<ul class="source-list">${drafts.slice().reverse().map(d => `<li><span>${esc(d.proposal.name || config.title)}<small>${esc(new Date(d.createdAt).toLocaleString())} · ${esc(d.state)}</small></span><button type="button" class="button" data-open-source="${esc(d.id)}">Review draft</button></li>`).join('')}</ul>` : '<p>No drafts yet. Choose your sources, then prepare a draft.</p>'}`;
        for (const button of target.querySelectorAll('[data-open-source]')) button.addEventListener('click', () => {
          const draft = drafts.find(d => d.id === button.dataset.openSource);
          this.keep(form, { id: draft.id, worker, draft });
          this.uploads.set(form, draft.uploadIDs); this.uploads.refresh(form); this.draw(form);
          if (draft.state === 'drafting') this.refresh(form);
        });
      } catch (error) { if (target.isConnected) target.textContent = error.message; }
    }
  };
})();
