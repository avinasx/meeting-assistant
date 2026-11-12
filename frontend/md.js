// md.js — a small, safe Markdown renderer for model output.
//
// Everything is HTML-escaped first; only the tags produced here ever reach
// the page, so model output cannot inject markup.
(function (root) {
  'use strict';

  function escapeHtml(text) {
    return String(text)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  // Inline formatting: `code`, **bold**, *italic*.
  function inline(text) {
    return text
      .split(/(`[^`\n]+`)/g)
      .map(function (part) {
        if (part.length > 2 && part[0] === '`' && part[part.length - 1] === '`') {
          return '<code>' + escapeHtml(part.slice(1, -1)) + '</code>';
        }
        var html = escapeHtml(part);
        html = html.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
        html = html.replace(/(^|[\s(])\*([^*\s][^*\n]*?)\*(?=[\s).,;:!?]|$)/g, '$1<em>$2</em>');
        return html;
      })
      .join('');
  }

  function renderMarkdown(source) {
    var lines = String(source == null ? '' : source).replace(/\r\n?/g, '\n').split('\n');
    var out = [];
    var para = [];
    var list = null; // { type: 'ul' | 'ol', items: [] }
    var code = null; // array of lines while inside a fenced block

    function flushPara() {
      if (para.length) {
        out.push('<p>' + para.map(inline).join('<br>') + '</p>');
        para = [];
      }
    }
    function flushList() {
      if (list) {
        out.push('<' + list.type + '>' +
          list.items.map(function (item) { return '<li>' + inline(item) + '</li>'; }).join('') +
          '</' + list.type + '>');
        list = null;
      }
    }
    function flushCode() {
      out.push('<pre><code>' + escapeHtml(code.join('\n')) + '</code></pre>');
      code = null;
    }

    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];

      if (code) {
        if (/^\s*```\s*$/.test(line)) {
          flushCode();
        } else {
          code.push(line);
        }
        continue;
      }
      if (/^\s*```/.test(line)) {
        flushPara();
        flushList();
        code = [];
        continue;
      }
      if (!line.trim()) {
        flushPara();
        flushList();
        continue;
      }

      var heading = line.match(/^(#{1,6})\s+(.*)$/);
      if (heading) {
        flushPara();
        flushList();
        var level = Math.min(heading[1].length + 2, 5); // "#" -> h3: keep headings small
        out.push('<h' + level + '>' + inline(heading[2]) + '</h' + level + '>');
        continue;
      }

      var bullet = line.match(/^\s*[-*•]\s+(.*)$/);
      var numbered = bullet ? null : line.match(/^\s*\d+[.)]\s+(.*)$/);
      if (bullet || numbered) {
        flushPara();
        var type = bullet ? 'ul' : 'ol';
        if (list && list.type !== type) {
          flushList();
        }
        if (!list) {
          list = { type: type, items: [] };
        }
        list.items.push((bullet || numbered)[1]);
        continue;
      }
      if (list && /^\s{2,}\S/.test(line)) {
        // Indented continuation of the previous list item.
        list.items[list.items.length - 1] += ' ' + line.trim();
        continue;
      }

      flushList();
      para.push(line);
    }

    if (code) {
      flushCode(); // an unterminated fence: the answer is still streaming
    }
    flushPara();
    flushList();
    return out.join('');
  }

  root.renderMarkdown = renderMarkdown;
  root.escapeHtml = escapeHtml;
  if (typeof module !== 'undefined' && module.exports) {
    module.exports = { renderMarkdown: renderMarkdown, escapeHtml: escapeHtml };
  }
})(typeof window !== 'undefined' ? window : globalThis);
