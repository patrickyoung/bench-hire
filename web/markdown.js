/* Readable, escaped Markdown for worker-authored results and job files.
   Deliberately limited: no HTML, images, scripts, embedded content or plugins. */
(() => {
  'use strict';
  const esc = value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&#39;');
  const inline = value => {
    const text = String(value);
    const tokens = /`([^`\n]+)`|\*\*([^*\n]+)\*\*|\[([^\]\n]+)\]\(([^\s)]+)\)/g;
    let out = '', last = 0;
    for (const match of text.matchAll(tokens)) {
      out += esc(text.slice(last, match.index));
      if (match[1] !== undefined) out += `<code>${esc(match[1])}</code>`;
      else if (match[2] !== undefined) out += `<strong>${esc(match[2])}</strong>`;
      else {
        let href = '';
        try { const url = new URL(match[4]); if (['https:', 'http:'].includes(url.protocol)) href = url.href; } catch { /* leave unsupported links as text */ }
        out += href ? `<a href="${esc(href)}" target="_blank" rel="noopener noreferrer">${esc(match[3])}</a>` : esc(match[0]);
      }
      last = match.index + match[0].length;
    }
    return out + esc(text.slice(last));
  };
  const cells = line => {
    const text = line.trim().replace(/^\|/, '').replace(/\|$/, '');
    const result = [];
    let value = '', code = false;
    for (let i = 0; i < text.length; i++) {
      if (text[i] === '\\' && text[i + 1] === '|') { value += '|'; i++; }
      else if (text[i] === '`') { code = !code; value += text[i]; }
      else if (text[i] === '|' && !code) { result.push(value.trim()); value = ''; }
      else value += text[i];
    }
    result.push(value.trim());
    return result;
  };
  const fence = line => line.match(/^ {0,3}(`{3,}|~{3,})([\w+-]*)\s*$/);
  const heading = line => line.match(/^ {0,3}(#{1,6})\s+(.*)$/);
  const item = line => line.match(/^\s*([-*+] |\d+\. )(.*)$/);
  const tableStart = (lines, i) => lines[i]?.includes('|') && lines[i + 1]?.includes('|') && cells(lines[i + 1]).every(cell => /^:?-{3,}:?$/.test(cell)) && cells(lines[i]).length === cells(lines[i + 1]).length;

  window.HireMarkdown = text => {
    const lines = String(text || '').replaceAll('\r\n', '\n').split('\n');
    let html = '', i = 0;
    while (i < lines.length) {
      if (!lines[i].trim()) { i++; continue; }
      const block = fence(lines[i]);
      if (block) {
        const content = [];
        const close = new RegExp(`^ {0,3}${block[1][0]}{${block[1].length},}\\s*$`);
        i++;
        while (i < lines.length && !close.test(lines[i])) content.push(lines[i++]);
        if (i < lines.length) i++;
        html += `<pre tabindex="0" aria-label="Code block"><code>${esc(content.join('\n'))}</code></pre>`;
        continue;
      }
      const title = heading(lines[i]);
      if (title) {
        const level = Math.min(title[1].length + 2, 6);
        html += `<h${level}>${inline(title[2])}</h${level}>`; i++; continue;
      }
      if (tableStart(lines, i)) {
        const columns = cells(lines[i]);
        html += `<div class="table-scroll" tabindex="0" role="region" aria-label="Data table"><table><thead><tr>${columns.map(cell => `<th scope="col">${inline(cell)}</th>`).join('')}</tr></thead><tbody>`;
        i += 2;
        while (i < lines.length && lines[i].includes('|') && cells(lines[i]).length === columns.length) {
          html += `<tr>${cells(lines[i++]).map(cell => `<td>${inline(cell)}</td>`).join('')}</tr>`;
        }
        html += '</tbody></table></div>'; continue;
      }
      const bullet = item(lines[i]);
      if (bullet) {
        const type = /^\d/.test(bullet[1]) ? 'ol' : 'ul';
        const start = type === 'ol' ? ` start="${Number.parseInt(bullet[1], 10)}"` : '';
        html += `<${type}${start}>`;
        while (i < lines.length) {
          const next = item(lines[i]);
          if (!next || (/^\d/.test(next[1]) ? 'ol' : 'ul') !== type) break;
          const content = [next[2]]; i++;
          while (i < lines.length && /^\s+\S/.test(lines[i]) && !item(lines[i]) && !fence(lines[i])) content.push(lines[i++].trim());
          html += `<li>${inline(content.join(' '))}</li>`;
        }
        html += `</${type}>`; continue;
      }
      const paragraph = [lines[i++].trim()];
      while (i < lines.length && lines[i].trim() && !fence(lines[i]) && !heading(lines[i]) && !item(lines[i]) && !tableStart(lines, i)) paragraph.push(lines[i++].trim());
      html += `<p>${inline(paragraph.join(' '))}</p>`;
    }
    return html || '<p class="muted">Empty.</p>';
  };
})();
