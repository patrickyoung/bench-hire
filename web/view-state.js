/* Preserve the manager's reading and editing state while live data changes.
   No application state or execution verdict is stored here. */
(() => {
  'use strict';

  class HireViewState {
    constructor(root, scope) {
      this.root = root;
      this.scope = scope;
      this.currentScope = scope();
      this.records = {};
      try { this.records = JSON.parse(sessionStorage.getItem('hire.view.v1') || '{}'); } catch { /* storage is optional */ }
      root.addEventListener('toggle', (event) => {
        if (event.target.tagName === 'DETAILS' && root.contains(event.target)) this.rememberDetails(root);
      }, true);
      const rememberInput = (event) => {
        const editor = event.target;
        if (editor.dataset.draft && !editor.readOnly && !editor.disabled) {
          this.record().drafts[`editor:${editor.dataset.draft}`] = { value: editor.value, baseSha256: editor.dataset.baseSha256 || '' };
          this.persist();
          return;
        }
        const form = event.target.form;
        if (!form?.dataset.form) return;
        this.record().drafts[this.formKey(form)] = this.formValues(form);
        this.persist();
      };
      root.addEventListener('input', rememberInput);
      root.addEventListener('change', rememberInput);
    }

    record(scope = this.currentScope) {
      if (!this.records[scope]) this.records[scope] = { details: {}, drafts: {} };
      return this.records[scope];
    }

    persist() {
      const keys = Object.keys(this.records);
      for (const key of keys.slice(0, Math.max(0, keys.length - 60))) delete this.records[key];
      try { sessionStorage.setItem('hire.view.v1', JSON.stringify(this.records)); } catch { /* memory still works */ }
    }

    details(node) {
      const result = [...node.querySelectorAll('details')];
      if (node.matches?.('details')) result.unshift(node);
      return result;
    }

    detailKey(detail) {
      if (detail.dataset.disclosure) return detail.dataset.disclosure;
      if (detail.id) return detail.id;
      const parts = [];
      for (let el = detail; el && this.root.contains(el); el = el.parentElement?.closest('details')) {
        const summary = el.querySelector(':scope > summary');
        parts.unshift((summary?.textContent || '').trim().replace(/\d+/g, '#'));
      }
      return parts.join('/');
    }

    rememberDetails(node) {
      const saved = this.record().details;
      for (const detail of this.details(node)) saved[this.detailKey(detail)] = detail.open;
      this.persist();
    }

    formValues(form) {
      const fields = {};
      for (const el of form.elements) {
        if (!el.name || ['hidden', 'password', 'file', 'submit'].includes(el.type)) continue;
        fields[el.name] = ['checkbox', 'radio'].includes(el.type) ? el.checked : el.value;
      }
      return fields;
    }

    formKey(form) {
      return form.dataset.form + (form.dataset.id ? `:${form.dataset.id}` : '');
    }

    formSubmission(form) {
      return { scope: this.currentScope, values: this.formValues(form) };
    }

    forgetForm(form, submission) {
      const name = typeof form === 'string' ? form : form && this.formKey(form);
      const saved = this.record(submission?.scope);
      // A slow save may finish after navigation or after the manager starts
      // another draft. Clear only the submitted values in their original page.
      if (name && (!submission || !saved.drafts[name] || JSON.stringify(saved.drafts[name]) === JSON.stringify(submission.values))) delete saved.drafts[name];
      this.persist();
    }

    selection(name, scope = this.currentScope) {
      return this.record(scope).selections?.[name];
    }

    select(name, value, scope = this.currentScope) {
      const saved = this.record(scope);
      saved.selections ||= {};
      saved.selections[name] = value;
      this.persist();
    }

    forgetEditor(editor) {
      if (editor?.dataset.draft) delete this.record().drafts[`editor:${editor.dataset.draft}`];
      this.persist();
    }

    editorSubmission(editor) {
      return { scope: this.currentScope, key: `editor:${editor.dataset.draft}`, value: editor.value, baseSha256: editor.dataset.baseSha256 || '' };
    }

    savedEditor(submission, sha256) {
      const drafts = this.record(submission.scope).drafts;
      const current = drafts[submission.key];
      if (current?.value === submission.value && current.baseSha256 === submission.baseSha256) delete drafts[submission.key];
      // Text typed while saving is based on our own successful write now.
      // Preserve a separately reopened version if its base already changed.
      else if (current?.baseSha256 === submission.baseSha256) current.baseSha256 = sha256;
      this.persist();
      return Boolean(drafts[submission.key]);
    }

    hasDraft() {
      return Object.keys(this.record().drafts).length > 0;
    }

    restore(node) {
      const saved = this.record();
      for (const detail of this.details(node)) {
        const key = this.detailKey(detail);
        if (Object.hasOwn(saved.details, key)) detail.open = saved.details[key];
      }
      for (const form of node.querySelectorAll('form[data-form]')) {
        const fields = saved.drafts[this.formKey(form)];
        if (!fields) continue;
        for (const el of form.elements) {
          if (!Object.hasOwn(fields, el.name) || ['hidden', 'password', 'file', 'submit'].includes(el.type)) continue;
          if (['checkbox', 'radio'].includes(el.type)) el.checked = fields[el.name];
          else el.value = fields[el.name];
        }
      }
      for (const editor of node.querySelectorAll('[data-draft]')) {
        const draft = saved.drafts[`editor:${editor.dataset.draft}`];
        if (!draft || editor.readOnly || editor.disabled) continue;
        editor.value = draft.value;
        editor.dataset.baseSha256 = draft.baseSha256;
        editor.dataset.restoredDraft = 'true';
        const notice = editor.parentElement.querySelector('[data-draft-notice]');
        if (notice) notice.hidden = false;
      }
    }

    focusKey(el) {
      if (!el) return '';
      if (el.id) return `id:${el.id}`;
      if (el.tagName === 'SUMMARY') return `summary:${this.detailKey(el.parentElement)}`;
      return JSON.stringify([el.tagName, el.getAttribute('name'), el.getAttribute('href'),
        el.dataset.action, el.dataset.id, el.dataset.do, el.dataset.decision, el.dataset.tab,
        el.closest('form')?.dataset.form, el.closest('details')?.dataset.disclosure]);
    }

    // Background updates wait while text is selected. Replacing a log under
    // an active selection would break copying, even if scroll were restored.
    selected(node) {
      const selection = window.getSelection();
      return selection && !selection.isCollapsed &&
        (node.contains(selection.anchorNode) || node.contains(selection.focusNode));
    }

    replace(node, html, { outer = false, background = false, navigation = false } = {}) {
      if (!node || (background && this.selected(node))) return false;
      this.rememberDetails(node);
      const active = document.activeElement;
      const focused = node.contains(active) ? this.focusKey(active) : '';
      const cursor = focused && typeof active.selectionStart === 'number'
        ? [active.selectionStart, active.selectionEnd, active.selectionDirection] : null;
      const scroll = [window.scrollX, window.scrollY, this.root.parentElement?.scrollTop || 0];
      const positions = [...node.querySelectorAll('pre, textarea, [data-scroll]')].map(el => [el, el.scrollLeft, el.scrollTop]);
      const offsets = positions.map(([el, left, top]) => [this.focusKey(el), left, top]);
      if (outer) {
        const template = document.createElement('template');
        template.innerHTML = html.trim();
        const replacement = template.content.firstElementChild;
        node.replaceWith(replacement);
        node = replacement;
      } else {
        if (html instanceof Element) node.replaceChildren(...html.childNodes);
        else node.innerHTML = html;
      }
      if (node === this.root) this.currentScope = this.scope();
      this.restore(node);
      if (!navigation) {
        const candidates = [...node.querySelectorAll('input, textarea, select, button, a, summary, pre, [tabindex]')];
        const target = focused && candidates.find(el => this.focusKey(el) === focused);
        if (target) {
          target.focus({ preventScroll: true });
          if (cursor) target.setSelectionRange(...cursor);
        }
        const scrollables = [...node.querySelectorAll('pre, textarea, [data-scroll]')];
        offsets.forEach(([key, left, top], index) => {
          const el = scrollables.find(el => this.focusKey(el) === key) || scrollables[index];
          if (el) { el.scrollLeft = left; el.scrollTop = top; }
        });
        window.scrollTo(scroll[0], scroll[1]);
        if (this.root.parentElement) this.root.parentElement.scrollTop = scroll[2];
      }
      return true;
    }
  }

  window.HireViewState = HireViewState;
})();
