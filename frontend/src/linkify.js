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
 * Formats a Unicode code point as a visually identifiable badge, e.g. <U+00A0>.
 */
export function escapeUnicodeChar(codePoint) {
  const hex = codePoint.toString(16).toUpperCase().padStart(4, "0");
  return `<span class="unicode-escape" title="Unicode U+${hex}">&lt;U+${hex}&gt;</span>`;
}

/**
 * Escapes HTML and converts any non-ASCII character (codePoint > 127) into a visible badge.
 */
export function escapeHtmlWithUnicode(str) {
  if (!str) return "";
  let out = "";
  for (const ch of str) {
    const cp = ch.codePointAt(0);
    if (cp > 127) {
      out += escapeUnicodeChar(cp);
    } else if (ch === "&") {
      out += "&amp;";
    } else if (ch === "<") {
      out += "&lt;";
    } else if (ch === ">") {
      out += "&gt;";
    } else if (ch === '"') {
      out += "&quot;";
    } else if (ch === "'") {
      out += "&#39;";
    } else {
      out += ch;
    }
  }
  return out;
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

/**
 * Escapes HTML with Unicode escaping for non-ASCII characters, and converts valid URLs into clickable links.
 *
 * @param {string} text - Raw text to escape and linkify
 * @returns {string} - HTML string with safe links and escaped Unicode badges
 */
export function linkifyUrlsWithUnicode(text) {
  if (!text) return "";

  const urlRegex = /https?:\/\/[^\s<>"'`]+/g;
  let result = "";
  let lastIdx = 0;
  let match;

  while ((match = urlRegex.exec(text)) !== null) {
    const start = match.index;
    const end = urlRegex.lastIndex;

    if (start > lastIdx) {
      result += escapeHtmlWithUnicode(text.slice(lastIdx, start));
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
      result += `<a href="${escapeHtml(url)}" target="_blank" rel="noopener noreferrer">${escapeHtmlWithUnicode(url)}</a>`;
      if (trailing) {
        result += escapeHtmlWithUnicode(trailing);
      }
    } else {
      result += escapeHtmlWithUnicode(candidate);
    }

    lastIdx = end;
  }

  if (lastIdx < text.length) {
    result += escapeHtmlWithUnicode(text.slice(lastIdx));
  }

  return result;
}
