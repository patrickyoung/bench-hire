/* A skill is a folder: review its files, test a separate proposed version,
   then install exactly that version or restore the preceding one. */
(() => {
  'use strict';
  const enc = encodeURIComponent;
  const arr = value => Array.isArray(value) ? value : [];
  const labels = { drafting: 'Preparing changes', review: 'Ready for review', testing: 'Running checks', applied: 'Improvement applied', reverted: 'Previous version restored', failed: 'Draft needs attention' };
  window.HireSkills = class {
    constructor(options) { Object.assign(this, options); this.pending = new Map(); this.fileChoices = new Map(); this.generation = 0; }
    url(slug, skill, id = '') { return `/workers/${enc(slug)}/skills/${enc(skill)}${id ? '/improvements/'+enc(id) : ''}`; }
    apiURL(slug, skill, id = '') { return '/api'+this.url(slug,skill,id); }
    error(target, error) { if (target?.isConnected && error.name !== 'AbortError') target.textContent = error.message; }
    async render(slug, skill, id) {
      const generation = ++this.generation;
      this.editorOpen = false;
      const base = this.apiURL(slug,skill,id);
      const data = await this.api(base);
      if (generation !== this.generation) return;
      if (id) {
        this.drawRevision(data,base);
        const source = new URLSearchParams(location.search).get('source');
        if (source !== null) await this.openSource(base,source);
        if (['drafting','testing'].includes(data.improvement.state)) this.watch(base,generation);
      } else {
        const worker = await this.api('/api/workers/'+enc(slug));
        if (generation !== this.generation) return;
        this.drawSkill(data,worker,base);
      }
    }
    drawSkill(data,worker,base) {
      const { esc, md } = this, mutable = !worker.retiredAt && !worker.retiringAt;
      const files = arr(data.bundle.files), results = arr(worker.requests).filter(r => r.resultExists).slice().reverse().slice(0,40);
      this.mount('Improve '+data.skill, `<section class="page skill-page"><a class="back" data-link href="/workers/${enc(worker.slug)}?tab=capabilities">← Training</a><header class="page-head"><div><p class="eyebrow">Skill development</p><h1>${esc(data.skill)}</h1><p class="lede">Improve the instructions, scripts and resources together. Review and test a proposed version before using it.</p></div></header>
        ${mutable ? `<section class="block"><h2>What should get better?</h2><form data-form="skill-improve"><input type="hidden" name="workerSlug" value="${esc(worker.slug)}"><div class="field"><label for="skill-goal">Feedback and desired improvement</label><textarea id="skill-goal" name="goal" rows="4" maxlength="8192" required placeholder="The report script misses refunds. Update the calculation, keep the existing format and add a test for refunds."></textarea></div>
        ${results.length ? `<fieldset class="skill-work-evidence"><legend>Relevant work results <small>Choose up to eight</small></legend>${results.map(r => `<label class="check"><input type="checkbox" name="result-${esc(r.id)}" data-work-result value="${esc(r.id)}"><span><strong>${esc(r.title)}</strong><small>${esc(r.state)} · ${esc(r.summary || 'Open the result to review its evidence.')}</small></span><a href="/workers/${enc(worker.slug)}/requests/${enc(r.id)}" target="_blank" rel="noopener">Read result</a></label>`).join('')}</fieldset>` : '<p class="muted">Completed work results will be available here as evidence. You can also supply examples and corrections as files.</p>'}
        <p class="muted">The current skill is kept while a separate proposal is prepared. Your sources are retained with the review.</p><p class="form-error" role="status" data-skill-error></p><button class="button primary" type="submit" ${this.pending.has(base) ? 'disabled' : ''}>${this.pending.has(base) ? 'Preparing…' : 'Prepare improvement'}</button></form></section>` : ''}
        <section class="block"><h2>Installed files</h2><p>${files.filter(f=>!f.dir).length} files. Scripts, resources and binary assets are included in the version snapshot.</p><ul class="skill-file-list">${files.map(f => `<li><span>${f.dir ? 'Folder' : f.text ? 'Text' : 'Asset'}</span><button type="button" class="link" data-current-file="${esc(f.path)}" ${f.dir ? 'disabled' : ''}>${esc(f.path)}</button><small>${f.dir ? '' : (f.mode & 73 ? 'Executable · ' : '')+f.size+' bytes'}</small></li>`).join('')}</ul><div data-current-preview></div></section>
        <section class="block"><h2>Improvements & previous versions</h2>${arr(data.improvements).length ? `<div class="list">${data.improvements.map(d=>`<a class="skill-history" data-link href="${this.url(worker.slug,data.skill,d.id)}"><strong>${esc(d.goal)}</strong><span>${esc(labels[d.state] || d.state)} · ${esc(new Date(d.createdAt).toLocaleString())}</span></a>`).join('')}</div>` : '<p>No improvements proposed yet.</p>'}</section></section>`,'team');
      const root = document.querySelector('.skill-page'), form = root.querySelector('form');
      form?.addEventListener('submit',async event => {
        event.preventDefault(); event.stopPropagation();
        if (this.pending.has(base)) return;
        const snapshot = this.reading.formSubmission(form), button = form.querySelector('[type=submit]');
        try {
          const fields = new FormData(form), requestIDs = [...form.querySelectorAll('[data-work-result]:checked')].map(input=>input.value);
          if (requestIDs.length > 8) throw new Error('Choose up to eight work results.');
          const uploadIDs = this.uploads.selected(form);
          this.pending.set(base,true); button.disabled = true; button.textContent = 'Preparing…';
          const result = await this.api(base+'/improvements',{method:'POST',body:{goal:fields.get('goal'),baseSha256:data.bundle.sha256,uploadIDs,requestIDs}});
          this.reading.forgetForm(form,snapshot); this.uploads.clear(form,uploadIDs);
          if (form.isConnected) this.navigate(this.url(worker.slug,data.skill,result.improvement.id));
        } catch(error) { this.error(form.querySelector('[data-skill-error]'),error); }
        finally { this.pending.delete(base); if (button.isConnected) { button.disabled = false; button.textContent = 'Prepare improvement'; } }
      });
      for (const button of root.querySelectorAll('[data-current-file]')) button.addEventListener('click',async()=>{
        const target = root.querySelector('[data-current-preview]');
        try {
          const file = await this.api('/api/workers/'+enc(worker.slug)+'/files?path='+enc('skills/'+data.skill+'/'+button.dataset.currentFile));
          if (!target.isConnected) return;
          target.innerHTML = `<h3>${esc(file.path)}</h3>${file.binary ? '<p>Binary asset. It will be preserved byte for byte unless a change is proposed.</p>' : `<pre class="skill-code" tabindex="0">${esc(file.content || '')}</pre>`}`;
        } catch(error) { this.error(target,error); }
      });
    }
    drawRevision(data,base) {
      this.data = data; this.base = base;
      const {esc,md} = this, d = data.improvement, mutable = !data.worker.retiredAt && !data.worker.retiringAt;
      const busy = ['drafting','testing'].includes(d.state), ready = d.state === 'review';
      const tested = Boolean(d.testedSha256 && d.testedSha256 === d.proposalSha256);
      const current = data.currentSha256, original = current === d.before.sha256, installed = current === d.after.sha256;
      const canApply = mutable && !busy && ['review','reverted'].includes(d.state) && tested && original;
      this.mount('Improve '+d.skill,`<section class="page skill-page"><a class="back" data-link href="${this.url(d.workerSlug,d.skill)}">← ${esc(d.skill)} · improvements</a><header class="page-head"><div><p class="eyebrow">Review a skill improvement</p><h1>${esc(d.skill)}</h1><p class="lede">${esc(d.goal)}</p><p class="skill-phase" role="status">${esc(labels[d.state] || d.state)}</p></div></header>
        ${d.error ? `<p class="notice warn">${esc(d.error)}</p>` : ''}
        ${busy ? `<p class="notice">${d.state === 'drafting' ? 'Preparing changes from the complete skill snapshot and your evidence.' : 'Checking the original and proposed versions in separate copies. The installed skill is kept.'} You can leave this page and return.</p>` : ''}
        ${!original && !installed ? '<p class="notice warn">The installed skill has changed since this proposal. Review it and prepare a new improvement before applying changes.</p>' : ''}
        ${!busy && d.after.sha256 ? '<nav class="skill-review-steps" aria-label="Skill review steps"><a class="button" href="#skill-changes">1. Review files</a><a class="button" href="#skill-checks">2. Check behavior</a><a class="button" href="#skill-decision">3. Apply or restore</a></nav>' : ''}<div data-skill-source></div>
        ${d.plan.summary ? `<section class="block"><h2>Proposed improvement</h2><article class="prose">${md(d.plan.summary)}</article>${arr(d.plan.assumptions).length ? `<h3>Assumptions to confirm</h3><ul>${d.plan.assumptions.map(text=>`<li class="prose">${md(text)}</li>`).join('')}</ul>` : ''}</section>` : ''}
        ${arr(data.changes).length && d.after.sha256 ? `<section id="skill-changes" class="block"><h2>Changed files</h2><p>Compare each change with the original. Files outside this list are kept unchanged.</p><ul class="skill-file-list">${data.changes.map(c=>`<li><span class="skill-change ${esc(c.kind)}">${esc(c.kind)}</span><button type="button" class="link" data-compare-file="${esc(c.path)}" ${c.before?.dir || c.after?.dir ? 'disabled' : ''}>${esc(c.path)}</button><small>${c.after?.dir || c.before?.dir ? 'Folder' : c.after?.mode & 73 ? 'Executable' : c.after && !c.after.text ? 'Binary asset' : ''}</small></li>`).join('')}</ul><div data-skill-comparison></div></section>` : ''}
        ${arr(d.plan.checks).length ? `<section id="skill-checks" class="block"><h2>Checks for this improvement</h2><p>These checks are proposed for your review. Run them against both versions to assess the change. Passing proves the listed checks, not every possible use of the skill.</p><ul class="skill-check-plan">${d.plan.checks.map(check=>`<li><strong>${esc(check.name)}</strong><p>${esc(check.purpose)}</p><code>${esc(JSON.stringify(check.argv))}</code></li>`).join('')}</ul>${arr(d.plan.checkFiles).length ? `<p>Inspect the test code:</p><div class="actions">${d.plan.checkFiles.map(file=>`<button type="button" class="button" data-check-file="${esc(file.path)}">${esc(file.path)}</button>`).join('')}</div>` : ''}<div data-skill-check-preview></div>
        <div class="actions"><button type="button" class="button primary" data-skill-test ${mutable && ready ? '' : 'disabled'}>${d.state === 'testing' ? 'Checks running…' : tested ? 'Run checks again' : 'Run checks'}</button></div><p class="muted">Checks run with network access off and writes limited to a disposable skill copy and its temporary directory.</p><div data-skill-results>${this.results(d)}</div></section>` : ''}
        <section id="skill-decision" class="block skill-decision"><h2>Your next step</h2><p data-skill-action-error class="form-error" role="status"></p>
        ${d.state === 'applied' ? `<p>This exact version is installed. The preceding version is retained with this review.</p><button class="button" type="button" data-skill-revert ${mutable && installed ? '' : 'disabled'}>Restore previous version</button>` : `<p>${canApply ? 'Review the file changes and check results, then apply this version.' : d.state === 'reverted' ? 'The original version is back in use. This proposed version and its checks remain available.' : 'Review the proposed files, make any corrections, and run the checks before applying.'}</p><button class="button primary" type="button" data-skill-apply ${canApply ? '' : 'disabled'}>Apply reviewed improvement</button>`}
        <a class="button" data-link href="${this.url(d.workerSlug,d.skill)}">Prepare another improvement</a></section></section>`,'team');
      const root = document.querySelector('.skill-page'); this.root = root;
      root.querySelector('[data-skill-test]')?.addEventListener('click',()=>this.act('test'));
      root.querySelector('[data-skill-apply]')?.addEventListener('click',()=>this.act('apply'));
      root.querySelector('[data-skill-revert]')?.addEventListener('click',()=>this.act('revert'));
      for (const button of root.querySelectorAll('[data-compare-file]')) button.addEventListener('click',()=>this.compare(button.dataset.compareFile));
      for (const button of root.querySelectorAll('[data-check-file]')) button.addEventListener('click',()=>this.showCheck(button.dataset.checkFile));
      for (const button of root.querySelectorAll('[data-check-output]')) button.addEventListener('click',()=>{
        const result = d.results[Number(button.dataset.checkOutput)];
        const target = root.querySelector('[data-check-log]');
        target.innerHTML = `<h3>${esc(result.name)} · ${result.version === 'before' ? 'Original' : 'Proposed'}</h3><pre class="skill-code" tabindex="0">${esc(result.output || result.error || 'No output. See the recorded exit status above.')}</pre>`;
      });
      const selected = this.fileChoices.get(base);
      if (selected) this.compare(selected);
    }
    results(d) {
      if (!arr(d.results).length) return '<p>No checks have run on this proposal yet.</p>';
      const {esc} = this;
      return `<h3>Recorded results</h3><ul class="skill-test-results">${d.results.map((r,i)=>`<li><span><strong>${esc(r.name)}</strong><small>${r.version === 'before' ? 'Original' : 'Proposed'} version · ${(r.durationMs/1000).toFixed(1)}s</small></span><span class="skill-test-state ${esc(r.state)}">${esc(r.state)} · exit ${r.exit}</span><button type="button" class="button" data-check-output="${i}">View output</button></li>`).join('')}</ul><div data-check-log></div>`;
    }
    async act(action) {
      const root = this.root, data = this.data, base = this.base, d = data.improvement;
      if (this.pending.has(base)) return;
      const buttons = [...root.querySelectorAll('[data-skill-test],[data-skill-apply],[data-skill-revert]')].map(button => [button,button.disabled]);
      try {
        if (this.editorOpen) throw new Error('Save or cancel your file edit before running checks or applying a version.');
        this.pending.set(base,true);
        for (const button of root.querySelectorAll('[data-skill-test],[data-skill-apply],[data-skill-revert]')) button.disabled = true;
        await this.api(base+'/'+action,{method:'POST',body:action === 'test' ? {sha256:d.proposalSha256} : {sha256:d.proposalSha256,currentSha256:data.currentSha256}});
        if (!root.isConnected) return;
        await this.render(d.workerSlug,d.skill,d.id);
      } catch(error) {
        this.error(root.querySelector('[data-skill-action-error]'),error);
        if (root.isConnected) root.querySelector('[data-skill-action-error]')?.scrollIntoView({block:'center'});
      } finally { this.pending.delete(base); for (const [button,disabled] of buttons) if (button.isConnected) button.disabled = disabled; }
    }
    async watch(base,generation) {
      setTimeout(async()=>{
        if (generation !== this.generation || !this.root?.isConnected) return;
        try {
          const data = await this.api(base);
          if (generation !== this.generation) return;
          this.drawRevision(data,base);
          if (['drafting','testing'].includes(data.improvement.state)) this.watch(base,generation);
        } catch(error) { if (error.name !== 'AbortError' && generation === this.generation) { this.error(this.root.querySelector('[data-skill-action-error]'),error); this.watch(base,generation); } }
      },2000);
    }
    async readFile(version,path) {
      return this.api(this.base+'/file?version='+version+'&path='+enc(path));
    }
    fileMarkup(file,version,path) {
      const {esc,md} = this;
      if (!file) return '<p class="muted">This file is absent in this version.</p>';
      const download = this.base+'/file?version='+version+'&path='+enc(path)+'&download=1';
      return `<p><a href="${download}">Download exact file</a> · ${file.size} bytes · Mode ${Number(file.mode).toString(8)}</p>${file.image ? `<img class="skill-asset-preview" src="${this.base}/file?version=${version}&path=${enc(path)}&preview=1" alt="${esc(version === 'before' ? 'Original' : 'Proposed')} ${esc(path)}">` : ''}${file.binary ? '<p>Binary asset. The download preserves its exact bytes.</p>' : `<pre class="skill-code" tabindex="0">${esc(file.content || '')}</pre>`}${file.truncated ? '<p>Preview truncated; the download contains the whole file.</p>' : ''}`;
    }
    async compare(path) {
      const base = this.base, target = this.root?.querySelector('[data-skill-comparison]'), {esc} = this;
      if (!target) return;
      if (this.editorOpen) { this.error(this.root.querySelector('[data-skill-action-error]'),new Error('Save or cancel your file edit before opening another file.')); return; }
      this.fileChoices.set(base,path);
      const results = await Promise.allSettled([this.readFile('before',path),this.readFile('candidate',path)]);
      if (!target.isConnected || base !== this.base || this.fileChoices.get(base) !== path) return;
      const failure = results.find(result => result.status === 'rejected' && result.reason.status !== 404);
      if (failure) { this.error(target,failure.reason); return; }
      const before = results[0].status === 'fulfilled' ? results[0].value : null, after = results[1].status === 'fulfilled' ? results[1].value : null;
      const reason = arr(this.data.improvement.plan.changes).find(change => change.path === path)?.reason;
      target.innerHTML = `<h3>${esc(path)}</h3>${reason ? `<article class="prose skill-change-reason">${this.md(reason)}</article>` : ''}<div class="skill-file-compare"><section><h4>Original</h4>${this.fileMarkup(before,'before',path)}</section><section><h4>Proposed</h4>${this.fileMarkup(after,'candidate',path)}</section></div>${after && !after.binary && !after.truncated && after.editable ? `<button type="button" class="button" data-edit-file>${this.hasFileDraft('candidate',path) ? 'Resume unsaved edit' : 'Edit proposed file'}</button>` : ''}<div data-file-editor></div>`;
      target.querySelector('[data-edit-file]')?.addEventListener('click',()=>this.edit(target.querySelector('[data-file-editor]'),'candidate',path,after.content));
    }
    async showCheck(path) {
      const base = this.base, target = this.root.querySelector('[data-skill-check-preview]');
      if (this.editorOpen) return;
      try {
        const file = await this.readFile('checks',path);
        if (!target.isConnected || base !== this.base) return;
        target.innerHTML = `<h3>${this.esc(path)}</h3>${this.fileMarkup(file,'checks',path)}${file.editable ? `<button type="button" class="button" data-edit-check>${this.hasFileDraft('checks',path) ? 'Resume unsaved edit' : 'Edit proposed test'}</button>` : ''}<div data-file-editor></div>`;
        target.querySelector('[data-edit-check]')?.addEventListener('click',()=>this.edit(target.querySelector('[data-file-editor]'),'checks',path,file.content));
      } catch(error) { this.error(target,error); }
    }
    draftKey(version,path) { return `skill:${this.data.improvement.id}:${version}:${path}`; }
    hasFileDraft(version,path) { return Boolean(this.reading.record().drafts['editor:'+this.draftKey(version,path)]); }
    edit(target,version,path,content) {
      this.editorOpen = true;
      const data = this.data, base = this.base, d = data.improvement, esc = this.esc;
      target.innerHTML = `<form><div class="field"><label for="skill-file-edit">Proposed content for ${esc(path)}</label><p data-draft-notice hidden>Unsaved edit restored. Saving checks that the proposal is still the version you edited.</p><textarea id="skill-file-edit" data-draft="${esc(this.draftKey(version,path))}" data-base-sha256="${esc(d.proposalSha256)}" rows="16" maxlength="524288" spellcheck="false" class="skill-code">${esc(content)}</textarea></div><p>Saving this edit invalidates previous test results. Run the checks again before applying.</p><p data-edit-error role="status"></p><div class="actions"><button class="button primary" type="submit">Save proposed file</button><button class="button" type="button" data-cancel-edit>Cancel edit</button></div></form>`;
      this.reading.restore(target);
      target.querySelector('[data-cancel-edit]').addEventListener('click',()=>{ this.reading.forgetEditor(target.querySelector('textarea')); this.editorOpen = false; target.innerHTML = ''; });
      target.querySelector('form').addEventListener('submit',async event=>{
        event.preventDefault(); event.stopPropagation();
        const button = target.querySelector('[type=submit]'), editor = target.querySelector('textarea'), snapshot = this.reading.editorSubmission(editor); button.disabled = true;
        try {
          const saved = await this.api(base+'/file',{method:'PUT',body:{sha256:snapshot.baseSha256,version,path,content:snapshot.value}});
          this.reading.savedEditor(snapshot,saved.improvement.proposalSha256);
          this.editorOpen = false;
          if (target.isConnected) await this.render(d.workerSlug,d.skill,d.id);
        } catch(error) { this.error(target.querySelector('[data-edit-error]'),error); if (button.isConnected) button.disabled = false; }
      });
      target.querySelector('textarea').focus();
    }
    async openSource(base,index) {
      const target = this.root.querySelector('[data-skill-source]');
      try {
        const {source} = await this.api(base+'/file?source='+enc(index));
        if (!target.isConnected) return;
        target.innerHTML = `<section class="block"><h2>Source: ${this.esc(source.title)}</h2><article class="prose">${this.md(source.content?.text || JSON.stringify(source.content))}</article></section>`;
        target.scrollIntoView({block:'start'});
      } catch(error) { this.error(target,error); }
    }
  };
})();
