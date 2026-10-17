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
 * Update the shared tag definitions cache (e.g. from backend config or API response).
 */
export function setCachedTags(tags) {
  if (tags && typeof tags === "object") {
    cachedTags = { ...tags };
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
      cachedTags = { ...res };
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
