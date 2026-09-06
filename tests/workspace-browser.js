/* Manager navigation, mobile controls and direct correction, using offline work. */
(async () => {
  const checks = [];
  const find = selector => document.querySelector(selector);
  const assert = (ok, message) => { if (!ok) throw new Error(message); checks.push(message); };
  const wait = async (predicate, label) => {
    for (let i = 0; i < 800; i++) {
      if (predicate()) return;
      await new Promise(resolve => setTimeout(resolve, 20));
    }
    throw new Error(`Timed out: ${label}`);
  };
  const visit = async (path, predicate) => {
    history.pushState({}, '', path);
    dispatchEvent(new PopStateEvent('popstate'));
    await wait(predicate, path);
  };
  const fit = label => assert(document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1, `${label} fits the viewport`);
  const visible = element => element && !element.closest('details:not([open])') && element.getClientRects().length > 0;
  try {
    await wait(() => find('form[data-form="quick-hire"]'), 'onboarding');
    assert(visible(find('#nav-attention')), 'Inbox remains available when empty');
    assert(find('.journey').children.length === 3, 'First hire has an explicit path');
    fit('Onboarding');
    await visit('/hire', () => find('[data-disclosure="hire-examples"]'));
    assert(!find('#hire-timer'), 'Hiring has no unexplained elapsed timer');
    assert([...document.querySelectorAll('.example-card')].length === 3, 'Example jobs are visible cards');
    fit('Hiring');
    const bootstrap = await fetch('/api/bootstrap').then(r => r.json());
    const api = async (path, body, method = 'POST') => {
      const response = await fetch(path, { method, headers: { 'Content-Type': 'application/json', 'X-Hire-Token': bootstrap.token }, body: JSON.stringify(body) });
      const data = await response.json();
      if (!response.ok) throw new Error(JSON.stringify(data));
      return data;
    };
    await api('/api/workers', { name: 'Sales Reporter', purpose: 'Turn source records into clear sales reports, explain changes, and flag missing information.' });
    await api('/api/workers', { name: 'Release Writer', purpose: 'Prepare useful release notes from completed changes and keep the team informed.' });
    const original = await api('/api/workers/sales-reporter/requests', { text: 'Summarise net sales for September 1–3. Explain refunds and identify the leading region.' });
    assert((await fetch('/__test/work', { method: 'POST' })).ok, 'The assigned work runs through the real controller');
    const resultPath = `work/requests/${original.request.id}/RESULT.md`;
    const report = '# Net sales: September 1–3\n\nNet sales were **$320** after refunds. South led with **$170**, or 53% of total sales.\n\n## Regional performance\n\n| Region | Net sales |\n| --- | ---: |\n| North | $100 |\n| South | $170 |\n| East | $50 |\n| **Total** | **$320** |\n\n## What this means\n\nSouth contributed the largest share of revenue in the supplied three-day period. Refunds are included as negative amounts.\n\n## Before you use this report\n\nThe records contain no costs or prior-period sales. They do not establish profit or growth. Supporting totals are in totals.tsv.';
    await api('/api/workers/sales-reporter/files', { path: resultPath, content: report }, 'PUT');
    await api('/api/workers/sales-reporter/files', { path: resultPath.replace('RESULT.md', 'totals.tsv'), content: 'Region\tNet sales\nSouth\t170\nNorth\t100\nEast\t50\n' }, 'PUT');
    await visit('/', () => find('.overview'));
    assert(find('.metric strong').textContent === '1', 'Team overview identifies the decision waiting on the manager');
    assert(document.querySelectorAll('.team-grid .worker-row').length === 2, 'The team is available as selectable worker cards');
    fit('Team');
    const taskURL = `/workers/sales-reporter/requests/${original.request.id}`;
    await visit(taskURL, () => find('.review-box'));
    assert(visible(find('#result-feedback')), 'Feedback is immediately available beside the delivery');
    assert(!find('.diagnostics').open && !visible(find('.log')), 'A result for review never opens raw worker logs automatically');
    assert(find('.activity-summary').textContent.includes('checks passed'), 'Activity explains the recorded outcome in plain language');
    assert(find('.document-meta strong').textContent === 'Sales Reporter', 'The delivery identifies its author');
    assert(find('.document-body h3').textContent === 'Net sales: September 1–3', 'The actual report has a readable document title');
    assert(find('.file-card').textContent.includes('totals.tsv'), 'Supporting deliverables are directly accessible');
    fit('Review');
    find('.file-card').click();
    await wait(() => find('.file-preview table'), 'supporting file preview');
    assert(visible(find('.file-preview table')) && !visible(find('#file-editor')), 'Supporting data opens as a readable table');
    assert(find('.file-preview tbody tr').cells[1].textContent === '170', 'The preview preserves the delivered value');
    fit('Supporting file');
    await visit(taskURL, () => find('#result-feedback'));
    const feedback = find('#result-feedback');
    feedback.value = 'Include the source date and explain whether the records include every region.';
    feedback.dispatchEvent(new Event('input', { bubbles: true }));
    const review = feedback.form;
    review.requestSubmit(review.querySelector('[value="revise"]'));
    await wait(() => location.pathname !== taskURL && document.body.textContent.includes('Original result and your feedback'), 'direct revision');
    const revision = await fetch(`/api${location.pathname}`).then(r => r.json());
    assert(revision.request.revisionOf === original.request.id, 'Request revision saves feedback and creates a linked task in one action');
    assert(revision.request.text.includes('Include the source date'), 'The correction task carries the exact feedback');
    await visit('/workers/sales-reporter', () => find('.composer'));
    assert(document.querySelectorAll('.tabs [role="tab"]').length === 6, 'All six worker destinations are visible');
    for (const button of document.querySelectorAll('.tabs button')) {
      const bounds = button.getBoundingClientRect();
      assert(bounds.height >= 44 && bounds.left >= 0 && bounds.right <= innerWidth, `${button.textContent} has a reachable touch target`);
    }
    find('#intake-text').value = 'Keep this task draft while I check the schedule.';
    find('#intake-text').dispatchEvent(new Event('input', { bubbles: true }));
    for (const [name, selector] of [['schedule', '[data-action="add-routine"]'], ['capabilities', '#skill-name'], ['refine', '.builder'], ['files', '.file-layout'], ['details', '[data-action="toggle-network"]']]) {
      find(`.tabs [data-tab="${name}"]`).click();
      await wait(() => find(selector), name);
      assert(!visible(find('.composer')), `${name} focuses on its own controls`);
      fit(name);
    }
    assert(visible(find('[data-action="retire"]')), 'Employment controls have a labelled home in Tools & access');
    find('.tabs [data-tab="work"]').click();
    await wait(() => visible(find('.composer')), 'tasks');
    assert(find('#intake-text').value === 'Keep this task draft while I check the schedule.', 'Task drafts survive navigation through all worker sections');
    fit('Tasks');
    window.scrollTo(0, 0);
    await fetch('/__test/result', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ checks }) });
  } catch (error) {
    await fetch('/__test/result', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ error: error.stack, checks }) });
  }
})();
