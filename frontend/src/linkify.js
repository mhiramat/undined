/**
 * linkify.js: Safely escapes HTML and converts HTTP/HTTPS URLs into clickable <a> links.
 */

export function escapeHtml(str) {
  if (!str) return "";
  return String(str).replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}

/**
 * Escapes HTML in the given text and converts valid HTTP and HTTPS URLs
 * into clickable <a> anchor tags targeting a new tab.
 * Handles trailing punctuation, delimiters (<url>, (url), [url]), and balanced brackets.
 *
 * @param {string} text - Raw text to escape and linkify
 * @returns {string} - HTML string with safe links
 */
export function linkifyUrls(text) {
  if (!text) return "";

  const urlRegex = /https?:\/\/[^\s<>"'`]+/g;
  let result = "";
  let lastIdx = 0;
  let match;

  while ((match = urlRegex.exec(text)) !== null) {
    const start = match.index;
    const end = urlRegex.lastIndex;

    if (start > lastIdx) {
      result += escapeHtml(text.slice(lastIdx, start));
    }

    const candidate = match[0];
    let url = candidate;
    let trailing = "";
    const protoMatch = candidate.match(/^https?:\/\//);
    const protoLen = protoMatch ? protoMatch[0].length : 8;

    while (url.length > protoLen) {
      const lastChar = url[url.length - 1];

      // Strip trailing punctuation commonly appended to URLs in prose
      if (/[.,:;!?]/.test(lastChar)) {
        trailing = lastChar + trailing;
        url = url.slice(0, -1);
        continue;
      }

      // Strip unbalanced closing parentheses: (https://example.com)
      if (lastChar === ")") {
        const openCount = (url.match(/\(/g) || []).length;
        const closeCount = (url.match(/\)/g) || []).length;
        if (closeCount > openCount) {
          trailing = lastChar + trailing;
          url = url.slice(0, -1);
          continue;
        }
      }

      // Strip unbalanced closing square brackets: [https://example.com]
      if (lastChar === "]") {
        const openCount = (url.match(/\[/g) || []).length;
        const closeCount = (url.match(/\]/g) || []).length;
        if (closeCount > openCount) {
          trailing = lastChar + trailing;
          url = url.slice(0, -1);
          continue;
        }
      }

      // Strip unbalanced closing curly braces: {https://example.com}
      if (lastChar === "}") {
        const openCount = (url.match(/\{/g) || []).length;
        const closeCount = (url.match(/\}/g) || []).length;
        if (closeCount > openCount) {
          trailing = lastChar + trailing;
          url = url.slice(0, -1);
          continue;
        }
      }

      break;
    }

    const hostPart = url.slice(protoLen).split("/")[0].split("?")[0].split("#")[0];
    if (hostPart) {
      result += `<a href="${escapeHtml(url)}" target="_blank" rel="noopener noreferrer">${escapeHtml(url)}</a>`;
      if (trailing) {
        result += escapeHtml(trailing);
      }
    } else {
      result += escapeHtml(candidate);
    }

    lastIdx = end;
  }

  if (lastIdx < text.length) {
    result += escapeHtml(text.slice(lastIdx));
  }

  return result;
}
