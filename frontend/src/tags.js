/**
 * Unified Tag Definitions and State for Project UndineD.
 *
 * Provides a single shared source of truth for color tags across
 * the message list filter, viewer pane header dropdown, and store.
 */

import { fetchTags } from "./api.js";

export const DEFAULT_TAGS = {
  // mailbox.org (Open-Xchange) custom color flags
  "$cl_1": { tag: "Red", color: "#CC0000" },
  "$cl_2": { tag: "Blue", color: "#3333FF" },
  "$cl_3": { tag: "Green", color: "#006600" },
  "$cl_4": { tag: "Gray", color: "#999999" },
  "$cl_5": { tag: "Purple", color: "#6600CC" },
  "$cl_6": { tag: "Light green", color: "#33CC00" },
  "$cl_7": { tag: "Orange", color: "#FF9900" },
  "$cl_8": { tag: "Pink", color: "#CC33CC" },
  "$cl_9": { tag: "Light blue", color: "#33CCFF" },
  "$cl_10": { tag: "Yellow", color: "#FFFF00" },
  // Mozilla Thunderbird default labels
  "$label1": { tag: "Important", color: "#FF2600" },
  "$label2": { tag: "Work", color: "#FF9900" },
  "$label3": { tag: "Personal", color: "#009900" },
  "$label4": { tag: "To Do", color: "#3333FF" },
  "$label5": { tag: "Later", color: "#993399" },
};

let cachedTags = null;

/**
 * Return [key, tagDef] entries ordered by numeric priority rank:
 * $cl_1..$cl_10 -> 1..10, $label1..$label5 -> 11..15, custom tags -> 50 (lexicographic tie-break).
 */
export function sortTagEntries(tags) {
  if (!tags || typeof tags !== "object") return [];
  return Object.entries(tags).sort(([ka], [kb]) => {
    const ra = getTagPriorityRank(ka);
    const rb = getTagPriorityRank(kb);
    if (ra !== rb) return ra - rb;
    return String(ka || "").localeCompare(String(kb || ""));
  });
}

function normalizeTagMap(tags) {
  return Object.fromEntries(sortTagEntries(tags));
}

/**
 * Update the shared tag definitions cache (e.g. from backend config or API response).
 */
export function setCachedTags(tags) {
  if (tags && typeof tags === "object") {
    cachedTags = normalizeTagMap(tags);
  }
}

/**
 * Return currently cached tags, or DEFAULT_TAGS fallback if not yet fetched.
 */
export function getCachedTags() {
  return cachedTags ? { ...cachedTags } : { ...DEFAULT_TAGS };
}

/**
 * Fetch tag definitions from /api/tags and cache them.
 * Falls back to DEFAULT_TAGS on failure.
 */
export async function fetchAndCacheTags() {
  if (cachedTags) return cachedTags;
  try {
    const res = await fetchTags();
    if (res && typeof res === "object") {
      cachedTags = normalizeTagMap(res);
      return cachedTags;
    }
  } catch (err) {
    console.warn("Failed to fetch tags from API, using default tags:", err);
  }
  if (!cachedTags) {
    cachedTags = { ...DEFAULT_TAGS };
  }
  return cachedTags;
}

/**
 * Get tag definitions asynchronously (fetches if not already cached).
 */
export async function getTags() {
  if (cachedTags) return cachedTags;
  return fetchAndCacheTags();
}

/**
 * Returns numeric priority rank for a tag key:
 * $cl_1..$cl_99 -> 1..99, $label1..$label99 -> 11..109, other custom tags -> 50, untagged -> 9999.
 */
export function getTagPriorityRank(key) {
  if (!key) return 9999;
  const lower = String(key).trim().toLowerCase();
  if (!lower) return 9999;
  if (lower.startsWith("$cl_")) {
    const n = parseInt(lower.slice(4), 10);
    if (!isNaN(n) && n >= 1 && n <= 99) return n;
  }
  if (lower.startsWith("$label")) {
    const n = parseInt(lower.slice(6), 10);
    if (!isNaN(n) && n >= 1 && n <= 99) return 10 + n;
  }
  return 50;
}

/**
 * Returns all resolved tags attached to a message object, ordered by priority rank.
 */
export function getMessageTags(msg) {
  if (!msg) return [];
  if (Array.isArray(msg.tags) && msg.tags.length > 0) {
    return [...msg.tags].sort((a, b) => {
      const ra = getTagPriorityRank(a?.key);
      const rb = getTagPriorityRank(b?.key);
      if (ra !== rb) return ra - rb;
      return String(a?.key || "").localeCompare(String(b?.key || ""));
    });
  }
  if (msg.tag_key || msg.tag_name) {
    return [
      {
        key: msg.tag_key || "",
        tag: msg.tag_name || msg.tag_key || "",
        color: msg.tag_color || "",
      },
    ];
  }
  if (msg.flags && typeof msg.flags === "string") {
    const available = getCachedTags();
    const lowerMap = new Map();
    for (const [k, v] of Object.entries(available)) {
      lowerMap.set(k.toLowerCase(), { key: k, tag: v.tag || k, color: v.color || "" });
    }
    const found = [];
    const seen = new Set();
    for (const tok of msg.flags.replace(/,/g, " ").split(/\s+/)) {
      const entry = lowerMap.get(tok.toLowerCase());
      if (entry && !seen.has(entry.key.toLowerCase())) {
        seen.add(entry.key.toLowerCase());
        found.push(entry);
      }
    }
    if (found.length > 0) {
      return found.sort((a, b) => getTagPriorityRank(a.key) - getTagPriorityRank(b.key));
    }
  }
  return [];
}

/**
 * Checks whether a message currently has a given tag key attached (case-insensitive).
 */
export function hasMessageTag(msg, tagKey) {
  if (!msg || !tagKey) return false;
  const target = String(tagKey).trim().toLowerCase();
  if (!target) return false;
  return getMessageTags(msg).some((t) => String(t?.key || "").trim().toLowerCase() === target);
}

/**
 * Helper to test whether a tag key or definition matches any target in a list of query tags.
 */
export function isTagMatch(key, tagDef, targetList) {
  if (!targetList || targetList.length === 0) return false;
  const k = (key || "").toLowerCase();
  const kBare = k.replace(/^\$/, "");
  const name = (tagDef?.tag || "").toLowerCase();
  const color = (tagDef?.color || "").toLowerCase();

  return targetList.some((item) => {
    const target = (item || "").trim().toLowerCase();
    if (!target) return false;
    return (
      target === k ||
      target === kBare ||
      target === name ||
      target === color
    );
  });
}
