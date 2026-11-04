/**
 * markdown.js: Secure, zero-dependency Markdown parser and detector for email messages.
 * Escapes all raw HTML to prevent XSS and generates semantic, styled HTML.
 */

import { escapeHtml } from "./linkify.js";

/**
 * Checks whether a given URL is safe for anchor hrefs.
 * Only allows http, https, mailto, or relative anchors.
 */
function isSafeUrl(url) {
  if (!url || typeof url !== "string") return false;
  const trimmed = url.trim().toLowerCase();
  if (trimmed.startsWith("https://") || trimmed.startsWith("http://") || trimmed.startsWith("mailto:")) {
    return true;
  }
  if (trimmed.startsWith("#") || trimmed.startsWith("/")) {
    return true;
  }
  return false;
}

/**
 * Cleans invisible preheader whitespace padding from marketing/HTML emails.
 * Strips invisible/zero-width characters (e.g. \u2007 figure spaces, \u034F combining grapheme joiners)
 * and collapses excessive blank lines down to clean paragraph breaks.
 *
 * @param {string} text - Raw email body
 * @returns {string} - Cleaned body text
 */
export function cleanBodyText(text) {
  if (!text || typeof text !== "string") return "";
  const normalized = text.replace(/\r\n/g, "\n").replace(/\r/g, "\n");
  const lines = normalized.split("\n");
  const cleaned = [];

  const invisibleOnlyRegex = /^[\s\u2000-\u200F\u034F\uFEFF\u200B-\u200D]*$/;
  const trailingInvisibleRegex = /[\u2000-\u200F\u034F\uFEFF\u200B-\u200D\s]+$/;

  for (const line of lines) {
    if (invisibleOnlyRegex.test(line)) {
      cleaned.push("");
    } else {
      cleaned.push(line.replace(trailingInvisibleRegex, ""));
    }
  }

  return cleaned.join("\n").replace(/\n{3,}/g, "\n\n").trim();
}

/**
 * Heuristically detects whether a message body is formatted in Markdown.
 *
 * @param {string} text - Raw text body of the email
 * @returns {boolean}
 */
export function isMarkdown(text) {
  if (!text || typeof text !== "string") return false;

  const cleaned = cleanBodyText(text);
  if (cleaned.length < 10) return false;

  // Patch or diff emails should never be treated as markdown
  if (/^--- [^\n]+\n\+\+\+ [^\n]+/m.test(cleaned) || /^diff --git /m.test(cleaned)) {
    return false;
  }

  // 1. Fenced code blocks
  if (/```[a-z0-9_-]*\r?\n[\s\S]*?```/i.test(cleaned)) {
    return true;
  }

  // 2. Markdown tables (| col | col | \n | --- | --- |)
  if (/\|[^\r\n]+\|\r?\n\|[-:\s|]+\|\r?\n\|[^\r\n]+\|/.test(cleaned)) {
    return true;
  }

  // 3. Markdown ATX headings (# Heading, ## Heading)
  const headingMatches = cleaned.match(/(?:^|\n)#{1,6}\s+[^\r\n]+/g);
  if (headingMatches && headingMatches.length >= 1) {
    if (headingMatches.length >= 2) return true;
    if (/(?:^|\n)\s*[-*+]\s+[^\r\n]+/.test(cleaned)) return true;
    if (/\*\*[^*\r\n]+\*\*/.test(cleaned)) return true;
    if (/`[^`\r\n]+`/.test(cleaned)) return true;
  }

  // 4. Task lists (- [ ] or - [x])
  if (/(?:^|\n)\s*[-*+]\s+\[[ xX]\]\s+[^\r\n]+/.test(cleaned)) {
    return true;
  }

  // 5. Explicit Markdown links: [Title](https://...)
  const linkMatches = cleaned.match(/\[[^\]\r\n]+\]\(https?:\/\/[^\s\)]+\)/g);
  if (linkMatches && linkMatches.length >= 2) {
    return true;
  }

  // 6. Multiple list items combined with inline code or bold
  const listMatches = cleaned.match(/(?:^|\n)\s*[-*+]\s+[^\r\n]+/g);
  if (listMatches && listMatches.length >= 3) {
    if (/\*\*[^*\r\n]+\*\*/.test(cleaned) || /`[^`\r\n]+`/.test(cleaned)) {
      return true;
    }
  }

  return false;
}

/**
 * Parse inline Markdown formatting: bold, italic, code, links, bare URLs.
 */
function parseInline(str) {
  if (!str) return "";

  // 1. Inline code: `code`
  const codePlaceholders = [];
  str = str.replace(/`([^`\r\n]+)`/g, (_, code) => {
    const idx = codePlaceholders.length;
    codePlaceholders.push(`<code class="md-inline-code">${escapeHtml(code)}</code>`);
    return `@@MDCODE${idx}@@`;
  });

  // 2. Markdown autolinks: <https://...> or <mailto:...>
  const linkPlaceholders = [];
  str = str.replace(/<(https?:\/\/[^\s>]+)>/g, (_, url) => {
    const safe = isSafeUrl(url);
    const href = safe ? escapeHtml(url) : "#";
    const idx = linkPlaceholders.length;
    linkPlaceholders.push(`<a href="${href}" target="_blank" rel="noopener noreferrer" class="md-link">${escapeHtml(url)}</a>`);
    return `@@MDLINK${idx}@@`;
  });

  // 3. Markdown explicit links: [text](url)
  str = str.replace(/\[([^\]\r\n]+)\]\(([^)\s]+)\)/g, (_, label, url) => {
    const safe = isSafeUrl(url);
    const href = safe ? escapeHtml(url) : "#";
    const idx = linkPlaceholders.length;
    linkPlaceholders.push(`<a href="${href}" target="_blank" rel="noopener noreferrer" class="md-link">${parseInline(label)}</a>`);
    return `@@MDLINK${idx}@@`;
  });

  // 4. Auto-link bare URLs: https://...
  str = str.replace(/(https?:\/\/[^\s<>"'`]+)/g, (url) => {
    let cleanUrl = url;
    let trailing = "";
    while (cleanUrl.length > 8) {
      const last = cleanUrl[cleanUrl.length - 1];
      if (/[.,:;!?)]/.test(last)) {
        trailing = last + trailing;
        cleanUrl = cleanUrl.slice(0, -1);
      } else {
        break;
      }
    }
    const safe = isSafeUrl(cleanUrl);
    const href = safe ? escapeHtml(cleanUrl) : "#";
    const idx = linkPlaceholders.length;
    linkPlaceholders.push(`<a href="${href}" target="_blank" rel="noopener noreferrer" class="md-link">${escapeHtml(cleanUrl)}</a>${escapeHtml(trailing)}`);
    return `@@MDLINK${idx}@@`;
  });

  // 5. Escape raw HTML entities in surrounding text
  str = escapeHtml(str);

  // 6. Strikethrough: ~~text~~
  str = str.replace(/~~([^~\r\n]+)~~/g, "<del>$1</del>");

  // 7. Bold + Italic: ***text*** or ___text___
  str = str.replace(/\*\*\*([^*\r\n]+)\*\*\*/g, "<strong><em>$1</em></strong>");
  str = str.replace(/(?:^|\s)___([^_\r\n]+)___(?=\s|$)/g, "<strong><em>$1</em></strong>");

  // 8. Bold: **text** or __text__
  str = str.replace(/\*\*([^*\r\n]+)\*\*/g, "<strong>$1</strong>");
  str = str.replace(/(?:^|\s)__([^_\r\n]+)__(?=\s|$)/g, "<strong>$1</strong>");

  // 9. Italic: *text* or _text_
  str = str.replace(/\*([^*\r\n]+)\*/g, "<em>$1</em>");
  str = str.replace(/(?:^|\s)_([^_\r\n]+)_(?=\s|$)/g, "<em>$1</em>");

  // 10. Restore links
  str = str.replace(/@@MDLINK(\d+)@@/g, (_, idx) => linkPlaceholders[Number(idx)] || "");

  // 11. Restore code
  str = str.replace(/@@MDCODE(\d+)@@/g, (_, idx) => codePlaceholders[Number(idx)] || "");

  return str;
}

/**
 * Render Markdown text into secure, semantic HTML.
 *
 * @param {string} md - Raw markdown text
 * @returns {string} - Rendered HTML
 */
export function renderMarkdown(md) {
  if (!md || typeof md !== "string") return "";

  const cleaned = cleanBodyText(md);
  if (!cleaned) return "";

  // Step 1: Extract fenced code blocks
  const codeBlocks = [];
  const withBlocksExtracted = cleaned.replace(/(?:^|\n)```([a-z0-9_-]*)\n([\s\S]*?)\n```(?:\n|$)/gi, (_, lang, content) => {
    const idx = codeBlocks.length;
    const cleanLang = (lang || "").trim();
    codeBlocks.push({ lang: cleanLang, content });
    return `\n\n@@MDBLOCK${idx}@@\n\n`;
  });

  // Split into block sections separated by blank lines
  const lines = withBlocksExtracted.split("\n");
  const output = [];

  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const trimmed = line.trim();

    // Empty or invisible lines
    if (!trimmed || /^[\s\u2000-\u200F\u034F\uFEFF\u200B-\u200D]*$/.test(trimmed)) {
      i++;
      continue;
    }

    // Fenced code block placeholder
    const blockMatch = trimmed.match(/^@@MDBLOCK(\d+)@@$/);
    if (blockMatch) {
      const idx = Number(blockMatch[1]);
      const block = codeBlocks[idx];
      if (block) {
        const langBadge = block.lang ? `<div class="md-code-header"><span class="md-code-lang">${escapeHtml(block.lang)}</span></div>` : "";
        output.push(`
          <div class="md-code-card">
            ${langBadge}
            <pre class="md-code-block"><code>${escapeHtml(block.content)}</code></pre>
          </div>
        `);
      }
      i++;
      continue;
    }

    // Horizontal Rule (---, ***, ___)
    if (/^(?:-{3,}|\*{3,}|_{3,})$/.test(trimmed)) {
      output.push("<hr class=\"md-hr\" />");
      i++;
      continue;
    }

    // Headings (# H1 to ###### H6)
    const headingMatch = line.match(/^(#{1,6})\s+(.+)$/);
    if (headingMatch) {
      const level = headingMatch[1].length;
      const title = parseInline(headingMatch[2].trim());
      output.push(`<h${level} class="md-h md-h${level}">${title}</h${level}>`);
      i++;
      continue;
    }

    // Tables: must have header line | ... | and separator | --- |
    if (trimmed.startsWith("|") && trimmed.endsWith("|") && i + 1 < lines.length && /^\s*\|[-:\s|]+\|\s*$/.test(lines[i + 1])) {
      const headerLine = trimmed;
      const sepLine = lines[i + 1].trim();
      i += 2;

      const headerCells = headerLine.slice(1, -1).split("|").map((c) => c.trim());
      const sepCells = sepLine.slice(1, -1).split("|").map((c) => c.trim());
      const aligns = sepCells.map((s) => {
        const left = s.startsWith(":");
        const right = s.endsWith(":");
        if (left && right) return "center";
        if (right) return "right";
        if (left) return "left";
        return "left";
      });

      const bodyRows = [];
      while (i < lines.length && lines[i].trim().startsWith("|") && lines[i].trim().endsWith("|")) {
        const rowCells = lines[i].trim().slice(1, -1).split("|").map((c) => c.trim());
        bodyRows.push(rowCells);
        i++;
      }

      let tableHtml = `<div class="md-table-wrapper"><table class="md-table"><thead><tr>`;
      headerCells.forEach((c, idx) => {
        const align = aligns[idx] || "left";
        tableHtml += `<th style="text-align: ${align}">${parseInline(c)}</th>`;
      });
      tableHtml += `</tr></thead><tbody>`;

      bodyRows.forEach((row) => {
        tableHtml += `<tr>`;
        headerCells.forEach((_, idx) => {
          const c = row[idx] || "";
          const align = aligns[idx] || "left";
          tableHtml += `<td style="text-align: ${align}">${parseInline(c)}</td>`;
        });
        tableHtml += `</tr>`;
      });

      tableHtml += `</tbody></table></div>`;
      output.push(tableHtml);
      continue;
    }

    // Blockquotes: lines starting with >
    if (line.startsWith(">") || line.startsWith(" >")) {
      const quoteLines = [];
      while (i < lines.length && (lines[i].startsWith(">") || lines[i].startsWith(" >") || (lines[i].trim() && quoteLines.length > 0 && !lines[i].startsWith("#")))) {
        let qLine = lines[i].replace(/^\s*>\s?/, "");
        quoteLines.push(qLine);
        i++;
        if (i < lines.length && !lines[i].trim()) {
          break;
        }
      }
      const quoteHtml = renderMarkdown(quoteLines.join("\n"));
      output.push(`<blockquote class="md-blockquote">${quoteHtml}</blockquote>`);
      continue;
    }

    // Lists (unordered or ordered, including task items)
    const isUnordered = /^\s*[-*+]\s+/.test(line);
    const isOrdered = /^\s*\d+\.\s+/.test(line);

    if (isUnordered || isOrdered) {
      const listTag = isOrdered ? "ol" : "ul";
      const items = [];

      while (i < lines.length) {
        const currentLine = lines[i];
        const unMatch = currentLine.match(/^\s*[-*+]\s+(.+)$/);
        const orMatch = currentLine.match(/^\s*\d+\.\s+(.+)$/);

        if (unMatch || orMatch) {
          const itemContent = (unMatch ? unMatch[1] : orMatch[1]).trim();
          // Check for task checkbox: [ ] or [x]
          const taskMatch = itemContent.match(/^\[([ xX])\]\s+(.+)$/);
          if (taskMatch) {
            const checked = taskMatch[1].toLowerCase() === "x";
            items.push(`<li class="md-task-item"><input type="checkbox" disabled ${checked ? "checked" : ""} class="md-task-checkbox"> ${parseInline(taskMatch[2])}</li>`);
          } else {
            items.push(`<li>${parseInline(itemContent)}</li>`);
          }
          i++;
        } else if (currentLine.startsWith("  ") || currentLine.startsWith("\t")) {
          // Continuation of previous list item
          if (items.length > 0) {
            items[items.length - 1] += "<br>" + parseInline(currentLine.trim());
          }
          i++;
        } else {
          break;
        }
      }

      output.push(`<${listTag} class="md-list">${items.join("")}</${listTag}>`);
      continue;
    }

    // Regular paragraph
    const paraLines = [];
    while (i < lines.length && lines[i].trim() && !lines[i].trim().match(/^@@MDBLOCK\d+@@$/)) {
      const cur = lines[i];
      if (/^#{1,6}\s+/.test(cur) || /^\s*[-*+]\s+/.test(cur) || /^\s*\d+\.\s+/.test(cur) || cur.startsWith(">") || /^(?:-{3,}|\*{3,}|_{3,})$/.test(cur.trim())) {
        break;
      }
      paraLines.push(cur);
      i++;
    }

    if (paraLines.length > 0) {
      const paraText = paraLines.map((l) => parseInline(l)).join("<br>");
      output.push(`<p class="md-p">${paraText}</p>`);
    }
  }

  return output.join("\n");
}
