/**
 * Filter parser and matcher for Project UndineD message list.
 *
 * Supports search query syntax:
 *   from:developer
 *   subject:kprobes
 *   before:2026/Sep
 *   after:2026/Jan
 *   is:patch
 *   is:read / is:unread
 *   general keywords
 */

const MONTHS = [
  "jan", "feb", "mar", "apr", "may", "jun",
  "jul", "aug", "sep", "oct", "nov", "dec"
];

export function parseFilterDate(dateStr, isBefore = true) {
  if (!dateStr || typeof dateStr !== "string") return null;
  const s = dateStr.trim();

  // 1. Regex for YYYY/Mon, YYYY/MM, YYYY-MM, or with day: YYYY/Mon/DD, YYYY-MM-DD
  const m = s.match(/^(\d{4})[\/\-](\w{3,9}|\d{1,2})(?:[\/\-](\d{1,2}))?$/i);
  if (m) {
    const year = parseInt(m[1], 10);
    const monStr = m[2].toLowerCase();
    const day = m[3] ? parseInt(m[3], 10) : null;

    let monIdx = -1;
    for (let i = 0; i < MONTHS.length; i++) {
      if (monStr.startsWith(MONTHS[i])) {
        monIdx = i;
        break;
      }
    }
    if (monIdx === -1 && /^\d+$/.test(monStr)) {
      monIdx = parseInt(monStr, 10) - 1;
    }

    if (monIdx >= 0 && monIdx <= 11) {
      if (day !== null) {
        // Specific day given
        const h = isBefore ? 23 : 0;
        const min = isBefore ? 59 : 0;
        const sec = isBefore ? 59 : 0;
        return Date.UTC(year, monIdx, day, h, min, sec);
      }
      // Month given without day
      if (isBefore) {
        // "before:2026/Sep" -> before Sep 1st 00:00:00 UTC
        return Date.UTC(year, monIdx, 1, 0, 0, 0);
      } else {
        // "after:2026/Sep" -> after Sep 1st 00:00:00 UTC
        return Date.UTC(year, monIdx, 1, 0, 0, 0);
      }
    }
  }

  // 2. Try standard Date parsing
  const d = new Date(s);
  if (!isNaN(d.getTime())) {
    return d.getTime();
  }

  return null;
}

export function tokenizeFilterQuery(queryStr) {
  if (!queryStr || typeof queryStr !== "string") return [];
  const tokens = [];
  let current = "";
  let inQuotes = false;
  let quoteChar = "";

  for (let i = 0; i < queryStr.length; i++) {
    const ch = queryStr[i];
    if ((ch === '"' || ch === "'") && (!inQuotes || quoteChar === ch)) {
      inQuotes = !inQuotes;
      quoteChar = inQuotes ? ch : "";
      current += ch;
    } else if (/\s/.test(ch) && !inQuotes) {
      if (current.length > 0) {
        tokens.push(current);
        current = "";
      }
    } else {
      current += ch;
    }
  }
  if (current.length > 0) {
    tokens.push(current);
  }
  return tokens;
}

export function splitTagValues(valStr) {
  if (!valStr) return [];
  const results = [];
  let current = "";
  let inQuotes = false;
  let quoteChar = "";

  for (let i = 0; i < valStr.length; i++) {
    const ch = valStr[i];
    if ((ch === '"' || ch === "'") && (!inQuotes || quoteChar === ch)) {
      inQuotes = !inQuotes;
      quoteChar = inQuotes ? ch : "";
    } else if (ch === "," && !inQuotes) {
      const clean = current.trim().replace(/^["']|["']$/g, "").trim();
      if (clean) results.push(clean);
      current = "";
    } else {
      current += ch;
    }
  }
  const clean = current.trim().replace(/^["']|["']$/g, "").trim();
  if (clean) results.push(clean);
  return results;
}

export function parseFilterQuery(queryStr) {
  if (!queryStr || !queryStr.trim()) return null;

  const rawTokens = tokenizeFilterQuery(queryStr.trim());
  const filter = {
    raw: queryStr.trim(),
    from: "",
    subject: "",
    before: null,
    after: null,
    beforeStr: "",
    afterStr: "",
    tags: [],
    isPatch: null, // true | false | null
    isRead: null,  // true | false | null
    terms: [],
  };

  for (const token of rawTokens) {
    const lower = token.toLowerCase();
    if (lower.startsWith("from:")) {
      filter.from = token.slice(5).trim().replace(/^["']|["']$/g, "");
    } else if (lower.startsWith("subject:")) {
      filter.subject = token.slice(8).trim().replace(/^["']|["']$/g, "");
    } else if (lower.startsWith("before:") || lower.startsWith("until:")) {
      const val = token.slice(token.indexOf(":") + 1).trim().replace(/^["']|["']$/g, "");
      filter.beforeStr = val;
      filter.before = parseFilterDate(val, true);
    } else if (lower.startsWith("after:") || lower.startsWith("since:")) {
      const val = token.slice(token.indexOf(":") + 1).trim().replace(/^["']|["']$/g, "");
      filter.afterStr = val;
      filter.after = parseFilterDate(val, false);
    } else if (lower.startsWith("tag:") || lower.startsWith("tags:")) {
      const val = token.slice(token.indexOf(":") + 1).trim();
      const parsedTags = splitTagValues(val);
      for (const t of parsedTags) {
        if (t && !filter.tags.includes(t)) {
          filter.tags.push(t);
        }
      }
    } else if (lower === "is:patch") {
      filter.isPatch = true;
    } else if (lower === "is:read") {
      filter.isRead = true;
    } else if (lower === "is:unread") {
      filter.isRead = false;
    } else {
      const cleanTerm = token.replace(/^["']|["']$/g, "");
      if (cleanTerm) {
        filter.terms.push(cleanTerm);
      }
    }
  }

  return filter;
}

export function matchMessageFilter(msg, filter) {
  if (!filter) return true;

  if (filter.from) {
    const fromText = `${msg.from_name || ""} ${msg.from_addr || ""}`.toLowerCase();
    if (!fromText.includes(filter.from.toLowerCase())) {
      return false;
    }
  }

  if (filter.subject) {
    const subj = String(msg.subject || "").toLowerCase();
    if (!subj.includes(filter.subject.toLowerCase())) {
      return false;
    }
  }

  const msgTime = (typeof msg.epoch_timestamp === "number" && msg.epoch_timestamp > 0)
    ? msg.epoch_timestamp * 1000
    : (msg.date ? new Date(msg.date).getTime() : 0);

  if (filter.before && msgTime) {
    if (msgTime >= filter.before) {
      return false;
    }
  }

  if (filter.after && msgTime) {
    if (msgTime <= filter.after) {
      return false;
    }
  }

  if (filter.tags && filter.tags.length > 0) {
    const msgKey = (msg.tag_key || "").toLowerCase();
    const msgName = (msg.tag_name || "").toLowerCase();
    const msgColor = (msg.tag_color || "").toLowerCase();

    const hasTagMatch = filter.tags.some((t) => {
      const target = (t || "").trim().toLowerCase();
      if (!target) return false;
      if (target === "none" || target === "untagged") {
        return !msgKey && !msgName;
      }
      return (
        (msgKey && (msgKey === target || msgKey.replace(/^\$/, "") === target.replace(/^\$/, ""))) ||
        (msgName && msgName === target) ||
        (msgColor && msgColor === target)
      );
    });

    if (!hasTagMatch) {
      return false;
    }
  }

  if (filter.isPatch !== null) {
    const isP = Boolean(msg.is_patch);
    if (isP !== filter.isPatch) {
      return false;
    }
  }

  if (filter.isRead !== null) {
    if (Boolean(msg.is_read) !== filter.isRead) {
      return false;
    }
  }

  if (filter.terms && filter.terms.length > 0) {
    const combined = `${msg.subject || ""} ${msg.from_name || ""} ${msg.from_addr || ""}`.toLowerCase();
    for (const term of filter.terms) {
      if (!combined.includes(term.toLowerCase())) {
        return false;
      }
    }
  }

  return true;
}

export function buildFilterQuery({
  from = "",
  subject = "",
  before = "",
  after = "",
  tags = [],
  isPatch = false,
  isRead = "",
}) {
  const parts = [];
  if (from && from.trim()) {
    parts.push(`from:${from.trim()}`);
  }
  if (subject && subject.trim()) {
    parts.push(`subject:${subject.trim()}`);
  }
  if (before && before.trim()) {
    parts.push(`before:${before.trim()}`);
  }
  if (after && after.trim()) {
    parts.push(`after:${after.trim()}`);
  }
  if (Array.isArray(tags) && tags.length > 0) {
    const formatted = tags
      .map((t) => (typeof t === "string" ? t.trim() : ""))
      .filter(Boolean)
      .map((t) => (t.includes(" ") ? `"${t}"` : t));
    if (formatted.length > 0) {
      parts.push(`tag:${formatted.join(",")}`);
    }
  }
  if (isPatch) {
    parts.push("is:patch");
  }
  if (isRead === "read") {
    parts.push("is:read");
  } else if (isRead === "unread") {
    parts.push("is:unread");
  }
  return parts.join(" ");
}
