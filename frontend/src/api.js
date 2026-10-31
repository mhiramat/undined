/**
 * API client wrapper for UndineD REST endpoints.
 */

const BASE_URL = "";

// Track recently acted-upon message IDs to suppress self-echo SSE events
const suppressedActionMessageIds = new Map();

export function suppressEventForMessage(idOrMsgId, durationMs = 1000) {
  if (idOrMsgId == null) return;
  const key = String(idOrMsgId).replace(/^<|>$/g, "").trim();
  if (!key) return;
  const expiry = Date.now() + durationMs;
  suppressedActionMessageIds.set(key, expiry);
}

export function isEventSuppressed(idOrMsgId) {
  if (idOrMsgId == null) return false;
  const key = String(idOrMsgId).replace(/^<|>$/g, "").trim();
  if (!key) return false;
  const expiry = suppressedActionMessageIds.get(key);
  if (!expiry) return false;
  if (Date.now() > expiry) {
    suppressedActionMessageIds.delete(key);
    return false;
  }
  return true;
}

// Single-flight guard for fetchFolders
let inFlightFetchFoldersPromise = null;

export async function fetchFolders() {
  if (inFlightFetchFoldersPromise) {
    return inFlightFetchFoldersPromise;
  }
  inFlightFetchFoldersPromise = (async () => {
    try {
      const res = await fetch(`${BASE_URL}/api/folders`);
      if (!res.ok) throw new Error("Failed to fetch folders");
      return await res.json();
    } finally {
      inFlightFetchFoldersPromise = null;
    }
  })();
  return inFlightFetchFoldersPromise;
}

export async function syncFullFolderSummaries(folderId) {
  const res = await fetch(`${BASE_URL}/api/folders/${folderId}/sync-full`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to download full summaries" }));
    throw new Error(err.detail || "Failed to download full summaries");
  }
  return res.json();
}

export async function fetchSyncStatus(folderId) {
  const res = await fetch(`${BASE_URL}/api/folders/${folderId}/sync-status`);
  if (!res.ok) throw new Error("Failed to fetch sync status");
  return res.json();
}

export async function cancelFolderSync(folderId) {
  const res = await fetch(`${BASE_URL}/api/folders/${folderId}/sync-cancel`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
  });
  if (!res.ok) throw new Error("Failed to cancel summary sync");
  return res.json();
}

// Single-flight guard for fetchMessages
const inFlightFetchMessagesMap = new Map();

export async function fetchMessages(folderId, limit = null, offset = 0) {
  const flightKey = `${folderId}_${limit}_${offset}`;
  if (inFlightFetchMessagesMap.has(flightKey)) {
    return inFlightFetchMessagesMap.get(flightKey);
  }
  const flightPromise = (async () => {
    try {
      const params = new URLSearchParams();
      if (folderId != null) params.append("folder_id", folderId);
      if (limit != null && limit > 0) params.append("limit", limit);
      if (offset > 0) params.append("offset", offset);
      const query = params.toString() ? `?${params.toString()}` : "";
      const res = await fetch(`${BASE_URL}/api/messages${query}`);
      if (!res.ok) throw new Error("Failed to fetch messages");
      return await res.json();
    } finally {
      inFlightFetchMessagesMap.delete(flightKey);
    }
  })();
  inFlightFetchMessagesMap.set(flightKey, flightPromise);
  return flightPromise;
}

// In-memory cache for downloaded message details
const messageDetailCache = new Map();
const MAX_CACHE_SIZE = 1000;

export function getCachedMessageDetail(idOrMsgId) {
  if (idOrMsgId == null) return null;
  return messageDetailCache.get(String(idOrMsgId)) || null;
}

export function hasCachedMessageDetail(idOrMsgId) {
  if (idOrMsgId == null) return false;
  return messageDetailCache.has(String(idOrMsgId));
}

export function setCachedMessageDetail(idOrMsgId, detail) {
  if (idOrMsgId == null || !detail) return;
  const key = String(idOrMsgId);
  if (messageDetailCache.size >= MAX_CACHE_SIZE && !messageDetailCache.has(key)) {
    const firstKey = messageDetailCache.keys().next().value;
    messageDetailCache.delete(firstKey);
  }
  messageDetailCache.set(key, detail);
  if (detail?.message?.id != null) {
    messageDetailCache.set(String(detail.message.id), detail);
  }
  if (detail?.message?.message_id) {
    messageDetailCache.set(String(detail.message.message_id), detail);
  }
}

export function invalidateMessageDetailCache(idOrMsgId) {
  if (idOrMsgId != null) {
    messageDetailCache.delete(String(idOrMsgId));
  }
}

export function clearMessageDetailCache() {
  messageDetailCache.clear();
}

export async function fetchMessageDetail(idOrMsgId, { bypassCache = false } = {}) {
  suppressEventForMessage(idOrMsgId, 1000);
  const key = String(idOrMsgId);
  if (!bypassCache && messageDetailCache.has(key)) {
    return messageDetailCache.get(key);
  }

  const res = await fetch(`${BASE_URL}/api/messages/${encodeURIComponent(idOrMsgId)}`);
  if (!res.ok) throw new Error("Failed to fetch message detail");
  const data = await res.json();
  if (data?.message?.id != null) suppressEventForMessage(data.message.id, 1000);
  if (data?.message?.message_id) suppressEventForMessage(data.message.message_id, 1000);
  setCachedMessageDetail(idOrMsgId, data);
  return data;
}

export async function markMessageRead(dbId, isRead = true) {
  suppressEventForMessage(dbId, 1000);
  const res = await fetch(`${BASE_URL}/api/messages/${dbId}/read`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ is_read: isRead }),
  });
  const data = await res.json();
  const cached = messageDetailCache.get(String(dbId));
  if (cached && cached.message) {
    cached.message.is_read = isRead ? 1 : 0;
    if (data.file_path) {
      cached.message.file_path = data.file_path;
    }
  }
  return data;
}

export async function fetchMessageMboxPath(idOrMsgId) {
  const res = await fetch(`${BASE_URL}/api/messages/${encodeURIComponent(idOrMsgId)}/mbox-path`);
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to get mbox path" }));
    throw new Error(err.detail || "Failed to get mbox path");
  }
  return res.json();
}

export async function deleteMessage(messageIdOrDbId) {
  const res = await fetch(`${BASE_URL}/api/messages/${encodeURIComponent(messageIdOrDbId)}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to delete message" }));
    throw new Error(err.detail || "Failed to delete message");
  }
  messageDetailCache.delete(String(messageIdOrDbId));
  return res.json();
}

export async function importB4Thread(messageId) {
  suppressEventForMessage(messageId, 1500);
  const res = await fetch(`${BASE_URL}/api/threads/import-b4`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message_id: messageId }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to import via b4" }));
    throw new Error(err.detail || "Failed to import via b4");
  }
  const data = await res.json();
  clearMessageDetailCache();
  return data;
}

export async function applyPatchSeries(messageId, targetGitRepo = null, branchName = null, baseCommit = null) {
  const res = await fetch(`${BASE_URL}/api/patch/apply`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      message_id: messageId,
      target_git_repo: targetGitRepo,
      branch_name: branchName,
      base_commit: baseCommit,
    }),
  });
  return res.json();
}

export async function abortApplyPatchSeries({ target_git_repo = null, branch_name = null, base_commit = null, original_branch = null, head_before = null } = {}) {
  const res = await fetch(`${BASE_URL}/api/patch/apply/abort`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      target_git_repo,
      branch_name,
      base_commit,
      original_branch,
      head_before,
    }),
  });
  return res.json();
}

export async function openEditor(messageId, notes = [], generalComment = "", customBody = null, customTo = null, customCc = null, customSubject = null) {
  const res = await fetch(`${BASE_URL}/api/composer/open-editor`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      message_id: messageId,
      notes: notes,
      general_comment: generalComment,
      custom_body: customBody,
      custom_to: customTo,
      custom_cc: customCc,
      custom_subject: customSubject,
    }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to launch editor" }));
    throw new Error(err.detail || "Failed to launch editor");
  }
  return res.json();
}

export async function searchMessages(query) {
  const res = await fetch(`${BASE_URL}/api/search?q=${encodeURIComponent(query)}`);
  if (!res.ok) throw new Error("Search query failed");
  return res.json();
}

export async function previewQuote(messageId, notes = [], generalComment = "") {
  const res = await fetch(`${BASE_URL}/api/composer/preview-quote`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      message_id: messageId,
      notes: notes,
      general_comment: generalComment,
    }),
  });
  if (!res.ok) throw new Error("Failed to preview quote");
  return res.json();
}

export async function sendEmail({
  to_addr,
  cc_addr = null,
  subject,
  body_text,
  in_reply_to = null,
  references = null,
  orig_msg_id = null,
  draft_id = null,
}) {
  const res = await fetch(`${BASE_URL}/api/smtp/send`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      to_addr,
      cc_addr,
      subject,
      body_text,
      in_reply_to,
      references,
      orig_msg_id,
      draft_id,
    }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to send email" }));
    throw new Error(err.detail || "Failed to send email");
  }
  return res.json();
}

export async function setMessageReplied(messageId, isReplied = true) {
  suppressEventForMessage(messageId, 1000);
  const res = await fetch(`${BASE_URL}/api/messages/${messageId}/replied`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ is_replied: isReplied }),
  });
  if (!res.ok) throw new Error("Failed to update message replied status");
  return res.json();
}

export async function fetchRawHeaders(messageId) {
  const res = await fetch(`${BASE_URL}/api/messages/${messageId}/raw-header`);
  if (!res.ok) throw new Error("Failed to fetch raw headers");
  const contentType = res.headers.get("content-type") || "";
  if (contentType.includes("application/json")) {
    return res.json();
  }
  const text = await res.text();
  try {
    return JSON.parse(text);
  } catch {
    return { raw_headers: text };
  }
}

export async function saveDraft({ to_addr, cc_addr = null, subject, body_text, in_reply_to = null, references = null, draft_id = null }) {
  const res = await fetch(`${BASE_URL}/api/composer/save-draft`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      to_addr,
      cc_addr,
      subject,
      body_text,
      in_reply_to,
      references,
      draft_id,
    }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to save draft" }));
    throw new Error(err.detail || "Failed to save draft");
  }
  return res.json();
}

export async function fetchConfig() {
  const res = await fetch(`${BASE_URL}/api/config`);
  if (!res.ok) throw new Error("Failed to fetch configuration");
  return res.json();
}

// In-memory cache for Patchwork message info
const patchworkCache = new Map();

export async function fetchPatchworkInfo(msgid, refresh = false) {
  if (!msgid) return { enabled: false, found: false };
  const clean = String(msgid).trim().replace(/^<|>$/g, "");
  if (refresh) {
    patchworkCache.delete(clean);
  } else if (patchworkCache.has(clean)) {
    return patchworkCache.get(clean);
  }

  try {
    const res = await fetch(`${BASE_URL}/api/patchwork/info?msgid=${encodeURIComponent(clean)}${refresh ? "&refresh=true" : ""}`);
    if (!res.ok) {
      let errDetail = "Patchwork request failed";
      try {
        const errJson = await res.json();
        errDetail = errJson.error || errJson.detail || errDetail;
      } catch {
        const text = await res.text().catch(() => "");
        if (text) errDetail = text;
      }
      throw new Error(errDetail);
    }
    const data = await res.json();
    const result = data || { enabled: true, found: false, msgid: clean };
    patchworkCache.set(clean, result);
    return result;
  } catch (err) {
    if (err.message && (err.message.includes("configured") || err.message.includes("failed") || err.message.includes("status"))) {
      throw err;
    }
    return { enabled: false, found: false, error: err.message };
  }
}

export async function updatePatchworkPatch(patchId, updateData) {
  const res = await fetch(`${BASE_URL}/api/patchwork/patches/${patchId}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(updateData),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to update patch on Patchwork" }));
    throw new Error(err.detail || "Failed to update patch on Patchwork");
  }
  const data = await res.json();
  // Clear cached info so fresh data will be fetched
  patchworkCache.clear();
  return data;
}

export async function delegatePatchworkPatchToMe(patchId) {
  const res = await fetch(`${BASE_URL}/api/patchwork/patches/${patchId}/delegate-to-me`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to delegate patch to me" }));
    throw new Error(err.detail || "Failed to delegate patch to me");
  }
  const data = await res.json();
  patchworkCache.clear();
  return data;
}

export async function fetchMyPatchworkPatches(params = {}) {
  const searchParams = new URLSearchParams();
  if (params.state) searchParams.append("state", params.state);
  if (params.project) searchParams.append("project", params.project);
  if (params.limit) searchParams.append("limit", params.limit);
  const q = searchParams.toString() ? `?${searchParams.toString()}` : "";
  const res = await fetch(`${BASE_URL}/api/patchwork/my-patches${q}`);
  if (!res.ok) throw new Error("Failed to fetch delegated patches");
  return res.json();
}

export async function syncTodoFromPatchwork() {
  const res = await fetch(`${BASE_URL}/api/patchwork/sync-todo`, {
    method: "POST",
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to sync TODO from Patchwork" }));
    throw new Error(err.detail || "Failed to sync TODO from Patchwork");
  }
  return res.json();
}

export async function fetchTags() {
  const res = await fetch(`${BASE_URL}/api/tags`);
  if (!res.ok) throw new Error("Failed to fetch tags");
  return res.json();
}

export async function setMessageTag(messageDbId, tagKey = null) {
  suppressEventForMessage(messageDbId, 1000);
  const res = await fetch(`${BASE_URL}/api/messages/${messageDbId}/tag`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ tag: tagKey }),
  });
  if (!res.ok) throw new Error("Failed to update message tag");
  return res.json();
}

export async function fetchMessageComments(idOrMsgId, state = null) {
  const query = state ? `?state=${encodeURIComponent(state)}` : "";
  const res = await fetch(`${BASE_URL}/api/messages/${encodeURIComponent(idOrMsgId)}/comments${query}`);
  if (!res.ok) throw new Error("Failed to fetch message comments");
  return res.json();
}

export async function saveMessageComment(idOrMsgId, commentData) {
  suppressEventForMessage(idOrMsgId, 1000);
  const res = await fetch(`${BASE_URL}/api/messages/${encodeURIComponent(idOrMsgId)}/comments`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(commentData),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to save comment" }));
    throw new Error(err.detail || "Failed to save comment");
  }
  invalidateMessageDetailCache(idOrMsgId);
  return res.json();
}

export async function discardDraftComments(idOrMsgId) {
  const res = await fetch(`${BASE_URL}/api/messages/${encodeURIComponent(idOrMsgId)}/comments/discard`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to discard draft comments" }));
    throw new Error(err.detail || "Failed to discard draft comments");
  }
  invalidateMessageDetailCache(idOrMsgId);
  return res.json();
}

export async function deleteMessageComment(idOrMsgId, commentId) {
  const res = await fetch(`${BASE_URL}/api/messages/${encodeURIComponent(idOrMsgId)}/comments/${commentId}`, {
    method: "DELETE",
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to delete comment" }));
    throw new Error(err.detail || "Failed to delete comment");
  }
  invalidateMessageDetailCache(idOrMsgId);
  return res.json();
}

export async function triggerAiReview(idOrMsgId, commitId = null) {
  const res = await fetch(`${BASE_URL}/api/review/trigger`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message_id: String(idOrMsgId), commit_id: commitId }),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to trigger AI review" }));
    throw new Error(err.detail || "Failed to trigger AI review");
  }
  return res.json();
}

export async function fetchAiReviewStatus() {
  const res = await fetch(`${BASE_URL}/api/review/status`);
  if (!res.ok) throw new Error("Failed to fetch AI review status");
  return res.json();
}

export async function cancelAiReview(idOrMsgId = null) {
  const payload = idOrMsgId != null ? { message_id: String(idOrMsgId) } : {};
  const res = await fetch(`${BASE_URL}/api/review/cancel`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to cancel AI review" }));
    throw new Error(err.detail || "Failed to cancel AI review");
  }
  return res.json();
}

export async function fetchKnownAddresses(query = "", limit = 20) {
  const params = new URLSearchParams();
  if (query) params.append("query", query);
  if (limit) params.append("limit", limit);
  const qs = params.toString() ? `?${params.toString()}` : "";
  const res = await fetch(`${BASE_URL}/api/addresses${qs}`);
  if (!res.ok) throw new Error("Failed to fetch addresses");
  return res.json();
}

export async function collectKnownAddresses() {
  const res = await fetch(`${BASE_URL}/api/addresses/collect`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
  });
  if (!res.ok) throw new Error("Failed to collect addresses");
  return res.json();
}

export async function downloadAttachment(messageId, index, filename) {
  const url = `${BASE_URL}/api/messages/${messageId}/attachments/${index}`;
  const res = await fetch(url);
  if (!res.ok) {
    const err = await res.json().catch(() => ({ detail: "Failed to download attachment" }));
    throw new Error(err.detail || `Failed to download attachment (HTTP ${res.status})`);
  }
  const blob = await res.blob();
  const blobUrl = window.URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.style.display = "none";
  a.href = blobUrl;
  a.download = filename || `attachment-${index}`;
  document.body.appendChild(a);
  a.click();
  setTimeout(() => {
    window.URL.revokeObjectURL(blobUrl);
    document.body.removeChild(a);
  }, 100);
}



