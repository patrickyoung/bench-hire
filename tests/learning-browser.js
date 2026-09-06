/* Rendered UI against public-command fixtures; real provenance is tested by
   TestRealSuiteRecoveryAndLearning. Delays hold actual HTTP responses. */
(async () => {
  const resumed = JSON.parse(sessionStorage.getItem('hire.test.learning') || 'null');
  const checks = resumed?.checks || [];
  const find = selector => document.querySelector(selector);
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  const assert = (ok, message) => { if (!ok) throw new Error(message); checks.push(message); };
  const wait = async (predicate, label) => {
    for (let i = 0; i < 800; i++) { if (predicate()) return; await sleep(20); }
    throw new Error(`Timed out: ${label}`);
  };
  const fill = (selector, value) => {
    const el = find(selector);
    if (!el) throw new Error(`Missing ${selector}`);
    el.value = value;
    el.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const submit = (kind, operation) => {
    const form = find(`form[data-form="${kind}"]`);
    form.requestSubmit(form.querySelector(operation ? `button[value="${operation}"]` : 'button[type="submit"]'));
  };
  const visit = path => {
    history.pushState({}, '', path);
    window.dispatchEvent(new PopStateEvent('popstate'));
  };
  const tab = async name => {
    find(`[data-tab="${name}"]`).click();
    await wait(() => find('#tab-panel')?.getAttribute('aria-labelledby') === `tab-${name}` && (name === 'work' ? find('.composer:not([hidden])') : find('#learn-session')), `${name} tab`);
  };
  const holdResponse = matches => {
    const original = window.fetch;
    let release;
    const gate = new Promise(resolve => { release = resolve; });
    const status = { received: false, released: false, release };
    window.fetch = async (...args) => {
      const response = await original(...args);
      if (matches(...args)) {
        status.received = true;
        window.fetch = original;
        await gate;
        status.released = true;
      }
      return response;
    };
    return status;
  };
  window.confirm = () => true;
  try {
    const bootstrap = await fetch('/api/bootstrap').then(r => r.json());
    const api = async (path, body, method = 'POST') => {
      const response = await fetch(path, { method, headers: { 'Content-Type': 'application/json', 'X-Hire-Token': bootstrap.token }, body: JSON.stringify(body) });
      const result = await response.json();
      if (!response.ok) throw new Error(JSON.stringify(result));
      return result;
    };
    if (!resumed) {
      await api('/api/workers', { name: 'Learner', purpose: 'Check reports against source records.' });
      await api('/api/workers', { name: 'Neighbor', purpose: 'Write useful notes.' });
      visit('/workers/learner?tab=capabilities');
      await wait(() => find('#learn-session'), 'recorded runs');
      assert(find('#learn-session').textContent.includes('Report corrected against source records'), 'Learning presents the recorded task, not a raw session ID');
      assert(find('#learn-session').getBoundingClientRect().height > 0, 'Learning controls are visible');
      fill('#learn-session', 'first-pass.jsonl');
      fill('#learn-skill', 'learned-method');
      submit('learn-from-run', 'inspect');
      await wait(() => find('#learning-result').textContent.includes('No verified recovery'), 'no-recovery explanation');
      assert(!find('[data-action="admit-learning"]'), 'Passing once cannot be admitted as a learned lesson');
      submit('learn-from-run', 'prepare');
      await wait(() => find('#learning-result').textContent.includes('No useful lesson was found'), 'no-lesson explanation');
      assert(!find('[data-action="admit-learning"]'), 'A run with nothing worth keeping offers no skill to install');
      fill('#learn-session', 'recovery.jsonl');
      submit('learn-from-run', 'inspect');
      await wait(() => find('#learning-result pre')?.textContent.includes('STUMBLE'), 'recovery evidence');
      await tab('work');
      await tab('capabilities');
      await wait(() => find('#learning-result pre'), 'recovery evidence restored');
      assert(find('#learning-result').textContent.includes('omitted a refund'), 'Evidence remains accessible after changing tabs');

      const delayed = holdResponse((path, options) => path.endsWith('/learning') && JSON.parse(options.body).operation === 'prepare');
      submit('learn-from-run', 'prepare');
      await wait(() => delayed.received, 'prepared proposal response held');
      assert(find('form[data-form="learn-from-run"]').getAttribute('aria-busy') === 'true', 'Lesson preparation has a visible busy state');
      await tab('work');
      await tab('capabilities');
      assert(find('button[value="prepare"]').disabled && find('button[value="inspect"]').disabled, 'Returning during preparation cannot start another operation');
      visit('/workers/neighbor?tab=capabilities');
      await wait(() => find('h1')?.textContent === 'Neighbor' && find('#memory-content'), 'another worker opened');
      fill('#memory-name', 'neighbor-note');
      fill('#memory-content', 'Keep this worker’s unsent note.');
      delayed.release();
      await wait(() => JSON.parse(sessionStorage.getItem('hire.view.v1'))['/workers/learner'].selections?.learning?.proposal, 'proposal selection recorded on its original worker');
      assert(find('h1').textContent === 'Neighbor' && !find('#learning-result pre'), 'A delayed lesson response does not redraw another worker');
      visit('/workers/learner?tab=capabilities');
      await wait(() => find('[data-action="admit-learning"]'), 'prepared lesson reopened');
      assert(find('#learning-result').textContent.includes('Include refunds'), 'Preparing opens the exact proposed method for review');
      const before = await fetch('/api/workers/learner/capabilities').then(r => r.json());
      assert(!before.skills?.length, 'Preparing and reviewing leave the skill catalogue unchanged');

      for (const editorKind of ['file', 'definition']) {
        const isFile = editorKind === 'file';
        if (isFile) await api('/api/workers/learner/files', { path: 'work/draft.md', content: 'Original text.' }, 'PUT');
        find(`[data-tab="${isFile ? 'files' : 'refine'}"]`).click();
        if (isFile) {
          await wait(() => find('[data-action="open"][data-path="work/draft.md"]'), 'editable file');
          find('[data-action="open"][data-path="work/draft.md"]').click();
        }
        const selector = `#${editorKind}-editor`;
        await wait(() => find(selector), `${editorKind} editor`);
        if (!isFile) find('.direct-editor').open = true;
        const original = find(selector).value;
        const first = `${original}\nFirst saved edit.`;
        const second = `${first}\nNewer draft typed during the save.`;
        fill(selector, first);
        const savingEdit = holdResponse((path, options) => path.endsWith(isFile ? '/files' : '/definition') && options?.method === 'PUT');
        find(`[data-action="save-${editorKind}"]`).click();
        await wait(() => savingEdit.received, 'editor save response held');
        fill(selector, second);
        savingEdit.release();
        await wait(() => find('#toast').textContent.includes('Your newer edits are still a draft'), 'newer editor draft kept');
        assert(find(selector).value === second, `${editorKind} edits typed during a save stay visible`);
        const nextSave = holdResponse((path, options) => path.endsWith(isFile ? '/files' : '/definition') && options?.method === 'PUT');
        find(`[data-action="save-${editorKind}"]`).click();
        await wait(() => nextSave.received, 'next editor save finished');
        nextSave.release();
        await wait(() => !JSON.parse(sessionStorage.getItem('hire.view.v1'))['/workers/learner'].drafts[`editor:${isFile ? 'file:work/draft.md' : 'definition:GOAL.md'}`], 'editor draft cleared after successful save');
        const saved = await fetch(`/api/workers/learner/files?path=${isFile ? 'work/draft.md' : 'GOAL.md'}`).then(r => r.json());
        assert(saved.content === second, `${editorKind} newer draft can be saved against the completed write`);
      }
      await tab('capabilities');
      await wait(() => find('[data-action="admit-learning"]'), 'lesson review remains accessible after editing');

      // A slow ordinary save must clear only its submitted draft. Later edits
      // and another worker's form with the same name must remain untouched.
      fill('#memory-name', 'learner-note');
      fill('#memory-content', 'The submitted note.');
      const saving = holdResponse((path, options) => path.endsWith('/files') && options?.method === 'PUT');
      submit('remember-fact');
      await wait(() => saving.received, 'memory save response held');
      fill('#memory-content', 'A newer unfinished note.');
      visit('/workers/neighbor?tab=capabilities');
      await wait(() => find('h1')?.textContent === 'Neighbor' && find('#memory-content'), 'neighbor revisited');
      saving.release();
      await wait(() => saving.released, 'save response released');
      await sleep(100);
      assert(find('h1').textContent === 'Neighbor' && find('#memory-content').value === 'Keep this worker’s unsent note.', 'A delayed save keeps the current page and another worker’s draft');
      visit('/workers/learner?tab=capabilities');
      await wait(() => find('[data-action="admit-learning"]') && find('#memory-content'), 'learner restored');
      assert(find('#memory-content').value === 'A newer unfinished note.', 'Edits made during a save survive its delayed response');
      const proposal = find('[data-action="admit-learning"]').dataset.proposal;
      sessionStorage.setItem('hire.test.learning', JSON.stringify({ checks, proposal }));
      location.reload();
      return;
    }

    await wait(() => find('[data-action="admit-learning"]'), 'lesson restored after full reload');
    assert(find('[data-action="admit-learning"]').dataset.proposal === resumed.proposal, 'Full reload reopens the same saved proposal');
    assert(find('#learning-result pre').textContent.includes('Include refunds'), 'Reload fetches the proposal contents for review');
    assert(find('#learn-session').getBoundingClientRect().height > 0, 'Learning controls remain visible after reload');
    find('[data-action="admit-learning"]').click();
    await wait(() => [...document.querySelectorAll('[data-action="capability-file"]')].some(el => el.textContent === 'learned-method'), 'reviewed skill installed');
    assert(!find('[data-action="admit-learning"]'), 'Admission clears the pending review');
    const skill = await fetch('/api/workers/learner/files?path=skills/learned-method/SKILL.md').then(r => r.json());
    assert(skill.content.includes('Include refunds'), 'The installed method is the exact lesson reviewed');
    await tab('work');
    fill('#intake-text', 'Check the next report against signed source records. Use the learned method and state what you checked.');
    submit('intake');
    await tab('work');
    await wait(() => find('a.task-row'), 'follow-up task saved');
    assert(document.body.textContent.includes('Check the next report'), 'A follow-up task can exercise the learned method');
    // Passing this fixture establishes the interaction, not better model work.
    await fetch('/__test/result', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ checks }) });
  } catch (error) {
    await fetch('/__test/result', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ error: error.stack, checks }) });
  }
})();
