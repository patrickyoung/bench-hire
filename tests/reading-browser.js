/* Browser-level checks of visible evidence, safe formatting and live review.
   The task and automatic verdict use the same offline fixtures as other flows. */
(async () => {
  const checks = [];
  const find = selector => document.querySelector(selector);
  const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
  const assert = (ok, message) => { if (!ok) throw new Error(message); checks.push(message); };
  const wait = async (predicate, label) => {
    for (let i = 0; i < 800; i++) { if (predicate()) return; await sleep(20); }
    throw new Error(`Timed out: ${label}`);
  };
  try {
    const bootstrap = await fetch('/api/bootstrap').then(r => r.json());
    const api = async (path, body, method = 'POST') => {
      const response = await fetch(path, { method, headers: { 'Content-Type': 'application/json', 'X-Hire-Token': bootstrap.token }, body: JSON.stringify(body) });
      const result = await response.json();
      if (!response.ok) throw new Error(JSON.stringify(result));
      return result;
    };
    await api('/api/workers', { name: 'Sales Reporter', purpose: 'Summarise net sales from the supplied records.' });
    const task = await api('/api/workers/sales-reporter/requests', { text: 'Summarise the supplied net sales and explain the refund.' });
    assert((await fetch('/__test/work', { method: 'POST' })).ok, 'Fixture task ran through Hire');
    const path = `work/requests/${task.request.id}/RESULT.md`;
    const report = `# Net sales report

Net sales are 32000 cents after the refund.

| Region | Net cents |
| --- | ---: |
| North | 10000 |
| South | 17000 |
| East | 5000 |
| **Total** | **32000** |

## What was checked

1. Read signed source amounts.
2. Subtract the refund before summing.
   Keep the source date with the figure.

\`\`\`text
region    net_cents
North     10000
South     17000
\`\`\`

[Source reference](https://example.com/source?period=2026-09&view=net)

The following is untrusted source text:
<img src=x onerror="window.untrustedContentRan=true">
[Unsafe link](javascript:alert%281%29)
`;
    await api('/api/workers/sales-reporter/files', { path, content: report }, 'PUT');
    const taskURL = `/workers/sales-reporter/requests/${task.request.id}`;
    history.pushState({}, '', taskURL);
    dispatchEvent(new PopStateEvent('popstate'));
    await wait(() => find('.result-box table') && find('form[data-form="result-review"]'), 'formatted result');
    assert(find('.result-box tbody').rows.length === 4, 'All report rows are readable as a table');
    assert(find('.result-box tbody').rows[0].cells[1].textContent === '10000', 'Table rendering preserves the reported amount');
    assert(find('.result-box pre code').textContent.includes('North     10000\nSouth'), 'Code blocks preserve spacing and line breaks');
    assert(find('.result-box ol').children[1].textContent.includes('Keep the source date'), 'Ordered steps retain their continuation text');
    const link = find('.result-box a');
    assert(link.href === 'https://example.com/source?period=2026-09&view=net' && link.rel.includes('noopener'), 'Source links remain usable without replacing the manager page');
    assert(!find('.result-box img') && !find('.result-box a[href^="javascript:"]') && !window.untrustedContentRan, 'Model-authored markup and executable links remain inert');
    assert(document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1, 'The result fits the viewport without horizontal page scrolling');
    assert(find('.table-scroll').tabIndex === 0 && find('.result-box pre').tabIndex === 0, 'Wide data and code can be reached by keyboard');
    find('[data-disclosure="task-checks"]').open = true;
    const paragraph = find('.result-box p');
    const range = document.createRange(); range.selectNodeContents(paragraph);
    getSelection().removeAllRanges(); getSelection().addRange(range);
    const updated = report.replace('Net sales are 32000 cents after the refund.', 'Updated result: net sales are 32000 cents after the refund.');
    await api('/api/workers/sales-reporter/files', { path, content: updated }, 'PUT');
    await sleep(8500);
    assert(!find('.result-box p').textContent.includes('Updated result'), 'A live update waits while the manager has text selected');
    const form = find('form[data-form="result-review"]');
    form.requestSubmit(form.querySelector('button[value="accepted"]'));
    await wait(() => find('#toast').textContent.includes('changed since you opened'), 'unseen result cannot be accepted');
    const unchangedReview = await fetch(`/api${taskURL}`).then(r => r.json());
    assert(!unchangedReview.request.review, 'A deferred redraw cannot authorize acceptance of unseen result bytes');
    getSelection().removeAllRanges();
    await wait(() => find('.result-box p').textContent.includes('Updated result'), 'changed result appears after selection ends');
    assert(find('[data-disclosure="task-checks"]').open, 'Expanded evidence survives the completed-task update');
    const current = await fetch(`/api${taskURL}`).then(r => r.json());
    await api(`/api${taskURL}/review`, { decision: 'accepted', resultSha256: current.request.resultSha256, jobUpdatedUs: current.job.updated_us });
    await wait(() => document.body.textContent.includes('You accepted this result'), 'review from another session becomes visible');
    assert(find('[data-disclosure="task-checks"]').open, 'An external review update keeps the manager’s reading state');
    find('[data-disclosure="task-checks"]').open = false;
    window.scrollTo(0, 0);
    await fetch('/__test/result', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ checks }) });
  } catch (error) {
    await fetch('/__test/result', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ error: error.stack, checks }) });
  }
})();
