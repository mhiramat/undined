/**
 * SeriesWorkspace: Human-controlled Patch Series Review & Send Workspace (#series/{series_id}).
 * Enforces explicit human confirmation before any SMTP transmission and drives live progress via SSE.
 */

import {
  cancelSeriesSend,
  deleteSeries,
  fetchFolders,
  fetchMessages,
  fetchSeriesDetail,
  fetchSeriesList,
  openEditor,
  sendSeries,
  testSendSeriesSelf,
  updateSeriesHeaders,
  updateSeriesItem,
} from "../api.js";
import { store } from "../store.js";

function showToast(message, type = "info") {
  const container = document.getElementById("toast-container");
  if (!container) return;
  const toast = document.createElement("div");
  toast.className = `toast ${type}`;
  toast.innerText = message;
  container.appendChild(toast);
  setTimeout(() => toast.remove(), 4500);
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

function parseAddressCSV(raw) {
  if (!raw) return [];
  return String(raw)
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

function formatStatusPill(status, sentCount, totalCount) {
  switch (status) {
    case "sending":
      return `<span class="series-status-pill sending">Sending ${sentCount}/${totalCount}</span>`;
    case "paused":
      return `<span class="series-status-pill paused">Paused ${sentCount}/${totalCount}</span>`;
    case "failed":
      return `<span class="series-status-pill failed">Failed (${sentCount}/${totalCount})</span>`;
    case "sent":
      return `<span class="series-status-pill sent">Sent ✓</span>`;
    default:
      return `<span class="series-status-pill draft">Draft</span>`;
  }
}

function formatItemBadge(status, isEdited) {
  if (status === "sent") return `<span class="series-item-badge sent">Sent ✓</span>`;
  if (status === "sending") return `<span class="series-item-badge sending">Sending…</span>`;
  if (status === "failed") return `<span class="series-item-badge failed">Failed ✗</span>`;
  if (isEdited) return `<span class="series-item-badge edited">Edited</span>`;
  return `<span class="series-item-badge draft">Draft</span>`;
}

function renderDiffPreviewHtml(body) {
  const lines = String(body || "").replace(/\r\n/g, "\n").split("\n");
  let inDiff = false;
  return lines
    .map((line) => {
      if (line.startsWith("diff --git ")) inDiff = true;
      let cls = "series-diff-line";
      if (inDiff) {
        if (line.startsWith("+++") || line.startsWith("---")) cls += " diff-file-hdr";
        else if (line.startsWith("@@")) cls += " diff-hunk-hdr";
        else if (line.startsWith("+")) cls += " diff-add";
        else if (line.startsWith("-")) cls += " diff-del";
      }
      return `<div class="${cls}">${escapeHtml(line) || " "}</div>`;
    })
    .join("");
}

export async function renderSeriesWorkspace(container, seriesId, stateRef = {}) {
  if (!container || !seriesId) return;

  container.innerHTML = `<div style="padding: 24px; color: var(--text-muted);">Loading patch series ${escapeHtml(seriesId)}…</div>`;

  let detail;
  try {
    detail = await fetchSeriesDetail(seriesId);
  } catch (err) {
    container.innerHTML = `<div style="padding: 24px; color: var(--accent-red);">Failed to load patch series: ${escapeHtml(err.message)}</div>`;
    return;
  }

  if (store.getState().selectedSeriesId !== seriesId) {
    return;
  }

  let selectedSeqIndex =
    stateRef.selectedSeqIndex != null && detail.items.some((it) => it.seq_index === stateRef.selectedSeqIndex)
      ? stateRef.selectedSeqIndex
      : (detail.items[0]?.seq_index ?? 0);
  let editMode = Boolean(stateRef.editMode);
  const editedIndices = stateRef.editedIndices || new Set();

  function renderWorkspace() {
    stateRef.selectedSeqIndex = selectedSeqIndex;
    stateRef.editMode = editMode;
    stateRef.editedIndices = editedIndices;

    const items = detail.items || [];
    const totalCount = detail.total_count ?? items.length;
    const sentCount = detail.sent_count ?? items.filter((it) => it.status === "sent").length;
    const remainingCount = Math.max(0, totalCount - sentCount);
    const isSending = Boolean(detail.is_sending_now || detail.status === "sending");
    const isSentAll = remainingCount === 0 && totalCount > 0;
    const activeItem = items.find((it) => it.seq_index === selectedSeqIndex) || items[0] || null;
    const warnings = Array.isArray(detail.warnings) ? detail.warnings : [];
    const activeItemWarnings = activeItem
      ? warnings.filter((w) => w.seq_index === activeItem.seq_index)
      : [];

    const primarySendLabel =
      detail.status === "paused" || detail.status === "failed"
        ? `▶ Resume Remaining (${remainingCount}/${totalCount})…`
        : `🚀 Send Series (${remainingCount} emails)…`;

    const pct = totalCount > 0 ? Math.round((sentCount / totalCount) * 100) : 0;
    const showProgressDrawer = isSending || detail.status === "paused" || detail.status === "failed" || detail.status === "sent";
    const failedItem = items.find((it) => it.status === "failed");

    const hasExternalEditor = Boolean(store.getConfig()?.has_external_editor);

    container.innerHTML = `
      <div class="series-workspace" id="series-workspace" data-series-id="${escapeHtml(detail.series_id)}">
        <div class="series-header-bar">
          <div class="series-title-row">
            <div class="series-title-left">
              <span class="series-icon">📦</span>
              <strong>${escapeHtml(detail.series_key)}</strong>
              <span class="series-version-badge">v${escapeHtml(detail.version || 1)} · ${totalCount} msgs</span>
              ${detail.branch ? `<span class="series-branch-badge">${escapeHtml(detail.branch)}</span>` : ""}
              ${formatStatusPill(detail.status, sentCount, totalCount)}
            </div>
            <div class="series-actions-right">
              <button type="button" class="action-btn" id="btn-series-test-self" ${isSending ? "disabled" : ""} title="Send test copy only to ${escapeHtml(detail.from_email)}">
                🧪 Send Test to Self
              </button>
              <button type="button" class="action-btn primary" id="btn-series-send" ${isSending || isSentAll ? "disabled" : ""}>
                ${escapeHtml(primarySendLabel)}
              </button>
              <button type="button" class="action-btn danger" id="btn-series-discard" ${isSending ? "disabled" : ""}>
                🗑️ Discard Series
              </button>
            </div>
          </div>

          <div class="series-shared-headers">
            <label class="series-field">
              <span>To:</span>
              <input type="text" id="series-to-input" value="${escapeHtml((detail.to_addrs || []).join(", "))}" ${isSending ? "disabled" : ""} />
            </label>
            <label class="series-field">
              <span>Cc:</span>
              <input type="text" id="series-cc-input" value="${escapeHtml((detail.cc_addrs || []).join(", "))}" ${isSending ? "disabled" : ""} />
            </label>
            <div class="series-field-row">
              <label class="series-field" style="flex: 1;">
                <span>In-Reply-To:</span>
                <input type="text" id="series-in-reply-to-input" value="${escapeHtml(detail.in_reply_to || "")}" placeholder="<optional-parent@kernel.org>" ${isSending ? "disabled" : ""} />
              </label>
              <label class="series-field series-delay-field">
                <span>Delay:</span>
                <select id="series-delay-select" ${isSending ? "disabled" : ""}>
                  <option value="1" ${Number(detail.send_delay_sec) === 1 ? "selected" : ""}>1s</option>
                  <option value="2" ${Number(detail.send_delay_sec || 2) === 2 ? "selected" : ""}>2s</option>
                  <option value="5" ${Number(detail.send_delay_sec) === 5 ? "selected" : ""}>5s</option>
                </select>
              </label>
              <button type="button" class="action-btn" id="btn-series-save-headers" ${isSending ? "disabled" : ""}>
                💾 Save Headers
              </button>
            </div>
          </div>
        </div>

        ${showProgressDrawer ? `
          <div class="series-progress-drawer status-${escapeHtml(detail.status)}" id="series-progress-drawer">
            <div class="series-progress-top">
              <div class="series-progress-text">
                <strong>Delivery Status:</strong> ${formatStatusPill(detail.status, sentCount, totalCount)}
                <span>(${sentCount} of ${totalCount} transmitted)</span>
                ${failedItem?.error_message ? `<span class="series-error-detail">— ${escapeHtml(failedItem.error_message)}</span>` : ""}
              </div>
              ${isSending ? `
                <button type="button" class="action-btn danger" id="btn-series-pause">
                  ⏸ Pause / Cancel
                </button>
              ` : ""}
            </div>
            <div class="series-progress-bar-track">
              <div class="series-progress-bar-fill" style="width: ${pct}%;"></div>
            </div>
          </div>
        ` : ""}

        <div class="series-body-split">
          <div class="series-patch-list" id="series-patch-list">
            ${items.map((it) => {
              const isSelected = activeItem && it.seq_index === activeItem.seq_index;
              const extraRcptCount = (it.extra_to?.length || 0) + (it.extra_cc?.length || 0);
              return `
                <button type="button" class="series-patch-item ${isSelected ? "active" : ""}" data-seq-index="${it.seq_index}">
                  <div class="series-patch-item-top">
                    <span class="series-patch-seq">${it.is_cover_letter ? "0 (Cover)" : `#${it.seq_index}`}</span>
                    ${formatItemBadge(it.status, editedIndices.has(it.seq_index))}
                  </div>
                  <div class="series-patch-subject">${escapeHtml(it.subject)}</div>
                  ${extraRcptCount > 0 ? `<div class="series-patch-extra">+${extraRcptCount} extra rcpt</div>` : ""}
                </button>
              `;
            }).join("")}
          </div>

          <div class="series-inspector-pane" id="series-inspector-pane">
            ${activeItem ? `
              <div class="series-inspector-toolbar">
                <div class="series-inspector-title">
                  <strong>${escapeHtml(activeItem.subject)}</strong>
                  ${activeItem.commit_sha ? `<span class="series-sha-badge">${escapeHtml(activeItem.commit_sha.slice(0, 12))}</span>` : ""}
                </div>
                <div class="series-inspector-controls">
                  <button type="button" class="action-btn ${!editMode ? "primary" : ""}" id="btn-series-mode-preview">📄 Diff View</button>
                  <button type="button" class="action-btn ${editMode ? "primary" : ""}" id="btn-series-mode-edit" ${activeItem.status === "sent" || isSending ? "disabled" : ""}>✏️ Edit Plain Text</button>
                  ${hasExternalEditor && activeItem.draft_msg_id && activeItem.status !== "sent" ? `
                    <button type="button" class="action-btn" id="btn-series-ext-editor" title="Open in external $EDITOR">🖥️ $EDITOR</button>
                  ` : ""}
                </div>
              </div>

              ${activeItemWarnings.length > 0 ? `
                <div class="series-warning-banner" id="series-warning-banner">
                  <strong>⚠️ Diff / Patch Warnings (non-blocking):</strong>
                  <ul>
                    ${activeItemWarnings.map((w) => `<li>${escapeHtml(w.message)}</li>`).join("")}
                  </ul>
                </div>
              ` : ""}

              ${editMode && activeItem.status !== "sent" ? `
                <div class="series-editor-form">
                  <label class="series-field">
                    <span>Subject:</span>
                    <input type="text" id="series-item-subject-input" value="${escapeHtml(activeItem.subject)}" />
                  </label>
                  <div class="series-field-row">
                    <label class="series-field" style="flex: 1;">
                      <span>Extra To:</span>
                      <input type="text" id="series-item-extra-to" value="${escapeHtml((activeItem.extra_to || []).join(", "))}" />
                    </label>
                    <label class="series-field" style="flex: 1;">
                      <span>Extra Cc:</span>
                      <input type="text" id="series-item-extra-cc" value="${escapeHtml((activeItem.extra_cc || []).join(", "))}" />
                    </label>
                  </div>
                  <textarea id="series-item-body-textarea" class="series-plain-textarea" spellcheck="false">${escapeHtml(activeItem.body || "")}</textarea>
                  <div class="series-editor-footer">
                    <span class="series-editor-hint">Strict 8-bit UTF-8 · Tab inserts literal \\t · Zero auto-wrapping</span>
                    <button type="button" class="action-btn primary" id="btn-series-save-item">💾 Save Item</button>
                  </div>
                </div>
              ` : `
                <div class="series-diff-preview" id="series-diff-preview">${renderDiffPreviewHtml(activeItem.body || "")}</div>
              `}
            ` : `<div style="padding: 24px; color: var(--text-muted);">No items in this series.</div>`}
          </div>
        </div>
      </div>
    `;

    // Wire item selection
    container.querySelectorAll(".series-patch-item").forEach((btn) => {
      btn.addEventListener("click", () => {
        selectedSeqIndex = parseInt(btn.getAttribute("data-seq-index"), 10);
        renderWorkspace();
      });
    });

    // Wire preview/edit toggles
    container.querySelector("#btn-series-mode-preview")?.addEventListener("click", () => {
      editMode = false;
      renderWorkspace();
    });
    container.querySelector("#btn-series-mode-edit")?.addEventListener("click", () => {
      editMode = true;
      renderWorkspace();
    });

    // Wire literal Tab insertion in plain-text textarea
    const bodyTextarea = container.querySelector("#series-item-body-textarea");
    if (bodyTextarea) {
      bodyTextarea.addEventListener("keydown", (e) => {
        if (e.key === "Tab") {
          e.preventDefault();
          const start = bodyTextarea.selectionStart;
          const end = bodyTextarea.selectionEnd;
          bodyTextarea.value = bodyTextarea.value.substring(0, start) + "\t" + bodyTextarea.value.substring(end);
          bodyTextarea.selectionStart = bodyTextarea.selectionEnd = start + 1;
        }
      });
    }

    // Save shared headers
    container.querySelector("#btn-series-save-headers")?.addEventListener("click", async () => {
      const toAddrs = parseAddressCSV(container.querySelector("#series-to-input")?.value);
      const ccAddrs = parseAddressCSV(container.querySelector("#series-cc-input")?.value);
      const inReplyTo = (container.querySelector("#series-in-reply-to-input")?.value || "").trim();
      const sendDelaySec = parseInt(container.querySelector("#series-delay-select")?.value || "2", 10);
      if (toAddrs.length === 0) {
        showToast("At least one To recipient is required.", "error");
        return;
      }
      try {
        detail = await updateSeriesHeaders(detail.series_id, {
          to_addrs: toAddrs,
          cc_addrs: ccAddrs,
          in_reply_to: inReplyTo,
          send_delay_sec: sendDelaySec,
        });
        showToast("Shared series headers updated across all unsent drafts.", "success");
        renderWorkspace();
      } catch (err) {
        showToast(err.message, "error");
      }
    });

    // Save individual item edit
    container.querySelector("#btn-series-save-item")?.addEventListener("click", async () => {
      if (!activeItem) return;
      const subject = (container.querySelector("#series-item-subject-input")?.value || "").trim();
      const body = container.querySelector("#series-item-body-textarea")?.value || "";
      const extraTo = parseAddressCSV(container.querySelector("#series-item-extra-to")?.value);
      const extraCc = parseAddressCSV(container.querySelector("#series-item-extra-cc")?.value);
      try {
        const res = await updateSeriesItem(detail.series_id, activeItem.seq_index, {
          subject,
          body,
          extra_to: extraTo,
          extra_cc: extraCc,
        });
        editedIndices.add(activeItem.seq_index);
        detail = await fetchSeriesDetail(detail.series_id);
        const warnCount = Array.isArray(res?.warnings) ? res.warnings.length : 0;
        if (warnCount > 0) {
          showToast(`Saved item #${activeItem.seq_index} with ${warnCount} non-blocking warning(s).`, "info");
        } else {
          showToast(`Saved item #${activeItem.seq_index}.`, "success");
        }
        renderWorkspace();
      } catch (err) {
        showToast(err.message, "error");
      }
    });

    // External editor launch
    container.querySelector("#btn-series-ext-editor")?.addEventListener("click", async () => {
      if (!activeItem?.draft_msg_id) return;
      try {
        await openEditor(activeItem.draft_msg_id);
        showToast("Launched external $EDITOR for this draft.", "info");
      } catch (err) {
        showToast(err.message || "Failed to launch external editor", "error");
      }
    });

    // Test Send to Self
    container.querySelector("#btn-series-test-self")?.addEventListener("click", async () => {
      if (!confirm(`Send a [TEST] copy of all ${totalCount} messages ONLY to ${detail.from_email}?`)) {
        return;
      }
      try {
        const res = await testSendSeriesSelf(detail.series_id);
        showToast(`Sent ${res.sent_count} test message(s) to ${detail.from_email}.`, "success");
      } catch (err) {
        showToast(err.message, "error");
      }
    });

    // Open Human Confirmation Modal before any SMTP transmission
    container.querySelector("#btn-series-send")?.addEventListener("click", () => {
      openConfirmSendModal(detail, async () => {
        try {
          await sendSeries(detail.series_id);
          detail = await fetchSeriesDetail(detail.series_id);
          renderWorkspace();
          showToast("Sequential patch series transmission started.", "info");
        } catch (err) {
          showToast(err.message, "error");
        }
      });
    });

    // Pause / Cancel active transmission
    container.querySelector("#btn-series-pause")?.addEventListener("click", async () => {
      try {
        await cancelSeriesSend(detail.series_id);
        showToast("Pause requested; halting after current message completes.", "info");
      } catch (err) {
        showToast(err.message, "error");
      }
    });

    // Discard series
    container.querySelector("#btn-series-discard")?.addEventListener("click", async () => {
      if (!confirm(`Discard series "${detail.series_key}" and delete its unsent Maildir Drafts files?`)) {
        return;
      }
      try {
        await deleteSeries(detail.series_id);
        const [list, folders] = await Promise.all([fetchSeriesList(), fetchFolders()]);
        store.setSeriesList(list);
        store.setFolders(folders);
        const curFolderId = store.getState().selectedFolderId;
        if (curFolderId) {
          const msgs = await fetchMessages(curFolderId);
          store.setMessages(msgs);
        }
        store.selectSeries(null);
        if (window.location.hash.startsWith("#series/")) {
          history.replaceState(null, "", window.location.pathname + window.location.search);
        }
        showToast(`Discarded series "${detail.series_key}".`, "success");
      } catch (err) {
        showToast(err.message, "error");
      }
    });
  }

  renderWorkspace();
}

function openConfirmSendModal(detail, onConfirm) {
  const existing = document.getElementById("series-confirm-modal-backdrop");
  if (existing) existing.remove();

  const remainingItems = (detail.items || []).filter((it) => it.status !== "sent");
  const warnings = Array.isArray(detail.warnings) ? detail.warnings : [];

  const backdrop = document.createElement("div");
  backdrop.id = "series-confirm-modal-backdrop";
  backdrop.className = "modal-backdrop series-confirm-backdrop";
  backdrop.innerHTML = `
    <div class="series-confirm-modal" role="dialog" aria-modal="true">
      <div class="series-confirm-header">
        <h3>🚀 Confirm Patch Series SMTP Transmission</h3>
        <button type="button" class="modal-close-btn" id="btn-close-confirm-series">✕</button>
      </div>
      <div class="series-confirm-body">
        <div class="series-confirm-row">
          <strong>SMTP Account:</strong>
          <span>${escapeHtml(detail.from_email)}${detail.smtp_host ? ` via ${escapeHtml(detail.smtp_host)}` : ""}</span>
        </div>
        <div class="series-confirm-row">
          <strong>Messages to Transmit:</strong>
          <span>${remainingItems.length} email(s) (delay: ${escapeHtml(detail.send_delay_sec || 2)}s between patches)</span>
        </div>
        <div class="series-confirm-row">
          <strong>To:</strong>
          <span>${escapeHtml((detail.all_to_addrs || detail.to_addrs || []).join(", "))}</span>
        </div>
        ${(detail.all_cc_addrs || detail.cc_addrs || []).length > 0 ? `
          <div class="series-confirm-row">
            <strong>Cc:</strong>
            <span>${escapeHtml((detail.all_cc_addrs || detail.cc_addrs || []).join(", "))}</span>
          </div>
        ` : ""}
        <div class="series-confirm-list">
          <strong>Ordered Subject List:</strong>
          <ol>
            ${remainingItems.map((it) => `<li>${escapeHtml(it.subject)}</li>`).join("")}
          </ol>
        </div>
        ${warnings.length > 0 ? `
          <div class="series-warning-banner">
            <strong>⚠️ Pre-Send Warnings (${warnings.length}):</strong>
            <ul>
              ${warnings.map((w) => `<li>${escapeHtml(w.message)}</li>`).join("")}
            </ul>
          </div>
        ` : ""}
      </div>
      <div class="series-confirm-footer">
        <button type="button" class="action-btn" id="btn-cancel-confirm-series">Cancel</button>
        <button type="button" class="action-btn primary" id="btn-confirm-send-series">
          ✅ Confirm &amp; Send Series (${remainingItems.length})
        </button>
      </div>
    </div>
  `;

  document.body.appendChild(backdrop);

  const close = () => backdrop.remove();
  backdrop.querySelector("#btn-close-confirm-series")?.addEventListener("click", close);
  backdrop.querySelector("#btn-cancel-confirm-series")?.addEventListener("click", close);
  backdrop.addEventListener("click", (e) => {
    if (e.target === backdrop) close();
  });
  backdrop.querySelector("#btn-confirm-send-series")?.addEventListener("click", async () => {
    close();
    await onConfirm();
  });
}
