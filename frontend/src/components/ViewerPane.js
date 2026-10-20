/**
 * ViewerPane: Bottom-right pane displaying email headers, b4 warning badge, action toolbar,
 * and the patch diff viewer or plain-text body.
 */

import { applyPatchSeries, cancelAiReview, delegatePatchworkPatchToMe, deleteMessage, fetchAiReviewStatus, fetchFolders, fetchMessageDetail, fetchMessages, fetchPatchworkInfo, fetchRawHeaders, fetchTags, getCachedMessageDetail, importB4Thread, markMessageRead, openEditor, setMessageTag, triggerAiReview, updatePatchworkPatch, fetchMessageMboxPath } from "../api.js";
import { store } from "../store.js";
import { getTags } from "../tags.js";
import { renderSideDiff } from "./SideDiff.js";
import { openReplyForMessage, openForwardForMessage, openEditForMessage, openNewMessage } from "./InternalEditor.js";
import { openApplyPatchDialog } from "./ApplyPatchDialog.js";
import { openReviewResultDialog } from "./ReviewResultDialog.js";
import { linkifyUrls } from "../linkify.js";
import {
  FOLDER_B4_IMPORTS,
  FOLDER_AI_REVIEWS,
  EVENT_FOLDER_SELECTED,
  EVENT_MESSAGES_UPDATED,
  EVENT_MESSAGE_SELECTED,
  EVENT_MESSAGE_DETAIL_LOADED,
  EVENT_NOTES_UPDATED,
  EVENT_AI_REVIEW_STATE_CHANGED,
  EVENT_MESSAGE_TAG_UPDATED,
} from "../constants.js";

export function renderViewerPane(container) {
  let headerMode = localStorage.getItem("undine_header_mode") || "abbreviated";
  if (!["abbreviated", "full", "from_only"].includes(headerMode)) {
    headerMode = "abbreviated";
  }
  let showPatchwork = false;
  let currentDetail = null;
  let reviewPollTimer = null;
  let livePollTimer = null;
  let pollingTargetId = null;

  function stopReviewPolling() {
    if (reviewPollTimer) {
      clearInterval(reviewPollTimer);
      reviewPollTimer = null;
    }
    if (livePollTimer) {
      clearInterval(livePollTimer);
      livePollTimer = null;
    }
    pollingTargetId = null;
  }

  function startReviewPolling(targetMsg) {
    if (!targetMsg) return;
    const targetDbId = targetMsg.id;
    const targetMsgId = (targetMsg.message_id || "").replace(/^<|>$/g, "").trim();
    const key = String(targetDbId || targetMsgId);

    if (reviewPollTimer && pollingTargetId === key) {
      return;
    }
    stopReviewPolling();
    pollingTargetId = key;

    reviewPollTimer = setInterval(async () => {
      // Check if message pane has opened another message
      if (!currentDetail || !currentDetail.message) {
        stopReviewPolling();
        return;
      }
      const cur = currentDetail.message;
      const curDbId = cur.id;
      const curMsgId = (cur.message_id || "").replace(/^<|>$/g, "").trim();
      const reviewedMsg = currentDetail.reviewed_message || cur.reviewed_message;
      const reviewedDbId = reviewedMsg?.id;
      const reviewedMsgId = (reviewedMsg?.message_id || "").replace(/^<|>$/g, "").trim();

      const isSameMessage =
        curDbId === targetDbId ||
        (targetMsgId && curMsgId === targetMsgId) ||
        (targetDbId && reviewedDbId === targetDbId) ||
        (targetMsgId && reviewedMsgId === targetMsgId);

      if (!isSameMessage) {
        stopReviewPolling();
        return;
      }

      try {
        const status = await fetchAiReviewStatus();
        if (!currentDetail || !currentDetail.message) {
          stopReviewPolling();
          return;
        }
        const nowCur = currentDetail.message;
        const nowCurDbId = nowCur.id;
        const nowCurMsgId = (nowCur.message_id || "").replace(/^<|>$/g, "").trim();
        const nowReviewed = currentDetail.reviewed_message || nowCur.reviewed_message;
        const nowReviewedDbId = nowReviewed?.id;
        const nowReviewedMsgId = (nowReviewed?.message_id || "").replace(/^<|>$/g, "").trim();

        const stillSame =
          nowCurDbId === targetDbId ||
          (targetMsgId && nowCurMsgId === targetMsgId) ||
          (targetDbId && nowReviewedDbId === targetDbId) ||
          (targetMsgId && nowReviewedMsgId === targetMsgId);

        if (!stillSame) {
          stopReviewPolling();
          return;
        }

        const hasRemaining = status.is_running || (status.queue_length && status.queue_length > 0);
        if (store.setAiReviewState) {
          store.setAiReviewState({
            isRunning: status.is_running,
            messageId: status.active_message_id,
            activeReviewDbId: status.active_review_db_id || null,
            status: status.status,
            error: status.error || null,
            output: status.output || null,
            queue: status.queue || [],
          });
        }

        const currentTargetStatus = store.getAiReviewStatusForTarget
          ? (store.getAiReviewStatusForTarget(targetDbId) || store.getAiReviewStatusForTarget(targetMsgId))
          : null;

        if (!currentTargetStatus || (!status.is_running && (!status.queue_length || status.queue_length === 0))) {
          stopReviewPolling();
          if (status.status === "error") {
            showToast(`AI review failed: ${status.error || "Unknown error"}`, "error");
          } else if (status.status === "cancelled") {
            showToast("AI review was cancelled.", "info");
          } else {
            showToast("AI review completed! Report saved to AI-reviews mailbox.", "success");
          }

          const updatedFolders = await fetchFolders();
          store.setFolders(updatedFolders);
          if (store.getState().selectedFolderId) {
            const updatedMsgs = await fetchMessages(store.getState().selectedFolderId);
            store.setMessages(updatedMsgs);
          }

          if (currentDetail && currentDetail.message && currentDetail.message.id === targetDbId) {
            const updated = await fetchMessageDetail(targetDbId, { bypassCache: true });
            if (currentDetail?.message?.id === targetDbId) {
              store.setActiveMessageDetail(updated);
            }
          }
        }
      } catch (e) {
        console.error("Error polling review status in ViewerPane:", e);
      }
    }, 1500);
  }

  async function loadDetail(msg) {
    stopReviewPolling();
    if (!msg) {
      currentDetail = null;
      const isDraft = isDraftFolder(store.getState().selectedFolderId);
      if (isDraft) {
        container.innerHTML = `
          <div class="empty-viewer">
            <div class="empty-viewer-text">Drafts folder</div>
            <div style="margin-top: 14px;">
              <button type="button" class="action-btn primary" id="btn-viewer-new-message" style="padding: 8px 18px; font-size: 13px;">
                ✏️ Edit New Message
              </button>
            </div>
          </div>
        `;
        const newMsgBtn = container.querySelector("#btn-viewer-new-message");
        if (newMsgBtn) {
          newMsgBtn.addEventListener("click", () => {
            triggerNewMessage();
          });
        }
      } else {
        container.innerHTML = `<div class="empty-viewer"><div class="empty-viewer-text">Select an email to read</div></div>`;
      }
      return;
    }
    const requestedId = msg.id;
    showPatchwork = false;

    // Check if message is already in cache to render immediately and avoid redundant requests
    const cached = getCachedMessageDetail(requestedId);
    if (cached) {
      store.setActiveMessageDetail(cached);

      // Auto mark as read if needed
      if (!msg.is_read) {
        msg.is_read = 1;
        if (cached.message) cached.message.is_read = 1;
        store.updateMessageRead(requestedId, true);
        markMessageRead(requestedId, true)
          .then((res) => {
            if (res?.file_path) {
              if (cached.message) cached.message.file_path = res.file_path;
              if (currentDetail?.message && currentDetail.message.id === requestedId) {
                currentDetail.message.file_path = res.file_path;
                const btn = container.querySelector("#btn-copy-mbox-path");
                if (btn) btn.title = `Copy mbox path to clipboard: ${res.file_path}`;
              }
            }
          })
          .catch(() => {});
      }
      return;
    }

    container.innerHTML = `<div style="padding: 24px; color: var(--text-muted);">Loading email ${escapeHtml(msg.subject)}...</div>`;
    try {
      const detail = await fetchMessageDetail(requestedId);
      // Ensure the user hasn't selected another message while this request was in flight
      if (store.getState().selectedMessageId !== requestedId) {
        return;
      }
      store.setActiveMessageDetail(detail);

      // Auto mark as read
      if (!msg.is_read) {
        msg.is_read = 1;
        if (detail.message) detail.message.is_read = 1;
        store.updateMessageRead(requestedId, true);
        markMessageRead(requestedId, true)
          .then((res) => {
            if (res?.file_path) {
              if (detail.message) detail.message.file_path = res.file_path;
              if (currentDetail?.message && currentDetail.message.id === requestedId) {
                currentDetail.message.file_path = res.file_path;
                const btn = container.querySelector("#btn-copy-mbox-path");
                if (btn) btn.title = `Copy mbox path to clipboard: ${res.file_path}`;
              }
            }
          })
          .catch(() => {});
      }
    } catch (err) {
      if (store.getState().selectedMessageId === requestedId) {
        container.innerHTML = `<div style="padding: 24px; color: var(--accent-red);">Failed to load email: ${escapeHtml(err.message)}</div>`;
      }
    }
  }

  function render(detail) {
    if (!detail || !detail.message) {
      container.innerHTML = `<div style="padding: 24px; color: var(--text-muted);">Select an email from the summary list above.</div>`;
      return;
    }

    currentDetail = detail;
    const m = detail.message;
    const isAiReview = isAiReviewMessage(m);
    const missingParent = detail.missing_parent;
    const parentMsgid = detail.parent_msgid;
    const cleanMsgId = (m.message_id || "").replace(/^<|>$/g, "").trim();
    const encodedMsgId = encodeURIComponent(cleanMsgId);
    const loreUrl = detail.lore_url || (cleanMsgId ? `https://lore.kernel.org/r/${encodedMsgId}` : "");
    const sashikoUrl = cleanMsgId ? `https://sashiko.dev/#/patchset/${encodedMsgId}` : "";
    const commitId = m.review_commit_id || m["review-commit-id"];
    const mboxPath = m.file_path || "";

    function getHeaderToggleText(mode) {
      if (mode === "abbreviated") return "Headers: Abbrev";
      if (mode === "full") return "Headers: Full";
      return "Headers: From only";
    }

    const toFormatted = formatRecipientList(m.to_addr, headerMode === "full");
    const ccFormatted = formatRecipientList(m.cc_addr, headerMode === "full");

    let html = "";
    if (isAiReview) {
      html = `
      <!-- Header Box -->
      <div class="viewer-header-box">
        <div class="header-top-row">
          <div class="header-subject">
            ${linkifyUrls(m.subject)}
          </div>
        </div>
        <div class="header-meta">
          <span class="meta-label">From:</span>
          <span class="meta-val">${escapeHtml(m.from_name || "AI review")}</span>

          <span class="meta-label">Date:</span>
          <span class="meta-val">${escapeHtml(m.date || "")}</span>
        </div>
      </div>
      `;
    } else {
      html = `
      <!-- Header Box -->
      <div class="viewer-header-box">
        <div class="header-top-row">
          <div class="header-subject">
            ${linkifyUrls(m.subject)}
            ${m.is_replied ? `<span class="viewer-replied-badge" title="You replied to this message">↩ Replied</span>` : ""}
          </div>
          <div class="header-actions">
            <div class="header-tag-menu-wrapper" style="position: relative; display: inline-block;">
              <button type="button" class="header-action-btn" id="btn-change-tag" title="Set or change color tag (Keys 1-9 to set, 0 to clear)">
                🏷️ Tag${m.tag_name ? `: ${escapeHtml(m.tag_name)}` : ""} ▾
              </button>
              <div class="header-tag-dropdown" id="header-tag-dropdown" style="display: none;"></div>
            </div>
            <div class="header-menu-wrapper" style="position: relative; display: inline-block;">
              <button type="button" class="header-toggle-btn" id="btn-toggle-headers" title="Header mode: ${headerMode} (click to open menu)">
                ${getHeaderToggleText(headerMode)} ▾
              </button>
              <div class="header-dropdown" id="header-dropdown" style="display: none;">
                <button type="button" class="header-dropdown-item ${headerMode === 'abbreviated' ? 'active' : ''}" data-header-mode="abbreviated">
                  <span>Abbreviated header</span>
                  ${headerMode === 'abbreviated' ? '<span style="margin-left: auto; color: var(--accent-blue);">✓</span>' : ''}
                </button>
                <button type="button" class="header-dropdown-item ${headerMode === 'full' ? 'active' : ''}" data-header-mode="full">
                  <span>Full header</span>
                  ${headerMode === 'full' ? '<span style="margin-left: auto; color: var(--accent-blue);">✓</span>' : ''}
                </button>
                <button type="button" class="header-dropdown-item ${headerMode === 'from_only' ? 'active' : ''}" data-header-mode="from_only">
                  <span>From only</span>
                  ${headerMode === 'from_only' ? '<span style="margin-left: auto; color: var(--accent-blue);">✓</span>' : ''}
                </button>
                <div style="height: 1px; background-color: var(--border-color); margin: 3px 0;"></div>
                <button type="button" class="header-dropdown-item" id="btn-menu-raw-header">
                  <span>📜 Show raw-header</span>
                </button>
              </div>
            </div>
          </div>
        </div>
        <div class="header-meta">
          <span class="meta-label">From:</span>
          <span class="meta-val">${escapeHtml(m.from_name ? `${m.from_name} <${m.from_addr}>` : m.from_addr)}</span>

          ${headerMode !== "from_only" ? `
            <span class="meta-label">To:</span>
            <span class="meta-val">${toFormatted.html}</span>

            ${m.cc_addr ? `<span class="meta-label">Cc:</span><span class="meta-val">${ccFormatted.html}</span>` : ""}

            <span class="meta-label">Date:</span>
            <span class="meta-val">${escapeHtml(m.date || "")}</span>

            <span class="meta-label">Msg-ID:</span>
            <span class="meta-val msgid-val">
              ${cleanMsgId ? `
                <a href="javascript:void(0)" class="msgid-link" id="link-copy-msgid" role="button" title="Click to copy Message ID: &lt;${escapeAttr(cleanMsgId)}&gt;">&lt;${escapeHtml(cleanMsgId)}&gt;</a>
                <button type="button" class="btn-copy-msgid" id="btn-copy-msgid" title="Copy Message ID to clipboard">📋</button>
              ` : `<span style="color: var(--text-muted);">-</span>`}
              ${mboxPath ? `
                <button type="button" class="meta-link-btn btn-copy-mbox-path" id="btn-copy-mbox-path" title="Copy mbox path to clipboard: ${escapeAttr(mboxPath)}">Copy mbox path</button>
              ` : ""}
              ${cleanMsgId ? `
                <a href="${escapeHtml(loreUrl)}" target="_blank" rel="noopener noreferrer" class="meta-link-btn" title="Open email archive on lore.kernel.org">Archive ↗</a>
                <a href="${escapeHtml(sashikoUrl)}" target="_blank" rel="noopener noreferrer" class="meta-link-btn" title="Open patchset on Sashiko">Sashiko ↗</a>
              ` : ""}
            </span>

            ${commitId ? `
              <span class="meta-label">commit:</span>
              <span class="meta-val commit-val">
                <code class="commit-hash" title="${escapeAttr(commitId)}">${escapeHtml(commitId)}</code>
                <button type="button" class="btn-copy-commit" id="btn-copy-commit" title="Copy commit ID to clipboard">📋</button>
              </span>
            ` : ""}
          ` : ""}

          ${headerMode === "full" && m.in_reply_to ? `<span class="meta-label">In-Reply:</span><span class="meta-val">&lt;${escapeHtml(m.in_reply_to)}&gt;</span>` : ""}
          ${headerMode === "full" && m.references_header ? `<span class="meta-label">Refs:</span><span class="meta-val">${escapeHtml(m.references_header)}</span>` : ""}
        </div>
        <!-- Patchwork Metadata & Delegate Status Row -->
        <div class="patchwork-bar" id="patchwork-bar" style="display: none;"></div>
      </div>
      `;
    }

    // Missing Parent or Parent in b4/other mailbox banner
    const parentInfo = detail.parent_info;
    if (!isAiReview && parentInfo && !parentInfo.is_same_folder) {
      const folderName = parentInfo.folder_name || (parentInfo.is_in_b4 ? FOLDER_B4_IMPORTS : "b4 mailbox");
      const isB4 = Boolean(parentInfo.is_in_b4 || folderName === FOLDER_B4_IMPORTS);
      html += `
        <div class="parent-thread-banner ${isB4 ? "b4-imported" : "other-folder"}">
          <span>📬 Parent thread is available in <strong>${escapeHtml(folderName)}</strong>: <code>&lt;${escapeHtml(parentMsgid || parentInfo.message_id)}&gt;</code></span>
          <button type="button" class="b4-jump-btn" id="btn-jump-b4" title="Open parent thread in ${escapeHtml(folderName)} mailbox">
            📂 Open in ${escapeHtml(folderName)} →
          </button>
        </div>
      `;
    } else if (!isAiReview && missingParent && parentMsgid) {
      html += `
        <div class="missing-parent-banner">
          <span>⚠️ Missing parent thread context: <code>&lt;${escapeHtml(parentMsgid)}&gt;</code></span>
          <button type="button" class="b4-btn" id="btn-hydrate-b4">⚡ Hydrate Thread via b4</button>
        </div>
      `;
    }

    // Determine patch action button (AI review or Apply git am)
    const isReviewActive = store.isAiReviewActive ? store.isAiReviewActive() : false;
    const reviewTargetStatus = store.getAiReviewStatusForTarget
      ? (store.getAiReviewStatusForTarget(m.id) || store.getAiReviewStatusForTarget(m.message_id))
      : (store.isAiReviewTarget && (store.isAiReviewTarget(m.id) || store.isAiReviewTarget(m.message_id)) ? "in-progress" : null);

    let patchActionButton = "";
    if (commitId) {
      if (reviewTargetStatus === "in-progress" || reviewTargetStatus === "running") {
        patchActionButton = `
          <button type="button" class="action-btn" id="btn-ai-review" disabled title="AI review is running on this patch">⏳ AI Review running...</button>
          <button type="button" class="action-btn danger btn-cancel-ai-review" id="btn-cancel-ai-review" title="Cancel running AI review">🛑 Cancel Review</button>
        `;
      } else if (reviewTargetStatus === "queued") {
        patchActionButton = `
          <button type="button" class="action-btn" id="btn-ai-review" disabled title="AI review is queued for this patch">⏳ Queued...</button>
          <button type="button" class="action-btn danger btn-cancel-ai-review" id="btn-cancel-ai-review" title="Cancel queued AI review">🛑 Cancel Review</button>
        `;
      } else if (isReviewActive) {
        patchActionButton = `<button type="button" class="action-btn" id="btn-ai-review" title="Queue background AI review with agy --print">🤖 Queue AI review</button>`;
      } else {
        patchActionButton = `<button type="button" class="action-btn" id="btn-ai-review" title="Trigger background AI review with agy --print">🤖 AI review</button>`;
      }
    } else if (m.is_patch && !isDraftMessage(m)) {
      patchActionButton = `<button type="button" class="action-btn" id="btn-apply-git">📥 Apply with git am</button>`;
    }

    const draftNotes = store.getNotesList ? store.getNotesList().filter((n) => !n.state || n.state === "draft") : [];
    const draftCount = draftNotes.length;
    const discardDraftsButton = draftCount > 0
      ? `<button type="button" class="action-btn danger" id="btn-discard-drafts" title="Discard all draft comments on this patch">🗑️ Discard Drafts (${draftCount})</button>`
      : "";

    let reviewBannerHtml = "";
    if (reviewTargetStatus === "in-progress" || reviewTargetStatus === "running" || reviewTargetStatus === "queued") {
      const isQueued = reviewTargetStatus === "queued";
      const bannerText = isQueued
        ? "AI Review queued: waiting in background queue..."
        : "AI Review in progress: analyzing patch diff with <code>agy --print</code>...";
      reviewBannerHtml = `
        <div class="summary-sync-banner ai-review-banner" id="ai-review-banner">
          <div class="sync-banner-content">
            <div class="sync-banner-text">
              <span class="spinner" style="font-size: 11px; margin-right: 6px;">🔄</span>
              <span>${bannerText}</span>
            </div>
            <div class="sync-progress-bar-bg" style="margin-top: 4px;">
              <div class="sync-progress-bar-fill review-bar-pulse" style="width: 100%;"></div>
            </div>
          </div>
          <button type="button" class="action-btn danger btn-cancel-ai-review" id="btn-cancel-ai-review-banner" style="margin-left: 12px; white-space: nowrap;" title="Cancel this AI review">
            🛑 Cancel Review
          </button>
        </div>
      `;
    }

    const isDraft = isDraftMessage(m);
    let composeActionsHtml = "";
    if (isDraft) {
      composeActionsHtml = `
        <button type="button" class="action-btn primary" id="btn-edit-draft" title="Edit draft (e)">
          ✏️ Edit
        </button>
        <button type="button" class="action-btn" id="btn-toolbar-new-message" title="Edit a new message (c)">
          ➕ New Message
        </button>
      `;
    } else {
      composeActionsHtml = `
        <button type="button" class="action-btn primary" id="btn-reply" title="Compose reply (r)">
          📝 Reply
        </button>
        <button type="button" class="action-btn" id="btn-forward" title="Forward email (Shift+F)">
          ➡️ Forward
        </button>
      `;
    }

    // Action Toolbar
    let toolbarHtml = "";
    if (isAiReview) {
      const reviewedMsg = detail.reviewed_message || m.reviewed_message;
      let openReviewedBtn = "";
      if (reviewedMsg && (reviewedMsg.folder_id != null || reviewedMsg.id != null)) {
        const folderLabel = reviewedMsg.folder_name ? ` in ${escapeHtml(reviewedMsg.folder_name)}` : "";
        openReviewedBtn = `
          <button type="button" class="action-btn" id="btn-open-reviewed-message" title="Open reviewed message${folderLabel} (o)">
            📬 Open reviewed message
          </button>
        `;
      }
      const bodyTrim = (m.body_plain || "").trim();
      const isPending = bodyTrim === "Queued" || bodyTrim === "In-progress" ||
        (store.getAiReviewStatusForTarget && (
          store.getAiReviewStatusForTarget(m.id) ||
          store.getAiReviewStatusForTarget(m.message_id) ||
          (reviewedMsg && (store.getAiReviewStatusForTarget(reviewedMsg.id) || store.getAiReviewStatusForTarget(reviewedMsg.message_id)))
        ));
      let cancelBtn = "";
      if (isPending) {
        cancelBtn = `
          <button type="button" class="action-btn danger btn-cancel-ai-review" id="btn-cancel-ai-review" title="Cancel this AI review">
            🛑 Cancel Review
          </button>
        `;
      }
      toolbarHtml = `
        ${openReviewedBtn}
        ${cancelBtn}
        <button type="button" class="action-btn text-danger action-btn-trash" id="btn-trash-message" title="Move to Trash / Delete AI review">
          🗑️ Trash
        </button>
      `;
    } else {
      toolbarHtml = `
        ${composeActionsHtml}
        ${!isDraft ? `
          <button type="button" class="action-btn" id="btn-import-b4-toolbar" title="Import parent thread / series from lore.kernel.org via b4">
            ⚡ Import series from b4
          </button>
        ` : ""}
        ${patchActionButton}
        ${discardDraftsButton}
        ${!isDraft ? `
          <button type="button" class="action-btn" id="btn-toggle-patchwork" title="Toggle Patchwork line in header">
            🧩 Toggle patchwork
          </button>
        ` : ""}
        <button type="button" class="action-btn text-danger action-btn-trash" id="btn-trash-message" title="Move to Trash / Delete email">
          🗑️ Trash
        </button>
      `;
    }

    html += `
      <div class="action-toolbar">
        ${toolbarHtml}
      </div>

      ${reviewBannerHtml}

      <!-- Main Body Container -->
      <div class="viewer-body-container" id="viewer-diff-body"></div>
    `;

    container.innerHTML = html;

    // Mount Patch or Plain Text Body
    const bodyContainer = container.querySelector("#viewer-diff-body");
    if (detail.parsed_diff && detail.parsed_diff.is_patch) {
      renderSideDiff(bodyContainer, detail.parsed_diff);
    } else {
      bodyContainer.innerHTML = `<pre class="plaintext-email">${linkifyUrls(m.body_plain || "")}</pre>`;
    }

    // Event: Open reviewed message
    const openReviewedBtn = container.querySelector("#btn-open-reviewed-message");
    const reviewedMsg = detail.reviewed_message || m.reviewed_message;
    if (openReviewedBtn && reviewedMsg) {
      openReviewedBtn.addEventListener("click", () => {
        triggerOpenReviewed(reviewedMsg);
      });
    }

    // Event: Jump to parent in b4/other folder
    const jumpBtn = container.querySelector("#btn-jump-b4");
    if (jumpBtn && parentInfo) {
      jumpBtn.addEventListener("click", () => {
        const targetFolderName = parentInfo.folder_name || FOLDER_B4_IMPORTS;
        showToast(`Opening ${targetFolderName}...`, "info");
        store.selectFolder(parentInfo.folder_id, parentInfo.id || parentInfo.message_id);
      });
    }

    // Event: Hydrate via b4
    const hydrateBtn = container.querySelector("#btn-hydrate-b4");
    if (hydrateBtn) {
      hydrateBtn.addEventListener("click", async () => {
        hydrateBtn.disabled = true;
        hydrateBtn.innerText = "⏳ Hydrating...";
        try {
          const res = await importB4Thread(parentMsgid);
          showToast(`Imported ${res.imported_count} messages from lore.kernel.org into b4-imports!`, "success");
          // Refresh folders and current message
          const folders = await fetchFolders();
          store.setFolders(folders);
          const updatedDetail = await fetchMessageDetail(m.id, { bypassCache: true });
          store.setActiveMessageDetail(updatedDetail);
        } catch (err) {
          showToast(err.message, "error");
          hydrateBtn.disabled = false;
          hydrateBtn.innerText = "⚡ Hydrate Thread via b4";
        }
      });
    }

    // Event: Apply git am (opens modal to configure branch & base commit)
    const applyBtn = container.querySelector("#btn-apply-git");
    if (applyBtn) {
      applyBtn.addEventListener("click", () => {
        openApplyPatchDialog(m, async (applyRes) => {
          try {
            const updated = await fetchMessageDetail(m.id, { bypassCache: true });
            store.setActiveMessageDetail(updated);
          } catch (e) {
            console.error("Failed to reload message detail after apply:", e);
          }
        });
      });
    }

    // Event: Trigger AI Review
    const aiReviewBtn = container.querySelector("#btn-ai-review");
    if (aiReviewBtn && !aiReviewBtn.disabled) {
      aiReviewBtn.addEventListener("click", async () => {
        const targetId = m.message_id || m.id;
        if (store.getAiReviewStatusForTarget && store.getAiReviewStatusForTarget(targetId)) return;
        try {
          aiReviewBtn.disabled = true;
          const res = await triggerAiReview(targetId, commitId);
          const isQueued = res.status === "queued";
          showToast(isQueued ? "AI review queued in background..." : "AI review started in background...", "info");

          // Refresh status and store
          try {
            const status = await fetchAiReviewStatus();
            if (store.setAiReviewState) {
              store.setAiReviewState({
                isRunning: status.is_running,
                messageId: status.active_message_id,
                status: status.status,
                queue: status.queue || [],
              });
            }
          } catch (_) {}

          // Refresh folders and messages so AI-reviews folder updates immediately
          const folders = await fetchFolders();
          store.setFolders(folders);
          if (store.getState().selectedFolderId) {
            const msgs = await fetchMessages(store.getState().selectedFolderId);
            store.setMessages(msgs);
          }

          startReviewPolling(m);
        } catch (err) {
          if (store.setAiReviewState) {
            store.setAiReviewState({
              isRunning: false,
              messageId: null,
              status: "error",
              error: err.message,
            });
          }
          showToast(err.message, "error");
          aiReviewBtn.disabled = false;
        }
      });
    }

    // Event: Cancel AI Review
    const cancelAiReviewBtns = container.querySelectorAll(".btn-cancel-ai-review, #btn-cancel-ai-review, #btn-cancel-ai-review-banner");
    cancelAiReviewBtns.forEach((btn) => {
      btn.addEventListener("click", async () => {
        btn.disabled = true;
        stopReviewPolling();
        try {
          const reviewedMsg = detail.reviewed_message || m.reviewed_message;
          const targetId = m.message_id || m.id || (reviewedMsg ? (reviewedMsg.message_id || reviewedMsg.id) : null);
          await cancelAiReview(targetId);
          showToast("AI review cancelled", "info");

          try {
            const status = await fetchAiReviewStatus();
            if (store.setAiReviewState) {
              store.setAiReviewState({
                isRunning: status.is_running,
                messageId: status.active_message_id,
                activeReviewDbId: status.active_review_db_id || null,
                status: status.status,
                queue: status.queue || [],
              });
            }
          } catch (_) {}

          const folders = await fetchFolders();
          store.setFolders(folders);
          if (store.getState().selectedFolderId) {
            const msgs = await fetchMessages(store.getState().selectedFolderId);
            store.setMessages(msgs);
          }
          const updated = await fetchMessageDetail(m.id, { bypassCache: true });
          store.setActiveMessageDetail(updated);
        } catch (err) {
          showToast(`Failed to cancel review: ${err.message}`, "error");
          btn.disabled = false;
        }
      });
    });

    // Auto-poll if viewing an AI-reviews email currently Queued or In-progress
    if (isAiReview) {
      const bodyTrim = (m.body_plain || "").trim();
      if (bodyTrim === "Queued" || bodyTrim === "In-progress") {
        livePollTimer = setInterval(async () => {
          if (!currentDetail || currentDetail.message?.id !== m.id) {
            if (livePollTimer) {
              clearInterval(livePollTimer);
              livePollTimer = null;
            }
            return;
          }
          try {
            const updated = await fetchMessageDetail(m.id, { bypassCache: true });
            const newBody = (updated.message?.body_plain || "").trim();
            if (newBody !== bodyTrim) {
              if (livePollTimer) {
                clearInterval(livePollTimer);
                livePollTimer = null;
              }
              store.setActiveMessageDetail(updated);
            }
          } catch (_) {
            if (livePollTimer) {
              clearInterval(livePollTimer);
              livePollTimer = null;
            }
          }
        }, 2000);
      }
    }

    // Event: Discard Drafts
    const discardDraftsBtn = container.querySelector("#btn-discard-drafts");
    if (discardDraftsBtn) {
      discardDraftsBtn.addEventListener("click", async () => {
        discardDraftsBtn.disabled = true;
        try {
          if (store.discardDraftNotes) {
            await store.discardDraftNotes(m.id || m.message_id);
          }
          showToast("Draft comments discarded", "info");
          const updated = await fetchMessageDetail(m.id, { bypassCache: true });
          store.setActiveMessageDetail(updated);
        } catch (err) {
          showToast(`Failed to discard drafts: ${err.message}`, "error");
          discardDraftsBtn.disabled = false;
        }
      });
    }

    // Event: Copy Message ID to clipboard
    const copyMsgIdBtn = container.querySelector("#btn-copy-msgid");
    const copyMsgIdLink = container.querySelector("#link-copy-msgid");
    const handleCopyMsgId = async (e) => {
      if (e) {
        e.preventDefault();
        e.stopPropagation();
      }
      if (!cleanMsgId) return;
      try {
        await navigator.clipboard.writeText(cleanMsgId);
        showToast(`Copied Message ID to clipboard: ${cleanMsgId}`, "success");
      } catch {
        const ta = document.createElement("textarea");
        ta.value = cleanMsgId;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        showToast(`Copied Message ID: ${cleanMsgId}`, "success");
      }
    };
    if (copyMsgIdBtn) {
      copyMsgIdBtn.addEventListener("click", handleCopyMsgId);
    }
    if (copyMsgIdLink) {
      copyMsgIdLink.addEventListener("click", handleCopyMsgId);
    }

    // Event: Copy mbox path to clipboard
    const copyMboxPathBtn = container.querySelector("#btn-copy-mbox-path");
    const handleCopyMboxPath = async (e) => {
      if (e) {
        e.preventDefault();
        e.stopPropagation();
      }
      const msgObj = currentDetail?.message || m;
      const msgId = msgObj?.id || msgObj?.message_id;
      let targetPath = msgObj?.file_path || mboxPath;

      // Always query server to dynamically verify and resolve the live on-disk path
      // (ensuring transitions like new/ -> cur/ or flag changes are accurately resolved).
      if (msgId) {
        try {
          const res = await fetchMessageMboxPath(msgId);
          if (res?.file_path) {
            targetPath = res.file_path;
            if (msgObj) msgObj.file_path = targetPath;
            if (copyMboxPathBtn) {
              copyMboxPathBtn.title = `Copy mbox path to clipboard: ${targetPath}`;
            }
          }
        } catch (err) {
          console.warn("Could not dynamically resolve mbox path, using last known:", err);
        }
      }

      if (!targetPath) {
        showToast("No on-disk message file path found.", "error");
        return;
      }

      try {
        await navigator.clipboard.writeText(targetPath);
        showToast(`Copied mbox path to clipboard: ${targetPath}`, "success");
      } catch {
        const ta = document.createElement("textarea");
        ta.value = targetPath;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        showToast(`Copied mbox path: ${targetPath}`, "success");
      }
    };
    if (copyMboxPathBtn) {
      copyMboxPathBtn.addEventListener("click", handleCopyMboxPath);
    }

    // Event: Copy commit hash to clipboard
    const copyCommitBtn = container.querySelector("#btn-copy-commit");
    const commitHashEl = container.querySelector(".commit-hash");
    const handleCopyCommit = async (e) => {
      e.stopPropagation();
      if (!commitId) return;
      try {
        await navigator.clipboard.writeText(commitId);
        showToast(`Copied commit ID to clipboard: ${commitId.slice(0, 12)}`, "success");
      } catch {
        const ta = document.createElement("textarea");
        ta.value = commitId;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        showToast(`Copied commit ID: ${commitId.slice(0, 12)}`, "success");
      }
    };
    if (copyCommitBtn) {
      copyCommitBtn.addEventListener("click", handleCopyCommit);
    }
    if (commitHashEl) {
      commitHashEl.addEventListener("click", handleCopyCommit);
      commitHashEl.style.cursor = "pointer";
    }

    // Event: Edit Draft
    const editDraftBtn = container.querySelector("#btn-edit-draft");
    if (editDraftBtn) {
      editDraftBtn.addEventListener("click", () => {
        triggerEdit(m);
      });
    }

    // Event: Toolbar New Message
    const toolbarNewMsgBtn = container.querySelector("#btn-toolbar-new-message");
    if (toolbarNewMsgBtn) {
      toolbarNewMsgBtn.addEventListener("click", () => {
        triggerNewMessage();
      });
    }

    // Event: Reply
    const replyBtn = container.querySelector("#btn-reply");
    if (replyBtn) {
      replyBtn.addEventListener("click", () => {
        triggerReply(m);
      });
    }

    // Event: Forward
    const forwardBtn = container.querySelector("#btn-forward");
    if (forwardBtn) {
      forwardBtn.addEventListener("click", () => {
        triggerForward(m);
      });
    }

    // Event: Import series from b4 (in toolbar)
    const importB4ToolbarBtn = container.querySelector("#btn-import-b4-toolbar");
    if (importB4ToolbarBtn) {
      importB4ToolbarBtn.addEventListener("click", async () => {
        if (!m.message_id) {
          showToast("No Message-ID available", "error");
          return;
        }
        importB4ToolbarBtn.disabled = true;
        importB4ToolbarBtn.innerText = "⏳ Importing series...";
        try {
          const res = await importB4Thread(m.message_id);
          showToast(`Imported ${res.imported_count} messages via b4 into b4-imports!`, "success");
          const folders = await fetchFolders();
          store.setFolders(folders);
          const updatedDetail = await fetchMessageDetail(m.id, { bypassCache: true });
          store.setActiveMessageDetail(updatedDetail);
        } catch (err) {
          showToast(`b4 import failed: ${err.message}`, "error");
        } finally {
          importB4ToolbarBtn.disabled = false;
          importB4ToolbarBtn.innerText = "⚡ Import series from b4";
        }
      });
    }

    // Event: Change message tag
    const tagBtn = container.querySelector("#btn-change-tag");
    const tagDropdown = container.querySelector("#header-tag-dropdown");
    if (tagBtn && tagDropdown) {
      tagBtn.addEventListener("click", async (e) => {
        e.stopPropagation();
        const isOpen = tagDropdown.style.display !== "none";
        if (isOpen) {
          tagDropdown.style.display = "none";
          return;
        }
        tagDropdown.style.display = "block";
        tagDropdown.innerHTML = `<div style="padding: 6px 10px; color: var(--text-muted); font-size: 11px;">Loading tags...</div>`;
        try {
          const availableTags = await getTags();
          let itemsHtml = `
            <button type="button" class="header-tag-item" data-tag-key="">
              <span class="tag-color-dot" style="background-color: transparent; border: 1px dashed #64748b;"></span>
              <span>None (Clear)</span>
              <span style="margin-left: auto; color: var(--text-muted); font-size: 10px;">0</span>
            </button>
            <div style="height: 1px; background-color: var(--border-color); margin: 3px 0;"></div>
          `;
          let idx = 1;
          for (const [key, val] of Object.entries(availableTags)) {
            const isCl = key.startsWith("$cl_");
            const num = isCl ? parseInt(key.slice(4), 10) : (key.startsWith("$label") ? parseInt(key.slice(6), 10) : idx++);
            const shortcutHint = (num >= 1 && num <= 9) ? `${num}` : "";
            const isSelected = m.tag_key?.toLowerCase() === key.toLowerCase() ? "✓" : "";
            itemsHtml += `
              <button type="button" class="header-tag-item" data-tag-key="${escapeHtml(key)}">
                <span class="tag-color-dot" style="background-color: ${val.color};"></span>
                <span>${escapeHtml(val.tag)}</span>
                ${shortcutHint ? `<span style="margin-left: auto; color: var(--text-muted); font-size: 10px;">${shortcutHint}</span>` : ""}
                ${isSelected ? `<span style="color: var(--accent-blue); font-weight: bold; margin-left: 4px;">✓</span>` : ""}
              </button>
            `;
          }
          tagDropdown.innerHTML = itemsHtml;
          tagDropdown.querySelectorAll(".header-tag-item").forEach((btn) => {
            btn.addEventListener("click", async (ev) => {
              ev.stopPropagation();
              tagDropdown.style.display = "none";
              const selectedKey = btn.getAttribute("data-tag-key") || null;
              try {
                const res = await setMessageTag(m.id, selectedKey);
                store.updateMessageTag(m.id, res.tag_key, res.tag_name, res.tag_color);
                m.tag_key = res.tag_key;
                m.tag_name = res.tag_name;
                m.tag_color = res.tag_color;
                render(detail);
              } catch (err) {
                showToast(`Failed to update tag: ${err.message}`, "error");
              }
            });
          });
        } catch (err) {
          tagDropdown.innerHTML = `<div style="padding: 6px 10px; color: var(--accent-red); font-size: 11px;">Error loading tags</div>`;
        }
      });
    }

    // Event: Trash / Delete email
    const trashBtn = container.querySelector("#btn-trash-message");
    if (trashBtn) {
      trashBtn.addEventListener("click", async () => {
        const msgId = m.id != null ? m.id : m.message_id;
        if (!confirm(`Are you sure you want to delete "${m.subject || "this message"}"?`)) {
          return;
        }
        trashBtn.disabled = true;
        try {
          await deleteMessage(msgId);
          store.removeMessage(m.id);
          const folders = await fetchFolders();
          store.setFolders(folders);
          showToast("Message deleted.", "info");
        } catch (err) {
          console.error("Failed to delete message:", err);
          showToast(`Failed to delete message: ${err.message}`, "error");
          trashBtn.disabled = false;
        }
      });
    }

    // Event: Toggle Patchwork line in header
    const togglePwBtn = container.querySelector("#btn-toggle-patchwork");
    if (togglePwBtn) {
      togglePwBtn.addEventListener("click", async () => {
        const pwBar = container.querySelector("#patchwork-bar");
        if (!pwBar) return;

        // If patchwork bar is already shown, hide it
        if (showPatchwork) {
          showPatchwork = false;
          pwBar.style.display = "none";
          togglePwBtn.classList.remove("active");
          return;
        }

        // If patchwork data is already loaded in the bar for this message, just show it
        const cleanMsgId = String(m.message_id || "").trim().replace(/^<|>$/g, "");
        const barMsgId = pwBar.getAttribute("data-msgid");
        if (barMsgId && (barMsgId === cleanMsgId || barMsgId === m.message_id) && pwBar.innerHTML.trim() !== "") {
          showPatchwork = true;
          pwBar.style.display = "flex";
          togglePwBtn.classList.add("active");
          return;
        }

        // Otherwise fetch patchwork info on demand
        togglePwBtn.disabled = true;
        const origBtnText = togglePwBtn.innerHTML;
        togglePwBtn.innerHTML = `⏳ Loading patchwork...`;
        try {
          showPatchwork = true;
          const found = await loadPatchworkStatus(m, container, true);
          if (found) {
            pwBar.style.display = "flex";
            togglePwBtn.classList.add("active");
          } else {
            showPatchwork = false;
            pwBar.style.display = "none";
            togglePwBtn.classList.remove("active");
            showToast("No Patchwork entry found for this message", "info");
          }
        } catch (err) {
          showPatchwork = false;
          pwBar.style.display = "none";
          togglePwBtn.classList.remove("active");
          showToast(`Patchwork lookup failed: ${err.message}`, "error");
        } finally {
          togglePwBtn.disabled = false;
          togglePwBtn.innerHTML = origBtnText;
        }
      });
    }

    // Event: Toggle Header Dropdown & Select Options
    const toggleBtn = container.querySelector("#btn-toggle-headers");
    const headerDropdown = container.querySelector("#header-dropdown");
    if (toggleBtn && headerDropdown) {
      toggleBtn.addEventListener("click", (e) => {
        e.stopPropagation();
        const tagDropdown = container.querySelector("#header-tag-dropdown");
        if (tagDropdown) tagDropdown.style.display = "none";

        const isOpen = headerDropdown.style.display !== "none";
        headerDropdown.style.display = isOpen ? "none" : "block";
      });

      headerDropdown.querySelectorAll("[data-header-mode]").forEach((btn) => {
        btn.addEventListener("click", (e) => {
          e.stopPropagation();
          headerDropdown.style.display = "none";
          headerMode = btn.getAttribute("data-header-mode");
          localStorage.setItem("undine_header_mode", headerMode);
          render(currentDetail);
        });
      });

      const showRawBtn = headerDropdown.querySelector("#btn-menu-raw-header");
      if (showRawBtn) {
        showRawBtn.addEventListener("click", (e) => {
          e.stopPropagation();
          headerDropdown.style.display = "none";
          openRawHeaderModal(currentDetail);
        });
      }
    }

    // Event: Click (+N more) badge to expand full headers
    container.querySelectorAll(".meta-more-badge").forEach((badge) => {
      badge.addEventListener("click", () => {
        headerMode = "full";
        localStorage.setItem("undine_header_mode", headerMode);
        render(currentDetail);
      });
    });

    const isPendingReview = reviewTargetStatus === "in-progress" || reviewTargetStatus === "running" || reviewTargetStatus === "queued";
    const bodyTrim = (m.body_plain || "").trim();
    const isAiReviewPending = isAiReview && (bodyTrim === "Queued" || bodyTrim === "In-progress" ||
      (reviewedMsg && store.getAiReviewStatusForTarget && (store.getAiReviewStatusForTarget(reviewedMsg.id) || store.getAiReviewStatusForTarget(reviewedMsg.message_id))));

    if (isPendingReview || isAiReviewPending) {
      startReviewPolling(m);
    } else {
      stopReviewPolling();
    }
  }

  async function loadPatchworkStatus(m, container, refresh = false) {
    const currentMsgId = m.message_id;
    const cleanMsgId = String(currentMsgId || "").trim().replace(/^<|>$/g, "");
    const pwBar = container.querySelector("#patchwork-bar");
    const pwBtn = container.querySelector("#btn-toggle-patchwork");
    if (!pwBar) return false;

    try {
      const pw = await fetchPatchworkInfo(currentMsgId, refresh);
      // Ensure the message hasn't changed while request was in-flight
      if (!currentDetail || (currentDetail.message?.id !== m.id && currentDetail.message?.message_id !== currentMsgId)) {
        return false;
      }
      const isFound = pw && (pw.found === true || (pw.found !== false && (pw.id != null || pw.state != null)));
      if (!isFound) {
        pwBar.style.display = "none";
        pwBar.removeAttribute("data-msgid");
        pwBar.innerHTML = "";
        if (pwBtn) {
          pwBtn.classList.remove("active");
        }
        return false;
      }

      pwBar.setAttribute("data-msgid", cleanMsgId || currentMsgId);
      if (pwBtn) {
        pwBtn.classList.toggle("active", showPatchwork);
      }

      pwBar.style.display = showPatchwork ? "flex" : "none";

      // Delegate Badge & Dropdown
      let delegateHtml = "";
      let delegateBadgeHtml = "";
      if (pw.is_delegated_to_me) {
        delegateBadgeHtml = `<span class="pw-badge pw-badge-me" id="pw-delegate-display" title="This patch is delegated to you on patchwork.kernel.org">👤 Delegated to Me</span>`;
      } else if (pw.delegate) {
        const dName = pw.delegate.first_name || pw.delegate.last_name
          ? `${pw.delegate.first_name || ""} ${pw.delegate.last_name || ""}`.trim()
          : (pw.delegate.username || pw.delegate.email || "Unknown");
        delegateBadgeHtml = `<span class="pw-badge pw-badge-delegate" id="pw-delegate-display" title="Delegated to ${escapeHtml(dName)}">👤 Delegate: ${escapeHtml(dName)}</span>`;
      } else {
        delegateBadgeHtml = `<span class="pw-badge pw-badge-unassigned" id="pw-delegate-display" title="Not delegated to anyone">👤 Delegate: Unassigned</span>`;
      }

      if (pw.can_update && pw.type === "patch") {
        const delegateUsers = pw.delegate_users || store.getState().config?.patchwork_delegate_users || [];
        const userOptionsHtml = Array.isArray(delegateUsers)
          ? delegateUsers
              .filter((u) => u && String(u).trim() && String(u).trim().toLowerCase() !== "me")
              .map((u) => `<option value="user:${escapeAttr(String(u).trim())}">${escapeHtml(String(u).trim())}</option>`)
              .join("")
          : "";
        delegateHtml = `
          <div class="pw-delegate-wrapper">
            ${delegateBadgeHtml}
            <select class="pw-delegate-select" id="pw-delegate-dropdown" title="Change patch delegate on Patchwork">
              <option value="" disabled selected>Delegate ▾</option>
              <option value="me">Delegate to Me</option>
              <option value="none">Unassigned</option>
              ${userOptionsHtml}
            </select>
          </div>
        `;
      } else {
        delegateHtml = delegateBadgeHtml;
      }

      // State Badge & Dropdown
      let stateHtml = "";
      const stateSlug = pw.state || "new";
      const stateClass = `pw-state-${stateSlug}`;
      const stateLabels = {
        "new": "New",
        "queued": "Queued",
        "under-review": "Under Review",
        "accepted": "Accepted",
        "rejected": "Rejected",
        "rfc": "RFC",
        "not-applicable": "Not Applicable",
        "changes-requested": "Changes Requested",
        "awaiting-upstream": "Awaiting Upstream",
        "superseded": "Superseded",
        "deferred": "Deferred",
        "handled-elsewhere": "Handled Elsewhere",
      };
      const stateDisplay = stateLabels[stateSlug] || stateSlug;

      if (pw.can_update && pw.type === "patch") {
        stateHtml = `
          <div class="pw-state-wrapper">
            <span class="pw-badge ${stateClass}" id="pw-state-display">${escapeHtml(stateDisplay)}</span>
            <select class="pw-state-select" id="pw-state-dropdown" title="Change patch state on Patchwork">
              <option value="" disabled selected>Change State ▾</option>
              <option value="new">New</option>
              <option value="queued">Queued</option>
              <option value="under-review">Under Review</option>
              <option value="accepted">Accepted</option>
              <option value="changes-requested">Changes Requested</option>
              <option value="rejected">Rejected</option>
              <option value="rfc">RFC</option>
              <option value="superseded">Superseded</option>
              <option value="deferred">Deferred</option>
              <option value="handled-elsewhere">Handled Elsewhere</option>
            </select>
          </div>
        `;
      } else if (pw.state) {
        stateHtml = `<span class="pw-badge ${stateClass}">${escapeHtml(stateDisplay)}</span>`;
      }

      // CI Checks Summary Badge
      let checksHtml = "";
      if (pw.checks_count > 0 || pw.check_summary) {
        const summary = pw.check_summary || "pending";
        let checkIcon = "⏳";
        let checkClass = "pw-ci-pending";
        let checkText = "CI: Pending";

        if (summary === "success" || (pw.checks_passed === pw.checks_count && pw.checks_count > 0)) {
          checkIcon = "✅";
          checkClass = "pw-ci-success";
          checkText = `CI: ${pw.checks_passed}/${pw.checks_count} passed`;
        } else if (summary === "fail" || pw.checks_failed > 0) {
          checkIcon = "❌";
          checkClass = "pw-ci-fail";
          checkText = `CI: ${pw.checks_failed} failed (${pw.checks_passed}/${pw.checks_count} passed)`;
        } else if (summary === "warning" || pw.checks_warning > 0) {
          checkIcon = "⚠️";
          checkClass = "pw-ci-warning";
          checkText = `CI: ${pw.checks_warning} warnings`;
        }

        checksHtml = `
          <button type="button" class="pw-badge pw-badge-clickable ${checkClass}" id="btn-toggle-checks" title="Click to view CI checks details">
            ${checkIcon} ${escapeHtml(checkText)} ▾
          </button>
        `;
      }

      // Series Summary Badge
      let seriesHtml = "";
      const seriesObj = Array.isArray(pw.series) ? (pw.series[0] || null) : pw.series;
      if (seriesObj) {
        const v = seriesObj.version || 1;
        const total = pw.total_series_patches || seriesObj.total || (Array.isArray(seriesObj.patches) ? seriesObj.patches.length : 1);
        const myCount = pw.my_delegated_series_count || 0;
        let myNote = "";
        if (myCount > 0) {
          myNote = ` (${myCount}/${total} delegated to you)`;
        }
        seriesHtml = `
          <button type="button" class="pw-badge pw-badge-clickable pw-badge-series" id="btn-toggle-series" title="Click to inspect patches in this series">
            📦 Series v${v} [${total} patches${myNote}] ▾
          </button>
        `;
      }

      // Project / Archive Badge (with other archives listed in title if cross-posted)
      let projectTitle = pw.project?.name || "";
      if (pw.other_projects && pw.other_projects.length > 0) {
        const others = pw.other_projects
          .map((p) => p.link_name || p.name || p.list_email)
          .filter(Boolean)
          .join(", ");
        if (others) {
          projectTitle = projectTitle
            ? `${projectTitle} (also archived in: ${others})`
            : `Also archived in: ${others}`;
        }
      }
      const projectHtml = pw.project?.link_name
        ? `<span class="pw-project-tag" title="${escapeHtml(projectTitle)}">[${escapeHtml(pw.project.link_name)}]</span>`
        : "";

      // Build the bar HTML
      pwBar.innerHTML = `
        <div class="pw-bar-left">
          <span class="pw-label">🧩 Patchwork:</span>
          ${projectHtml}
          ${delegateHtml}
          ${stateHtml}
          ${checksHtml}
          ${seriesHtml}
        </div>
        <div class="pw-bar-right">
          <a href="${escapeHtml(pw.web_url || pw.url || '#')}" target="_blank" rel="noopener noreferrer" class="pw-link" title="Open patch on patchwork.kernel.org">
            patchwork ↗
          </a>
        </div>
        <!-- Expandable CI Checks Drawer -->
        <div class="pw-drawer" id="pw-checks-drawer" style="display: none;"></div>
        <!-- Expandable Series Drawer -->
        <div class="pw-drawer" id="pw-series-drawer" style="display: none;"></div>
      `;

      // Event listener: Delegate Select & Display click
      const delegateSelect = pwBar.querySelector("#pw-delegate-dropdown");
      const delegateDisplay = pwBar.querySelector("#pw-delegate-display");
      if (delegateSelect && delegateDisplay) {
        delegateDisplay.addEventListener("click", () => {
          if (!delegateSelect.disabled) {
            if (typeof delegateSelect.showPicker === "function") {
              try {
                delegateSelect.showPicker();
              } catch (_) {
                delegateSelect.focus();
              }
            } else {
              delegateSelect.focus();
            }
          }
        });
      }

      if (delegateSelect) {
        delegateSelect.addEventListener("change", async (e) => {
          const val = e.target.value;
          if (!val) return;
          delegateSelect.disabled = true;
          try {
            if (val === "me") {
              await delegatePatchworkPatchToMe(pw.id);
              showToast(`Patch #${pw.id} delegated to you!`, "success");
            } else if (val === "none") {
              await updatePatchworkPatch(pw.id, { delegate: null });
              showToast(`Patch #${pw.id} delegate cleared (unassigned)`, "success");
            } else if (val.startsWith("user:")) {
              const username = val.slice(5);
              await updatePatchworkPatch(pw.id, { delegate: username });
              showToast(`Patch #${pw.id} delegated to ${username}!`, "success");
            }
            await loadPatchworkStatus(m, container);
          } catch (err) {
            showToast(`Failed to update delegate: ${err.message}`, "error");
            delegateSelect.disabled = false;
            delegateSelect.value = "";
          }
        });
      }

      // Event listener: State Select & Display click
      const stateSelect = pwBar.querySelector("#pw-state-dropdown");
      const stateDisplayBadge = pwBar.querySelector("#pw-state-display");
      if (stateSelect && stateDisplayBadge) {
        stateDisplayBadge.addEventListener("click", () => {
          if (!stateSelect.disabled) {
            if (typeof stateSelect.showPicker === "function") {
              try {
                stateSelect.showPicker();
              } catch (_) {
                stateSelect.focus();
              }
            } else {
              stateSelect.focus();
            }
          }
        });
      }

      if (stateSelect) {
        stateSelect.addEventListener("change", async (e) => {
          const newState = e.target.value;
          if (!newState) return;
          stateSelect.disabled = true;
          try {
            await updatePatchworkPatch(pw.id, { state: newState });
            showToast(`Patch state updated to "${stateLabels[newState] || newState}"`, "success");
            await loadPatchworkStatus(m, container);
          } catch (err) {
            showToast(`Failed to update state: ${err.message}`, "error");
            stateSelect.disabled = false;
          }
        });
      }

      // Event listener: Toggle CI Checks Drawer
      const toggleChecksBtn = pwBar.querySelector("#btn-toggle-checks");
      const checksDrawer = pwBar.querySelector("#pw-checks-drawer");
      if (toggleChecksBtn && checksDrawer) {
        toggleChecksBtn.addEventListener("click", () => {
          if (checksDrawer.style.display === "none") {
            const checksList = Array.isArray(pw.checks) ? pw.checks : [];
            if (checksList.length > 0) {
              let chkHtml = `<div class="pw-checks-list"><div class="pw-drawer-title">CI Test Checks (${checksList.length}):</div>`;
              for (const c of checksList) {
                const cState = c.state || "pending";
                const cIcon = cState === "success" ? "✅" : (cState === "fail" ? "❌" : (cState === "warning" ? "⚠️" : "⏳"));
                const cName = c.context || "Check";
                const cDesc = c.description || "";
                const cUrl = c.target_url;
                chkHtml += `
                  <div class="pw-check-item pw-ci-${cState}">
                    <span class="pw-check-icon">${cIcon}</span>
                    <span class="pw-check-name">${escapeHtml(cName)}</span>
                    ${cDesc ? `<span class="pw-check-desc">${escapeHtml(cDesc)}</span>` : ""}
                    ${cUrl ? `<a href="${escapeHtml(cUrl)}" target="_blank" rel="noopener noreferrer" class="pw-check-link">Logs ↗</a>` : ""}
                  </div>
                `;
              }
              chkHtml += `</div>`;
              checksDrawer.innerHTML = chkHtml;
            } else {
              checksDrawer.innerHTML = `<div style="padding: 6px 10px; color: var(--text-muted);">No detailed checks available.</div>`;
            }
            checksDrawer.style.display = "block";
            const seriesDrawer = pwBar.querySelector("#pw-series-drawer");
            if (seriesDrawer) seriesDrawer.style.display = "none";
          } else {
            checksDrawer.style.display = "none";
          }
        });
      }

      // Event listener: Toggle Series Drawer
      const toggleSeriesBtn = pwBar.querySelector("#btn-toggle-series");
      const seriesDrawer = pwBar.querySelector("#pw-series-drawer");
      if (toggleSeriesBtn && seriesDrawer) {
        toggleSeriesBtn.addEventListener("click", () => {
          if (seriesDrawer.style.display === "none") {
            const seriesObj = Array.isArray(pw.series) ? (pw.series[0] || null) : pw.series;
            const sPatches = pw.series_patches || (seriesObj?.patches) || [];
            let sHtml = `<div class="pw-series-list">`;
            sHtml += `<div class="pw-drawer-title">Series: ${escapeHtml(seriesObj?.name || m.subject)} (${sPatches.length} patches):</div>`;
            if (sPatches.length > 0) {
              for (const sp of sPatches) {
                const isCurrent = sp.id === pw.id;
                const isMine = Boolean(sp.is_delegated_to_me);
                const dInfo = sp.delegate ? (sp.delegate.first_name || sp.delegate.username || sp.delegate.email) : "Unassigned";
                const spState = sp.state ? `[${sp.state}]` : "";
                sHtml += `
                  <div class="pw-series-item ${isCurrent ? 'pw-series-current' : ''}">
                    <span class="pw-series-item-title">${escapeHtml(sp.name || 'Patch #' + sp.id)}</span>
                    ${spState ? `<span class="pw-series-item-state">${escapeHtml(spState)}</span>` : ''}
                    ${isMine ? '<span class="pw-badge pw-badge-me-small">👤 Me</span>' : `<span class="pw-series-item-del">👤 ${escapeHtml(dInfo)}</span>`}
                    ${sp.web_url ? `<a href="${escapeHtml(sp.web_url)}" target="_blank" rel="noopener noreferrer" class="pw-check-link">Open ↗</a>` : ''}
                  </div>
                `;
              }
            }
            sHtml += `</div>`;
            seriesDrawer.innerHTML = sHtml;
            seriesDrawer.style.display = "block";
            if (checksDrawer) checksDrawer.style.display = "none";
          } else {
            seriesDrawer.style.display = "none";
          }
        });
      }

      return true;
    } catch (err) {
      console.error("Failed to load Patchwork metadata:", err);
      return false;
    }
  }



  // Subscribe to store events
  store.subscribe((event, payload) => {
    if (event === EVENT_FOLDER_SELECTED) {
      loadDetail(null);
    } else if (event === EVENT_MESSAGES_UPDATED) {
      if (store.getState().messages.length === 0) {
        loadDetail(null);
      }
    } else if (event === EVENT_MESSAGE_SELECTED) {
      loadDetail(payload);
    } else if (event === EVENT_MESSAGE_DETAIL_LOADED) {
      render(payload);
    } else if (event === EVENT_NOTES_UPDATED || event === EVENT_AI_REVIEW_STATE_CHANGED) {
      if (currentDetail) {
        render(currentDetail);
      }
    } else if (event === EVENT_MESSAGE_TAG_UPDATED) {
      if (currentDetail && currentDetail.message && currentDetail.message.id === payload.messageId) {
        currentDetail.message.tag_key = payload.tagKey;
        currentDetail.message.tag_name = payload.tagName;
        currentDetail.message.tag_color = payload.tagColor;
        render(currentDetail);
      }
    }
  });

  // Close tag and header dropdown on outside click
  document.addEventListener("click", (e) => {
    const tagDropdown = container.querySelector("#header-tag-dropdown");
    const tagBtn = container.querySelector("#btn-change-tag");
    if (tagDropdown && tagDropdown.style.display !== "none" && !tagDropdown.contains(e.target) && !tagBtn?.contains(e.target)) {
      tagDropdown.style.display = "none";
    }

    const headerDropdown = container.querySelector("#header-dropdown");
    const headerBtn = container.querySelector("#btn-toggle-headers");
    if (headerDropdown && headerDropdown.style.display !== "none" && !headerDropdown.contains(e.target) && !headerBtn?.contains(e.target)) {
      headerDropdown.style.display = "none";
    }
  });

  // Global Ctrl+E listener
  window.addEventListener("keydown", (e) => {
    if (e.key === "e" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      const current = store.getState().activeMessageDetail?.message;
      if (current && !isAiReviewMessage(current)) {
        if (isDraftMessage(current)) {
          triggerEdit(current);
        } else {
          triggerOpenEditor(current);
        }
      } else if (isDraftFolder(store.getState().selectedFolderId)) {
        triggerNewMessage();
      }
    }
  });
}

export function isAiReviewFolder(folderId) {
  if (folderId == null) return false;
  const folders = store.getState().folders || [];
  const folder = folders.find((f) => f.id === folderId);
  if (!folder) return false;
  const name = (folder.name || "").trim();
  return name === FOLDER_AI_REVIEWS || name.toLowerCase() === "ai-reviews";
}

export function isAiReviewMessage(msg) {
  if (!msg) return false;
  if (msg.is_ai_review) return true;
  const state = store.getState();
  const selectedFolderId = state.selectedFolderId;
  const targetFolderId = msg.folder_id != null ? msg.folder_id : selectedFolderId;
  if (isAiReviewFolder(targetFolderId)) return true;
  if (selectedFolderId != null && isAiReviewFolder(selectedFolderId)) return true;
  if (msg.folder_name === FOLDER_AI_REVIEWS || msg.folder_name === "AI-reviews") return true;
  if ((msg.from_name || "").trim() === "AI review") return true;
  return false;
}

export function triggerOpenReviewed(reviewedMsg) {
  if (!reviewedMsg) return;
  const targetFolderId = reviewedMsg.folder_id;
  const targetMsgId = reviewedMsg.id || reviewedMsg.message_id;
  const folderName = reviewedMsg.folder_name || "mailbox";
  if (targetFolderId != null) {
    showToast(`Opening reviewed message in ${folderName}...`, "info");
    store.selectFolder(targetFolderId, targetMsgId);
  } else {
    showToast("Could not determine mailbox for reviewed message", "error");
  }
}

export function isDraftFolder(folderId) {
  if (folderId == null) return false;
  const folders = store.getState().folders || [];
  const folder = folders.find((f) => f.id === folderId);
  if (!folder) return false;
  const name = (folder.name || "").toLowerCase().trim();
  return (
    name === "drafts" ||
    name === "draft" ||
    name.endsWith("/drafts") ||
    name.endsWith("/draft") ||
    name.endsWith(".drafts") ||
    name.endsWith(".draft")
  );
}

export function isDraftMessage(msg) {
  if (!msg) return false;
  const state = store.getState();
  const selectedFolderId = state.selectedFolderId;
  const targetFolderId = msg.folder_id != null ? msg.folder_id : selectedFolderId;
  if (isDraftFolder(targetFolderId)) return true;
  if (selectedFolderId != null && isDraftFolder(selectedFolderId)) return true;
  if (msg.flags && String(msg.flags).includes("D")) return true;
  return false;
}

export async function triggerNewMessage() {
  const cfg = store.getState().config;
  if (cfg?.reply_use_external_editor && cfg?.has_external_editor) {
    try {
      showToast("Launching external editor...", "info");
      const sig = cfg?.signature ? `\n\n-- \n${cfg.signature}\n` : "";
      const res = await openEditor("", [], "", sig, "", "", "");
      showToast(`Editor launched on ${res.temp_file}`, "success");
    } catch (err) {
      showToast(`Failed to open editor: ${err.message}`, "error");
    }
  } else {
    openNewMessage();
  }
}

export async function triggerEdit(msg) {
  if (!msg || isAiReviewMessage(msg)) return;
  const isDraft = isDraftMessage(msg);
  const cfg = store.getState().config;
  let opened = false;

  if (cfg?.reply_use_external_editor && cfg?.has_external_editor) {
    opened = await triggerOpenEditEditor(msg);
  } else {
    openEditForMessage(msg);
    opened = true;
  }

  if (opened && isDraft) {
    try {
      const msgId = msg.id != null ? msg.id : msg.message_id;
      await deleteMessage(msgId);
      store.removeMessage(msg.id);
      const folders = await fetchFolders();
      store.setFolders(folders);
      const currentFolderId = store.getState().selectedFolderId;
      if (currentFolderId && isDraftFolder(currentFolderId)) {
        const msgs = await fetchMessages(currentFolderId);
        store.setMessages(msgs);
      }
    } catch (err) {
      console.error("Failed to delete draft message:", err);
      showToast(`Failed to remove draft from folder: ${err.message}`, "error");
    }
  }
}

export async function triggerOpenEditEditor(msg) {
  if (!msg || isAiReviewMessage(msg)) return false;
  const toStr = msg.to_addr || "";
  const ccStr = msg.cc_addr || "";
  const subjStr = msg.subject || "";
  const bodyStr = msg.body_plain || "";
  try {
    showToast("Launching external editor...", "info");
    const res = await openEditor(msg.message_id || (msg.id != null ? String(msg.id) : ""), [], "", bodyStr, toStr, ccStr, subjStr);
    showToast(`Editor launched on ${res.temp_file}`, "success");
    return true;
  } catch (err) {
    showToast(`Failed to open editor: ${err.message}`, "error");
    return false;
  }
}

export function triggerReply(msg) {
  if (!msg || isAiReviewMessage(msg)) return;
  const cfg = store.getState().config;
  if (cfg?.reply_use_external_editor && cfg?.has_external_editor) {
    triggerOpenEditor(msg);
  } else {
    openReplyForMessage(msg);
  }
}

export function triggerForward(msg) {
  if (!msg || isAiReviewMessage(msg)) return;
  const cfg = store.getState().config;
  if (cfg?.reply_use_external_editor && cfg?.has_external_editor) {
    triggerOpenForwardEditor(msg);
  } else {
    openForwardForMessage(msg);
  }
}

export async function triggerOpenEditor(msg) {
  if (!msg || isAiReviewMessage(msg)) return;
  const notes = store.getNotesList();
  try {
    showToast("Launching external editor...", "info");
    const res = await openEditor(msg.message_id, notes);
    showToast(`Editor launched on ${res.temp_file}`, "success");
  } catch (err) {
    showToast(`Failed to open editor: ${err.message}`, "error");
  }
}

export async function triggerOpenForwardEditor(msg) {
  if (!msg || isAiReviewMessage(msg)) return;
  const cleanSubj = (msg.subject || "").trim();
  const subject = cleanSubj.toLowerCase().startsWith("fwd:") ? cleanSubj : `Fwd: ${cleanSubj}`;
  const fromStr = msg.from_name ? `${msg.from_name} <${msg.from_addr}>` : (msg.from_addr || "");
  const toStr = msg.to_addr || "";
  const ccStr = msg.cc_addr || "";
  const dateStr = msg.date || "";
  const subjStr = msg.subject || "";

  const forwardBody = `\n---------- Forwarded message ---------\n` +
    `From: ${fromStr}\n` +
    `Date: ${dateStr}\n` +
    `Subject: ${subjStr}\n` +
    `To: ${toStr}\n` +
    (ccStr ? `Cc: ${ccStr}\n` : "") +
    `\n` +
    (msg.body_plain || "");

  try {
    showToast("Launching external editor...", "info");
    const res = await openEditor(msg.message_id, [], "", forwardBody, "", null, subject);
    showToast(`Editor launched on ${res.temp_file}`, "success");
  } catch (err) {
    showToast(`Failed to open editor: ${err.message}`, "error");
  }
}

function parseRecipients(addrStr) {
  if (!addrStr || !addrStr.trim()) return [];
  const parts = [];
  let current = "";
  let inQuotes = false;
  let inAngle = false;
  for (let i = 0; i < addrStr.length; i++) {
    const ch = addrStr[i];
    if (ch === '"' && (i === 0 || addrStr[i - 1] !== '\\')) {
      inQuotes = !inQuotes;
    } else if (ch === '<' && !inQuotes) {
      inAngle = true;
    } else if (ch === '>' && !inQuotes) {
      inAngle = false;
    } else if (ch === ',' && !inQuotes && !inAngle) {
      if (current.trim()) parts.push(current.trim());
      current = "";
      continue;
    }
    current += ch;
  }
  if (current.trim()) parts.push(current.trim());
  return parts;
}

function formatRecipientList(addrStr, isFull) {
  if (!addrStr || !addrStr.trim()) return { html: "(none)", isLong: false };
  const parts = parseRecipients(addrStr);
  const isLong = parts.length > 2 || addrStr.length > 100;

  if (isFull || !isLong) {
    return {
      html: escapeHtml(addrStr),
      isLong,
    };
  }

  // Abbreviated view: show first 2 recipients + (+N more) badge
  const visible = parts.slice(0, 2);
  const remaining = parts.length - 2;
  const moreText = remaining > 0
    ? ` <span class="meta-more-badge" title="Click to view full headers">(+${remaining} more)</span>`
    : ` <span class="meta-more-badge" title="Click to view full headers">(...)</span>`;
  return {
    html: escapeHtml(visible.join(", ")) + moreText,
    isLong: true,
  };
}

function showToast(message, type = "info") {
  const container = document.getElementById("toast-container");
  if (!container) return;
  const toast = document.createElement("div");
  toast.className = `toast ${type}`;
  toast.innerText = message;
  container.appendChild(toast);
  setTimeout(() => toast.remove(), 4000);
}

function escapeHtml(str) {
  if (!str) return "";
  return String(str).replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}

function escapeAttr(str) {
  if (!str) return "";
  return String(str).replace(/"/g, "&quot;");
}

export function openRawHeaderModal(detail) {
  if (!detail || !detail.message) {
    showToast("No message selected", "error");
    return;
  }
  const m = detail.message;

  const existing = document.getElementById("raw-header-modal-backdrop");
  if (existing) existing.remove();

  const backdrop = document.createElement("div");
  backdrop.id = "raw-header-modal-backdrop";
  backdrop.className = "raw-header-backdrop";
  backdrop.setAttribute("role", "dialog");
  backdrop.setAttribute("aria-modal", "true");

  const cleanMsgId = (m.message_id || "").replace(/^<|>$/g, "").trim();

  backdrop.innerHTML = `
    <div class="raw-header-modal">
      <div class="raw-header-header">
        <div class="raw-header-title-group">
          <span class="raw-header-icon">📜</span>
          <span class="raw-header-title">Raw Headers</span>
          <span class="raw-header-badge">${escapeHtml(cleanMsgId ? `<${cleanMsgId}>` : (m.subject || "Message"))}</span>
        </div>
        <div class="raw-header-actions">
          <button type="button" class="raw-header-btn secondary" id="btn-copy-raw-headers" title="Copy raw headers to clipboard">
            📋 Copy Headers
          </button>
          <button type="button" class="raw-header-btn-close" id="btn-close-raw-headers" title="Close (Esc)">✕</button>
        </div>
      </div>
      <div class="raw-header-body">
        <div class="raw-header-loading" id="raw-header-loading">⏳ Loading raw headers...</div>
        <pre class="raw-header-content" id="raw-header-content" style="display: none;"></pre>
      </div>
      <div class="raw-header-footer">
        <div class="raw-header-footer-left">
          <span class="raw-header-info" id="raw-header-line-count"></span>
        </div>
        <div class="raw-header-footer-right">
          <button type="button" class="raw-header-btn secondary" id="btn-close-raw-headers-footer">Close (Esc)</button>
        </div>
      </div>
    </div>
  `;

  document.body.appendChild(backdrop);

  const loadingEl = backdrop.querySelector("#raw-header-loading");
  const contentEl = backdrop.querySelector("#raw-header-content");
  const lineCountEl = backdrop.querySelector("#raw-header-line-count");
  const copyBtn = backdrop.querySelector("#btn-copy-raw-headers");
  const closeBtn = backdrop.querySelector("#btn-close-raw-headers");
  const closeFooterBtn = backdrop.querySelector("#btn-close-raw-headers-footer");

  let rawHeaderText = detail.raw_headers || "";

  function closeModal() {
    window.removeEventListener("keydown", onKeyDown);
    backdrop.remove();
  }

  function onKeyDown(e) {
    if (e.key === "Escape") {
      e.preventDefault();
      closeModal();
    }
  }

  window.addEventListener("keydown", onKeyDown);
  closeBtn?.addEventListener("click", closeModal);
  closeFooterBtn?.addEventListener("click", closeModal);
  backdrop.addEventListener("click", (e) => {
    if (e.target === backdrop) closeModal();
  });

  async function loadHeaders() {
    if (!rawHeaderText) {
      try {
        const res = await fetchRawHeaders(m.id);
        rawHeaderText = typeof res === "string" ? res : (res.raw_headers || "");
        detail.raw_headers = rawHeaderText;
      } catch (err) {
        if (loadingEl) loadingEl.innerText = `Failed to load raw headers: ${err.message}`;
        return;
      }
    }

    if (loadingEl) loadingEl.style.display = "none";
    if (contentEl) {
      contentEl.style.display = "block";
      contentEl.textContent = rawHeaderText;
    }
    const lines = rawHeaderText.split("\n").length;
    if (lineCountEl) {
      lineCountEl.innerText = `${lines} header lines · ${rawHeaderText.length} bytes`;
    }
  }

  copyBtn?.addEventListener("click", async () => {
    if (!rawHeaderText) return;
    try {
      await navigator.clipboard.writeText(rawHeaderText);
      showToast("Raw headers copied to clipboard!", "success");
    } catch {
      showToast("Failed to copy headers to clipboard", "error");
    }
  });

  loadHeaders();
}

