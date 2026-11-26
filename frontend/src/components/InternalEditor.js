/**
 * InternalEditor: In-browser plaintext email composer and review response editor
 * with strict 8-bit plain-text formatting, Tab-key preservation (tab-size: 8),
 * and direct SMTP dispatch / Drafts saving.
 */

import { fetchConfig, fetchFolders, fetchKnownAddresses, fetchMessages, openEditor, previewQuote, saveDraft, sendEmail } from "../api.js";
import { EditorHistory } from "../editorHistory.js";
import { store } from "../store.js";

let activeModal = null;

export function openEditorInNewWindow(params = {}) {
  const sessionId = "comp_" + Date.now() + "_" + Math.random().toString(36).slice(2, 9);
  try {
    localStorage.setItem(sessionId, JSON.stringify(params));
  } catch (e) {
    console.error("Failed to store composer draft state in localStorage:", e);
  }

  const url = new URL(window.location.origin + window.location.pathname);
  url.searchParams.set("composer", sessionId);

  const w = window.open(
    url.toString(),
    sessionId,
    "width=1000,height=820,menubar=no,toolbar=no,location=no,status=no,resizable=yes,scrollbars=yes"
  );

  if (!w || w.closed || typeof w.closed === "undefined") {
    try {
      localStorage.removeItem(sessionId);
    } catch (_) {
      // Ignore storage cleanup errors
    }
    showToast("Pop-up window was blocked. Please allow pop-ups for UndineD.", "warning");
    return false;
  }
  if (w.focus) {
    w.focus();
  }
  return true;
}

const MAX_ATTACHMENT_TOTAL_BYTES = 25 * 1024 * 1024;

function formatAttachmentBytes(bytes) {
  const n = Number(bytes) || 0;
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function getComposerAttachmentIcon(filename, contentType) {
  const name = (filename || "").toLowerCase();
  const ct = (contentType || "").toLowerCase();
  if (name.endsWith(".patch") || name.endsWith(".diff") || ct.includes("patch") || ct.includes("diff")) return "🩹";
  if (ct.startsWith("image/") || /\.(png|jpe?g|gif|webp|svg)$/.test(name)) return "🖼️";
  if (ct === "application/pdf" || name.endsWith(".pdf")) return "📕";
  if (/\.(tar\.gz|tgz|zip|gz|xz|bz2|zst)$/.test(name)) return "🗜️";
  if (/\.(md|txt|log|c|h|py|go|js|ts|json|yaml|yml|csv)$/.test(name) || ct.startsWith("text/")) return "📄";
  return "📎";
}

function readFileAsBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      const result = typeof reader.result === "string" ? reader.result : "";
      const commaIdx = result.indexOf(",");
      resolve(commaIdx !== -1 ? result.slice(commaIdx + 1) : result);
    };
    reader.onerror = () => reject(new Error(`Failed to read file: ${file.name}`));
    reader.readAsDataURL(file);
  });
}

function serializeAttachmentsForApi(attList) {
  return (attList || []).map((att) => {
    if (att.content_base64) {
      return {
        filename: att.filename,
        content_type: att.content_type || "",
        content_base64: att.content_base64,
      };
    }
    return {
      filename: att.filename,
      content_type: att.content_type || "",
      source_message_id: att.source_message_id,
      source_attachment_index: att.source_attachment_index,
    };
  });
}

export function openInternalEditor({
  to = "",
  cc = "",
  subject = "",
  body = "",
  inReplyTo = "",
  references = "",
  origMsgId = "",
  draftId = null,
  notes = [],
  attachments = [],
  title = "Compose / Reply (Internal Editor)",
  titleIcon = "📝",
  isStandalone = false,
}) {
  const configuredMode = store.getState().config?.composer_window_mode || localStorage.getItem("undine_composer_window_mode");
  if (!isStandalone && configuredMode === "window") {
    const opened = openEditorInNewWindow({
      to,
      cc,
      subject,
      body,
      inReplyTo,
      references,
      origMsgId,
      draftId,
      notes,
      attachments,
      title,
      titleIcon,
    });
    if (opened) {
      return;
    }
  }

  // If already open, close previous
  closeInternalEditor();

  let sessionDraftId = draftId != null ? Number(draftId) : null;
  let sessionAttachments = Array.isArray(attachments)
    ? attachments.map((a) => ({
        filename: a.filename || "attachment",
        content_type: a.content_type || "",
        size: Number(a.size) || 0,
        content_base64: a.content_base64 || "",
        source_message_id: a.source_message_id != null ? Number(a.source_message_id) : null,
        source_attachment_index: a.source_attachment_index != null ? Number(a.source_attachment_index) : null,
      }))
    : [];

  const container = document.getElementById("composer-container") || document.body;
  const modalEl = document.createElement("div");
  modalEl.className = isStandalone ? "composer-backdrop standalone" : "composer-backdrop";

  modalEl.innerHTML = `
    <div class="composer-modal" role="dialog" aria-modal="true">
      <!-- Modal Header -->
      <div class="composer-header">
        <div class="composer-title-group">
          <span class="composer-icon">${escapeHtml(titleIcon)}</span>
          <span class="composer-title">${escapeHtml(title)}</span>
          <span class="composer-badge" id="comp-mime-badge">8-bit plain-text (UTF-8)</span>
        </div>
        <div class="composer-header-actions">
          ${!isStandalone ? `
          <button type="button" class="composer-btn secondary" id="btn-comp-popout" title="Open composer in new window">
            ⧉ Pop Out
          </button>` : ""}
          <button type="button" class="composer-btn secondary" id="btn-comp-external" title="Hand off current draft to external $EDITOR">
            🖥️ Open External Editor
          </button>
          <button type="button" class="composer-btn-close" id="btn-comp-close" title="Close editor (Esc)">✕</button>
        </div>
      </div>

      <!-- Address / Subject Form -->
      <div class="composer-meta-form">
        <div class="composer-field-row">
          <label class="composer-field-label">To:</label>
          <div class="composer-input-wrapper">
            <input type="text" class="composer-input" id="comp-to" value="${escapeAttr(to)}" placeholder="developer@kernel.org" autocomplete="off" />
            <div class="composer-autocomplete-dropdown" id="comp-to-autocomplete" style="display: none;"></div>
          </div>
        </div>
        <div class="composer-field-row">
          <label class="composer-field-label">Cc:</label>
          <div class="composer-input-wrapper">
            <input type="text" class="composer-input" id="comp-cc" value="${escapeAttr(cc)}" placeholder="linux-kernel@vger.kernel.org" autocomplete="off" />
            <div class="composer-autocomplete-dropdown" id="comp-cc-autocomplete" style="display: none;"></div>
          </div>
        </div>
        <div class="composer-field-row">
          <label class="composer-field-label">Subject:</label>
          <input type="text" class="composer-input" id="comp-subject" value="${escapeAttr(subject)}" placeholder="Re: [PATCH] ..." />
        </div>
      </div>

      <!-- Quick Action Toolbar for Review Tags & Undo/Redo -->
      <div class="composer-toolbar">
        <span class="composer-toolbar-label">Insert Tag:</span>
        <button type="button" class="composer-tag-btn" id="btn-insert-acked" title="Insert Acked-by: tag at cursor (Alt+A)">
          🏷️ Acked-by
        </button>
        <button type="button" class="composer-tag-btn" id="btn-insert-reviewed" title="Insert Reviewed-by: tag at cursor (Alt+R)">
          🏷️ Reviewed-by
        </button>
        <button type="button" class="composer-tag-btn" id="btn-erase-after-cursor" title="Erase text lines after cursor, keeping signature (Ctrl+K)">
          ✂️ Erase after cursor
        </button>
        <span class="composer-toolbar-separator"></span>
        <button type="button" class="composer-tag-btn" id="btn-comp-attach" title="Attach MIME-encoded file(s) (or drag & drop files onto editor)">
          📎 Attach File
        </button>
        <input type="file" id="comp-file-input" multiple style="display: none;" />
        <span class="composer-toolbar-separator"></span>
        <button type="button" class="composer-tag-btn" id="btn-comp-undo" title="Undo (Ctrl+Z)" disabled>
          ↩️ Undo
        </button>
        <button type="button" class="composer-tag-btn" id="btn-comp-redo" title="Redo (Ctrl+Y)" disabled>
          ↪️ Redo
        </button>
      </div>

      <!-- Attachments Bar (visible when files are attached) -->
      <div class="composer-attachments-bar" id="comp-attachments-bar" style="display: none;">
        <div class="composer-attachments-header">
          <span class="composer-attachments-title" id="comp-attachments-title">📎 Attachments (0)</span>
        </div>
        <div class="composer-attachments-chips" id="comp-attachments-chips"></div>
      </div>

      <!-- Main Monospace Editor -->
      <div class="composer-body-container">
        <textarea
          id="comp-body"
          class="composer-textarea"
          spellcheck="false"
          placeholder="Write your plaintext response or review comments here..."
        >${escapeHtml(body)}</textarea>
      </div>

      <!-- Modal Footer -->
      <div class="composer-footer">
        <div class="composer-footer-left">
          <span class="composer-pos-indicator" id="comp-cursor-pos">Ln 1, Col 1</span>
          <span class="composer-sep">·</span>
          <span class="composer-tab-indicator">Tab Size: 8</span>
          <span class="composer-sep">·</span>
          <span class="composer-hint">Ctrl+Z: Undo · Ctrl+Y: Redo · Alt+A: Acked-by · Alt+R: Reviewed-by · Ctrl+K: Erase after cursor · Ctrl+Enter: Send</span>
        </div>
        <div class="composer-footer-right">
          <button type="button" class="composer-btn secondary" id="btn-comp-save-draft">
            💾 Save Draft
          </button>
          <button type="button" class="composer-btn primary" id="btn-comp-send">
            📤 Send Email (Ctrl+Enter)
          </button>
        </div>
      </div>
    </div>
  `;

  container.appendChild(modalEl);
  activeModal = modalEl;

  // DOM Elements
  const toInput = modalEl.querySelector("#comp-to");
  const ccInput = modalEl.querySelector("#comp-cc");
  const subjectInput = modalEl.querySelector("#comp-subject");
  const bodyTextarea = modalEl.querySelector("#comp-body");
  const posIndicator = modalEl.querySelector("#comp-cursor-pos");
  const sendBtn = modalEl.querySelector("#btn-comp-send");
  const saveDraftBtn = modalEl.querySelector("#btn-comp-save-draft");
  const externalBtn = modalEl.querySelector("#btn-comp-external");
  const popoutBtn = modalEl.querySelector("#btn-comp-popout");
  const closeBtn = modalEl.querySelector("#btn-comp-close");
  const insertAckedBtn = modalEl.querySelector("#btn-insert-acked");
  const insertReviewedBtn = modalEl.querySelector("#btn-insert-reviewed");
  const eraseAfterCursorBtn = modalEl.querySelector("#btn-erase-after-cursor");
  const undoBtn = modalEl.querySelector("#btn-comp-undo");
  const redoBtn = modalEl.querySelector("#btn-comp-redo");
  const attachBtn = modalEl.querySelector("#btn-comp-attach");
  const fileInput = modalEl.querySelector("#comp-file-input");
  const attachmentsBar = modalEl.querySelector("#comp-attachments-bar");
  const attachmentsTitle = modalEl.querySelector("#comp-attachments-title");
  const attachmentsChips = modalEl.querySelector("#comp-attachments-chips");
  const mimeBadge = modalEl.querySelector("#comp-mime-badge");
  const modalDialog = modalEl.querySelector(".composer-modal");

  function renderAttachmentsUI() {
    if (!attachmentsBar || !attachmentsChips || !attachmentsTitle) return;
    if (sessionAttachments.length === 0) {
      attachmentsBar.style.display = "none";
      attachmentsChips.innerHTML = "";
      if (mimeBadge) {
        mimeBadge.textContent = "8-bit plain-text (UTF-8)";
      }
      return;
    }

    const totalSize = sessionAttachments.reduce((acc, a) => acc + (Number(a.size) || 0), 0);
    attachmentsTitle.textContent = `📎 Attachments (${sessionAttachments.length} · ${formatAttachmentBytes(totalSize)})`;
    if (mimeBadge) {
      mimeBadge.textContent = `MIME multipart/mixed (${sessionAttachments.length} file${sessionAttachments.length === 1 ? "" : "s"})`;
    }

    attachmentsChips.innerHTML = sessionAttachments
      .map((att, idx) => {
        const icon = getComposerAttachmentIcon(att.filename, att.content_type);
        const sizeLabel = formatAttachmentBytes(att.size);
        const hasDownload = att.source_message_id != null && att.source_attachment_index != null;
        const nameHtml = hasDownload
          ? `<a class="composer-attachment-link" href="/api/messages/${att.source_message_id}/attachments/${att.source_attachment_index}/${encodeURIComponent(att.filename)}" download="${escapeAttr(att.filename)}" title="Download ${escapeAttr(att.filename)}">${escapeHtml(att.filename)}</a>`
          : `<span class="composer-attachment-name">${escapeHtml(att.filename)}</span>`;
        return `
          <div class="composer-attachment-chip" data-att-idx="${idx}" title="${escapeAttr(att.filename)} (${escapeAttr(att.content_type || "application/octet-stream")}, ${sizeLabel})">
            <span class="composer-attachment-icon">${icon}</span>
            ${nameHtml}
            <span class="composer-attachment-size">${sizeLabel}</span>
            <button type="button" class="composer-attachment-remove" data-remove-idx="${idx}" title="Remove attachment">✕</button>
          </div>
        `;
      })
      .join("");

    attachmentsChips.querySelectorAll(".composer-attachment-remove").forEach((btn) => {
      btn.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        const idx = parseInt(btn.getAttribute("data-remove-idx"), 10);
        if (!Number.isNaN(idx) && idx >= 0 && idx < sessionAttachments.length) {
          const removed = sessionAttachments.splice(idx, 1)[0];
          renderAttachmentsUI();
          showToast(`Removed attachment: ${removed.filename}`, "info");
        }
      });
    });

    attachmentsBar.style.display = "block";
  }

  async function addFilesAsAttachments(fileList) {
    const files = Array.from(fileList || []);
    if (files.length === 0) return;

    let runningTotal = sessionAttachments.reduce((acc, a) => acc + (Number(a.size) || 0), 0);
    let addedCount = 0;

    for (const file of files) {
      if (runningTotal + file.size > MAX_ATTACHMENT_TOTAL_BYTES) {
        showToast(
          `Cannot attach "${file.name}": total attachment size exceeds ${formatAttachmentBytes(MAX_ATTACHMENT_TOTAL_BYTES)} limit`,
          "error"
        );
        continue;
      }
      try {
        const b64 = await readFileAsBase64(file);
        sessionAttachments.push({
          filename: file.name || "attachment.bin",
          content_type: file.type || "",
          size: file.size || 0,
          content_base64: b64,
          source_message_id: null,
          source_attachment_index: null,
        });
        runningTotal += file.size || 0;
        addedCount++;
      } catch (err) {
        showToast(err.message || `Failed to read ${file.name}`, "error");
      }
    }

    if (addedCount > 0) {
      renderAttachmentsUI();
      showToast(`Attached ${addedCount} file${addedCount === 1 ? "" : "s"}`, "success");
    }
  }

  attachBtn?.addEventListener("click", () => {
    fileInput?.click();
  });

  fileInput?.addEventListener("change", async () => {
    if (fileInput.files && fileInput.files.length > 0) {
      await addFilesAsAttachments(fileInput.files);
      fileInput.value = "";
    }
  });

  if (modalDialog) {
    let dragDepth = 0;
    modalDialog.addEventListener("dragenter", (e) => {
      if (e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files")) {
        e.preventDefault();
        dragDepth++;
        modalDialog.classList.add("composer-drag-over");
      }
    });
    modalDialog.addEventListener("dragover", (e) => {
      if (e.dataTransfer && Array.from(e.dataTransfer.types || []).includes("Files")) {
        e.preventDefault();
        e.dataTransfer.dropEffect = "copy";
      }
    });
    modalDialog.addEventListener("dragleave", (e) => {
      if (dragDepth > 0) {
        dragDepth--;
        if (dragDepth === 0) {
          modalDialog.classList.remove("composer-drag-over");
        }
      }
    });
    modalDialog.addEventListener("drop", async (e) => {
      if (e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files.length > 0) {
        e.preventDefault();
        e.stopPropagation();
        dragDepth = 0;
        modalDialog.classList.remove("composer-drag-over");
        await addFilesAsAttachments(e.dataTransfer.files);
      }
    });
  }

  renderAttachmentsUI();

  if (popoutBtn) {
    popoutBtn.addEventListener("click", () => {
      const currentParams = {
        to: toInput.value,
        cc: ccInput.value,
        subject: subjectInput.value,
        body: bodyTextarea.value,
        inReplyTo,
        references,
        origMsgId,
        draftId: sessionDraftId,
        notes,
        attachments: sessionAttachments,
        title,
        titleIcon,
      };
      closeInternalEditor();
      openEditorInNewWindow(currentParams);
    });
  }

  const toAutocomplete = modalEl.querySelector("#comp-to-autocomplete");
  const ccAutocomplete = modalEl.querySelector("#comp-cc-autocomplete");
  if (toInput && toAutocomplete) {
    setupAddressAutocomplete(toInput, toAutocomplete);
  }
  if (ccInput && ccAutocomplete) {
    setupAddressAutocomplete(ccInput, ccAutocomplete);
  }

  // Focus textarea
  setTimeout(() => {
    bodyTextarea.focus();
    // Position cursor at top or bottom depending on content
    bodyTextarea.setSelectionRange(0, 0);
    updateCursorPos();
  }, 50);

  const history = new EditorHistory(body, 0, 0);

  function updateUndoRedoUI() {
    if (undoBtn) undoBtn.disabled = !history.canUndo();
    if (redoBtn) redoBtn.disabled = !history.canRedo();
  }

  function performUndo() {
    const state = history.undo();
    if (!state) return;
    bodyTextarea.value = state.value;
    bodyTextarea.setSelectionRange(state.selectionStart, state.selectionEnd);
    updateCursorPos();
    updateUndoRedoUI();
  }

  function performRedo() {
    const state = history.redo();
    if (!state) return;
    bodyTextarea.value = state.value;
    bodyTextarea.setSelectionRange(state.selectionStart, state.selectionEnd);
    updateCursorPos();
    updateUndoRedoUI();
  }

  undoBtn?.addEventListener("click", () => {
    bodyTextarea.focus();
    performUndo();
  });
  redoBtn?.addEventListener("click", () => {
    bodyTextarea.focus();
    performRedo();
  });

  function getUserTagIdentity() {
    const cfg = store.getState().config;
    const name = cfg?.from_name?.trim() || "";
    const email = cfg?.from_email?.trim() || "";
    if (name && email) {
      return `${name} <${email}>`;
    }
    if (email) {
      return `<${email}>`;
    }
    if (name) {
      return name;
    }
    return "";
  }

  let lastTagInsertTime = 0;

  function insertTag(tagType) {
    const now = Date.now();
    if (now - lastTagInsertTime < 300) {
      return;
    }
    lastTagInsertTime = now;

    const identity = getUserTagIdentity();
    const hasIdentity = Boolean(identity);
    const tagText = hasIdentity ? `${tagType}: ${identity}` : `${tagType}: `;

    bodyTextarea.focus();

    const start = bodyTextarea.selectionStart;
    const end = bodyTextarea.selectionEnd;
    const val = bodyTextarea.value;

    const before = val.substring(0, start);
    const after = val.substring(end);

    const needLeadingNewline = before.length > 0 && !before.endsWith("\n");
    const needTrailingNewline = hasIdentity && (after.length === 0 || !after.startsWith("\n"));

    const textToInsert = (needLeadingNewline ? "\n" : "") + tagText + (needTrailingNewline ? "\n" : "");

    bodyTextarea.value = before + textToInsert + after;
    const newPos = start + textToInsert.length;
    bodyTextarea.selectionStart = bodyTextarea.selectionEnd = newPos;

    history.record(bodyTextarea.value, newPos, newPos, "tag", true);
    updateCursorPos();
    updateUndoRedoUI();
    showToast(`Inserted ${tagType}:`, "info");
  }

  function eraseLinesAfterCursor() {
    bodyTextarea.focus();
    const val = bodyTextarea.value;
    const start = bodyTextarea.selectionStart;
    if (start >= val.length) {
      showToast("Cursor is already at the end of text", "info");
      return;
    }

    const range = findEraseRange(val, start);
    if (!range || range.end <= start) {
      showToast(range?.sigFound ? "Cursor is already before signature" : "Nothing to erase after cursor", "info");
      return;
    }

    const end = range.end;
    const removedText = val.substring(start, end);
    const removedLines = removedText.split("\n").length;

    bodyTextarea.value = val.substring(0, start) + val.substring(end);
    bodyTextarea.selectionStart = bodyTextarea.selectionEnd = start;

    history.record(bodyTextarea.value, start, start, "erase", true);
    updateCursorPos();
    updateUndoRedoUI();

    if (range.sigFound) {
      showToast(`Erased ${removedLines} line${removedLines > 1 ? "s" : ""} before signature`, "info");
    } else {
      showToast(`Erased ${removedLines} line${removedLines > 1 ? "s" : ""} after cursor`, "info");
    }
  }

  insertAckedBtn?.addEventListener("click", () => insertTag("Acked-by"));
  insertReviewedBtn?.addEventListener("click", () => insertTag("Reviewed-by"));
  eraseAfterCursorBtn?.addEventListener("click", eraseLinesAfterCursor);

  // Key listener on modal for Alt+A, Alt+R, Ctrl+K, and Ctrl+Z / Ctrl+Y
  modalEl.addEventListener("keydown", (e) => {
    if (e.repeat) return;

    const isEditingMetaInput = e.target === toInput || e.target === ccInput || e.target === subjectInput;
    if (!isEditingMetaInput) {
      // Undo: Ctrl+Z / Cmd+Z
      if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && (e.key === "z" || e.key === "Z")) {
        e.preventDefault();
        e.stopPropagation();
        bodyTextarea.focus();
        performUndo();
        return;
      }
      // Redo: Ctrl+Y / Cmd+Y or Ctrl+Shift+Z / Cmd+Shift+Z
      if (
        ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && (e.key === "y" || e.key === "Y")) ||
        ((e.ctrlKey || e.metaKey) && !e.altKey && e.shiftKey && (e.key === "z" || e.key === "Z"))
      ) {
        e.preventDefault();
        e.stopPropagation();
        bodyTextarea.focus();
        performRedo();
        return;
      }
    }

    if ((e.ctrlKey || e.metaKey || e.altKey) && (e.key === "k" || e.key === "K")) {
      e.preventDefault();
      e.stopPropagation();
      eraseLinesAfterCursor();
      return;
    }
    if (e.altKey && !e.ctrlKey && !e.metaKey) {
      if (e.key === "a" || e.key === "A") {
        e.preventDefault();
        e.stopPropagation();
        insertTag("Acked-by");
      } else if (e.key === "r" || e.key === "R") {
        e.preventDefault();
        e.stopPropagation();
        insertTag("Reviewed-by");
      }
    }
  });

  let isComposing = false;
  bodyTextarea.addEventListener("compositionstart", () => {
    isComposing = true;
  });
  bodyTextarea.addEventListener("compositionend", () => {
    isComposing = false;
    history.record(bodyTextarea.value, bodyTextarea.selectionStart, bodyTextarea.selectionEnd, "input", true);
    updateUndoRedoUI();
  });

  bodyTextarea.addEventListener("paste", () => {
    history.breakGroup();
  });
  bodyTextarea.addEventListener("cut", () => {
    history.breakGroup();
  });

  // Keyboard shortcut listener on bodyTextarea
  bodyTextarea.addEventListener("keydown", (e) => {
    // Undo: Ctrl+Z / Cmd+Z (without Shift)
    if ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && (e.key === "z" || e.key === "Z")) {
      e.preventDefault();
      e.stopPropagation();
      performUndo();
      return;
    }
    // Redo: Ctrl+Y / Cmd+Y, or Ctrl+Shift+Z / Cmd+Shift+Z
    if (
      ((e.ctrlKey || e.metaKey) && !e.altKey && !e.shiftKey && (e.key === "y" || e.key === "Y")) ||
      ((e.ctrlKey || e.metaKey) && !e.altKey && e.shiftKey && (e.key === "z" || e.key === "Z"))
    ) {
      e.preventDefault();
      e.stopPropagation();
      performRedo();
      return;
    }

    if (e.key === "Tab") {
      e.preventDefault();
      const start = bodyTextarea.selectionStart;
      const end = bodyTextarea.selectionEnd;
      const val = bodyTextarea.value;
      bodyTextarea.value = val.substring(0, start) + "\t" + val.substring(end);
      const newPos = start + 1;
      bodyTextarea.selectionStart = bodyTextarea.selectionEnd = newPos;
      history.record(bodyTextarea.value, newPos, newPos, "tab", true);
      updateCursorPos();
      updateUndoRedoUI();
    } else if (e.key === "Enter") {
      if (e.ctrlKey || e.metaKey) {
        e.preventDefault();
        triggerSend();
      } else if (!e.altKey) {
        history.breakGroup();
      }
    } else if ((e.ctrlKey || e.metaKey || e.altKey) && (e.key === "k" || e.key === "K")) {
      e.preventDefault();
      e.stopPropagation();
      eraseLinesAfterCursor();
    }
  });

  // Track cursor position
  function updateCursorPos() {
    const text = bodyTextarea.value.substring(0, bodyTextarea.selectionStart);
    const lines = text.split("\n");
    const lineNo = lines.length;
    const colNo = lines[lines.length - 1].length + 1;
    posIndicator.innerText = `Ln ${lineNo}, Col ${colNo}`;
  }

  bodyTextarea.addEventListener("keyup", updateCursorPos);
  bodyTextarea.addEventListener("click", updateCursorPos);
  bodyTextarea.addEventListener("input", (e) => {
    if (isComposing) {
      updateCursorPos();
      return;
    }
    const isDelete = e.inputType ? e.inputType.startsWith("delete") : false;
    const isPaste = e.inputType === "insertFromPaste";
    history.record(
      bodyTextarea.value,
      bodyTextarea.selectionStart,
      bodyTextarea.selectionEnd,
      isDelete ? "delete" : "input",
      isPaste
    );

    if (
      !isDelete &&
      !isPaste &&
      (!e.inputType || e.inputType === "insertText") &&
      bodyTextarea.selectionStart === bodyTextarea.selectionEnd
    ) {
      const wrapped = autoWrapEditorLine(
        bodyTextarea.value,
        bodyTextarea.selectionStart,
        DEFAULT_WRAP_COLUMNS
      );
      if (wrapped) {
        bodyTextarea.value = wrapped.value;
        bodyTextarea.setSelectionRange(wrapped.cursor, wrapped.cursor);
        history.record(
          bodyTextarea.value,
          wrapped.cursor,
          wrapped.cursor,
          "wrap",
          true
        );
      }
    }

    updateCursorPos();
    updateUndoRedoUI();
  });

  // Event: Send Email
  async function triggerSend() {
    const toVal = toInput.value.trim();
    const subjectVal = subjectInput.value.trim();
    const bodyVal = bodyTextarea.value;
    const ccVal = ccInput.value.trim() || null;

    if (!toVal) {
      showToast("Please specify at least one recipient in 'To:'", "error");
      toInput.focus();
      return;
    }
    if (!subjectVal) {
      showToast("Please enter an email Subject", "error");
      subjectInput.focus();
      return;
    }
    if (!bodyVal.trim() && sessionAttachments.length === 0) {
      showToast("Email body cannot be empty", "error");
      bodyTextarea.focus();
      return;
    }

    sendBtn.disabled = true;
    sendBtn.innerText = "⏳ Sending...";

    try {
      await sendEmail({
        to_addr: toVal,
        cc_addr: ccVal,
        subject: subjectVal,
        body_text: bodyVal,
        in_reply_to: inReplyTo || null,
        references: references || null,
        orig_msg_id: origMsgId || null,
        draft_id: sessionDraftId,
        attachments: serializeAttachmentsForApi(sessionAttachments),
      });

      const replyTargetId = origMsgId || (inReplyTo ? inReplyTo.replace(/^<+|>+$/g, "").trim() : "");
      if (replyTargetId) {
        store.updateMessageReplied(replyTargetId, true);
      }
      if (sessionDraftId) {
        store.removeMessage(sessionDraftId);
      }

      showToast(`Email sent successfully to ${toVal}!`, "success");

      if (window.opener && !window.opener.closed) {
        try {
          window.opener.postMessage(
            {
              type: "UNDINE_MAIL_SENT",
              draftId: sessionDraftId,
              origMsgId: replyTargetId || null,
            },
            window.location.origin !== "null" ? window.location.origin : "*"
          );
        } catch {
          // ignore
        }
      }

      if (isStandalone) {
        savedSnapshot = getDraftSnapshot();
        setTimeout(() => {
          window.close();
        }, 500);
      } else {
        savedSnapshot = getDraftSnapshot();
        closeInternalEditor();

        // Refresh folder counts and message list
        try {
          const folders = await fetchFolders();
          store.setFolders(folders);
          const currentFolderId = store.getState().selectedFolderId;
          if (currentFolderId) {
            const msgs = await fetchMessages(currentFolderId);
            store.setMessages(msgs);
          }
        } catch {
          // ignore
        }
      }
    } catch (err) {
      showToast(`Send failed: ${err.message}`, "error");
      sendBtn.disabled = false;
      sendBtn.innerText = "📤 Send Email (Ctrl+Enter)";
    }
  }

  sendBtn.addEventListener("click", triggerSend);

  function getDraftSnapshot() {
    return JSON.stringify({
      to: toInput.value,
      cc: ccInput.value,
      subject: subjectInput.value,
      body: bodyTextarea.value,
      attachments: sessionAttachments.map((att) => ({
        filename: att.filename || "",
        content_type: att.content_type || "",
        size: Number(att.size) || 0,
        content_base64: att.content_base64 || "",
        source_message_id: att.source_message_id ?? null,
        source_attachment_index: att.source_attachment_index ?? null,
      })),
    });
  }

  let savedSnapshot = getDraftSnapshot();
  let isUnsavedPromptOpen = false;

  function isDraftDirty() {
    return getDraftSnapshot() !== savedSnapshot;
  }

  // Event: Save Draft (reusable by Save Draft button and Save & Close confirmation)
  async function performSaveDraft() {
    saveDraftBtn.disabled = true;
    saveDraftBtn.innerText = "⏳ Saving...";

    try {
      const res = await saveDraft({
        to_addr: toInput.value.trim(),
        cc_addr: ccInput.value.trim() || null,
        subject: subjectInput.value.trim(),
        body_text: bodyTextarea.value,
        in_reply_to: inReplyTo || null,
        references: references || null,
        draft_id: sessionDraftId,
        attachments: serializeAttachmentsForApi(sessionAttachments),
      });

      if (res && res.db_id) {
        sessionDraftId = res.db_id;
        if (Array.isArray(res.attachments)) {
          sessionAttachments = res.attachments.map((att, idx) => ({
            filename: att.filename || `attachment-${idx + 1}`,
            content_type: att.content_type || "",
            size: Number(att.size) || 0,
            content_base64: "",
            source_message_id: res.db_id,
            source_attachment_index: att.index != null ? att.index : idx,
          }));
          renderAttachmentsUI();
        }
      }

      savedSnapshot = getDraftSnapshot();
      showToast("Draft saved to 'Drafts' folder!", "success");
      saveDraftBtn.disabled = false;
      saveDraftBtn.innerText = "💾 Save Draft";

      if (window.opener && !window.opener.closed) {
        try {
          window.opener.postMessage(
            {
              type: "UNDINE_DRAFT_SAVED",
              draftId: sessionDraftId,
            },
            window.location.origin !== "null" ? window.location.origin : "*"
          );
        } catch {
          // ignore
        }
      }

      // Refresh folders and message list in this window
      try {
        const folders = await fetchFolders();
        store.setFolders(folders);
        const currentFolderId = store.getState().selectedFolderId;
        if (currentFolderId) {
          const msgs = await fetchMessages(currentFolderId);
          store.setMessages(msgs);
        }
      } catch {
        // Ignored if folders/store not loaded in standalone window
      }
      return true;
    } catch (err) {
      showToast(`Save draft failed: ${err.message}`, "error");
      saveDraftBtn.disabled = false;
      saveDraftBtn.innerText = "💾 Save Draft";
      return false;
    }
  }

  saveDraftBtn.addEventListener("click", performSaveDraft);

  // Event: Hand off to External Editor
  externalBtn.addEventListener("click", async () => {
    try {
      showToast("Launching external editor...", "info");
      await openEditor(
        origMsgId || "",
        notes,
        "",
        bodyTextarea.value,
        toInput.value.trim(),
        ccInput.value.trim() || null,
        subjectInput.value.trim()
      );
      savedSnapshot = getDraftSnapshot();
      if (isStandalone) {
        window.close();
      } else {
        closeInternalEditor();
      }
      showToast("Launched external editor", "success");
    } catch (err) {
      showToast(`External editor failed: ${err.message}`, "error");
    }
  });

  function forceCloseEditor() {
    savedSnapshot = getDraftSnapshot();
    if (isStandalone) {
      window.close();
    } else {
      closeInternalEditor();
    }
  }

  function showUnsavedClosePrompt() {
    if (isUnsavedPromptOpen) return;
    isUnsavedPromptOpen = true;

    const promptBackdrop = document.createElement("div");
    promptBackdrop.className = "composer-unsaved-backdrop";
    promptBackdrop.id = "composer-unsaved-backdrop";
    promptBackdrop.innerHTML = `
      <div class="composer-unsaved-dialog" role="dialog" aria-modal="true" aria-labelledby="composer-unsaved-title">
        <div class="composer-unsaved-header">
          <h4 id="composer-unsaved-title">Unsaved Draft Changes</h4>
        </div>
        <div class="composer-unsaved-body">
          This draft has unsaved modifications. Would you like to save it to Drafts before closing?
        </div>
        <div class="composer-unsaved-actions">
          <button type="button" class="composer-btn secondary" id="btn-unsaved-cancel">Cancel</button>
          <button type="button" class="composer-btn danger" id="btn-unsaved-discard">🗑️ Discard &amp; Close</button>
          <button type="button" class="composer-btn primary" id="btn-unsaved-save">💾 Save &amp; Close</button>
        </div>
      </div>
    `;

    modalEl.appendChild(promptBackdrop);

    const saveCloseBtn = promptBackdrop.querySelector("#btn-unsaved-save");
    const discardBtn = promptBackdrop.querySelector("#btn-unsaved-discard");
    const cancelBtn = promptBackdrop.querySelector("#btn-unsaved-cancel");

    const dismissPrompt = () => {
      isUnsavedPromptOpen = false;
      window.removeEventListener("keydown", promptKeyHandler, true);
      promptBackdrop.remove();
      bodyTextarea.focus();
    };

    const promptKeyHandler = (ev) => {
      if (ev.key === "Escape") {
        ev.preventDefault();
        ev.stopPropagation();
        dismissPrompt();
      }
    };
    window.addEventListener("keydown", promptKeyHandler, true);

    cancelBtn?.addEventListener("click", dismissPrompt);
    promptBackdrop.addEventListener("click", (ev) => {
      if (ev.target === promptBackdrop) {
        dismissPrompt();
      }
    });

    discardBtn?.addEventListener("click", () => {
      isUnsavedPromptOpen = false;
      window.removeEventListener("keydown", promptKeyHandler, true);
      promptBackdrop.remove();
      forceCloseEditor();
    });

    saveCloseBtn?.addEventListener("click", async () => {
      saveCloseBtn.disabled = true;
      discardBtn.disabled = true;
      cancelBtn.disabled = true;
      saveCloseBtn.innerText = "⏳ Saving...";
      const ok = await performSaveDraft();
      isUnsavedPromptOpen = false;
      window.removeEventListener("keydown", promptKeyHandler, true);
      promptBackdrop.remove();
      if (ok) {
        forceCloseEditor();
      } else {
        bodyTextarea.focus();
      }
    });

    saveCloseBtn?.focus();
  }

  // Close handlers (guard against losing unsaved changes on Esc or Close click)
  const handleEditorClose = () => {
    if (isDraftDirty()) {
      showUnsavedClosePrompt();
      return;
    }
    forceCloseEditor();
  };

  closeBtn.addEventListener("click", handleEditorClose);

  // Global key listener for Esc inside modal/standalone
  const escHandler = (e) => {
    if (e.key === "Escape") {
      if (isUnsavedPromptOpen) {
        return;
      }
      e.preventDefault();
      handleEditorClose();
    }
  };
  window.addEventListener("keydown", escHandler);
  modalEl._escHandler = escHandler;

  if (isStandalone) {
    const beforeUnloadHandler = (e) => {
      if (isDraftDirty()) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", beforeUnloadHandler);
    modalEl._beforeUnloadHandler = beforeUnloadHandler;
  }
}

export function closeInternalEditor() {
  if (activeModal) {
    if (activeModal._escHandler) {
      window.removeEventListener("keydown", activeModal._escHandler);
    }
    if (activeModal._beforeUnloadHandler) {
      window.removeEventListener("beforeunload", activeModal._beforeUnloadHandler);
    }
    activeModal.remove();
    activeModal = null;
  }
}

/**
 * Parse a comma-separated email recipient list, respecting quoted strings and angle brackets.
 */
export function parseRecipients(headerStr) {
  if (!headerStr || typeof headerStr !== "string") return [];
  const entries = [];
  let current = "";
  let inQuotes = false;
  let inAngle = false;

  for (let i = 0; i < headerStr.length; i++) {
    const char = headerStr[i];
    if (char === '"' && !inAngle) {
      inQuotes = !inQuotes;
      current += char;
    } else if (char === "<" && !inQuotes) {
      inAngle = true;
      current += char;
    } else if (char === ">" && !inQuotes) {
      inAngle = false;
      current += char;
    } else if (char === "," && !inQuotes && !inAngle) {
      const trimmed = current.trim();
      if (trimmed) entries.push(trimmed);
      current = "";
    } else {
      current += char;
    }
  }
  const lastTrimmed = current.trim();
  if (lastTrimmed) entries.push(lastTrimmed);
  return entries;
}

/**
 * Extract bare email address from an RFC822 address token (e.g. "Name <addr@dom.com>" -> "addr@dom.com").
 */
export function extractEmailAddress(entry) {
  if (!entry || typeof entry !== "string") return "";
  const match = entry.match(/<([^>]+)>/);
  if (match) {
    return match[1].trim().toLowerCase();
  }
  return entry.trim().toLowerCase();
}

/**
 * Build To and Cc recipient strings for replying to an LKML message.
 *
 * - To: The author of the original message (from_name <from_addr> or from_addr).
 * - Cc: All other recipients from To and Cc headers, excluding the author,
 *       current user email, and stable@vger.kernel.org / stable@kernel.org.
 */
export function buildReplyRecipients(msg, userEmail = "") {
  if (!msg) return { to: "", cc: "" };

  const fromName = (msg.from_name || "").trim();
  const fromAddr = (msg.from_addr || "").trim();
  const cleanSender = extractEmailAddress(fromAddr);

  let toAddr = "";
  if (fromName && fromAddr) {
    toAddr = `${fromName} <${fromAddr}>`;
  } else {
    toAddr = fromAddr || fromName;
  }

  const cleanUser = extractEmailAddress(userEmail);
  const excluded = new Set([
    cleanSender,
    cleanUser,
    "stable@vger.kernel.org",
    "stable@kernel.org",
  ]);
  excluded.delete("");

  const headerTo = msg.to_addr || "";
  const headerCc = msg.cc_addr || "";

  const allEntries = [
    ...parseRecipients(headerTo),
    ...parseRecipients(headerCc),
  ];

  const seen = new Set();
  const ccParts = [];

  for (const entry of allEntries) {
    const email = extractEmailAddress(entry);
    if (!email || excluded.has(email) || seen.has(email)) {
      continue;
    }
    seen.add(email);
    ccParts.push(entry);
  }

  return {
    to: toAddr,
    cc: ccParts.join(", "),
  };
}

export const DEFAULT_WRAP_COLUMNS = 75;

function isWideCodePoint(cp) {
  return (
    (cp >= 0x1100 && cp <= 0x115f) ||
    (cp >= 0x2e80 && cp <= 0xa4cf && cp !== 0x303f) ||
    (cp >= 0xac00 && cp <= 0xd7a3) ||
    (cp >= 0xf900 && cp <= 0xfaff) ||
    (cp >= 0xfe10 && cp <= 0xfe6f) ||
    (cp >= 0xff00 && cp <= 0xff60) ||
    (cp >= 0xffe0 && cp <= 0xffe6) ||
    (cp >= 0x1f300 && cp <= 0x1f9ff) ||
    (cp >= 0x20000 && cp <= 0x3fffd)
  );
}

function charWidth(ch, col) {
  if (ch === "\t") {
    return 8 - (col % 8);
  }
  const cp = ch.codePointAt(0);
  return isWideCodePoint(cp) ? 2 : 1;
}

export function visualWidth(str) {
  let col = 0;
  for (const ch of str) {
    col += charWidth(ch, col);
  }
  return col;
}

const URL_SCHEME_PATTERN = `(?:(?:https?|ftps?|git|ssh|ircs?|nntp|news):\\/\\/|mailto:)`;

const NO_WRAP_TAG_LINE_RE =
  /^(?:[A-Za-z][A-Za-z0-9-]*-by|Fixes|Link|Closes|Resolves|BugLink|Bug|Cc|To|From|Subject|Date|Message-Id|In-Reply-To|References|Change-Id|Depends-on|Reverts|See-also|URL|Lore|Commit|Author|CommitDate|AuthorDate|base-commit|prerequisite-patch-id|prerequisite-message-id):(?:\s|$)/i;

const NO_WRAP_URL_LINE_RE = new RegExp(
  `^(?:(?:\\[[^\\]]+\\]:?|[-*•]|\\d+[.)])\\s+)?[<(\\["'（]?${URL_SCHEME_PATTERN}\\S+$`,
  "i"
);

const URL_TOKEN_RE = new RegExp(`(?:^|[^A-Za-z0-9_])${URL_SCHEME_PATTERN}\\S+`, "i");

export function isNoWrapTagLine(trimmed) {
  return Boolean(trimmed) && NO_WRAP_TAG_LINE_RE.test(trimmed);
}

export function isNoWrapUrlLine(trimmed) {
  return Boolean(trimmed) && NO_WRAP_URL_LINE_RE.test(trimmed);
}

function isUrlToken(w) {
  return Boolean(w) && URL_TOKEN_RE.test(w);
}

/**
 * Wrap lines in a review comment around maxCols columns (default 75).
 * Quoted lines (">"), fenced code blocks ("```"), tab-indented lines,
 * kernel/mail trailer tags (e.g. "Fixes:", "Link:", "Closes:"), and URL lines are preserved.
 */
export function wrapCommentText(text, maxCols = DEFAULT_WRAP_COLUMNS) {
  if (!text || typeof text !== "string") return text || "";
  const limit = maxCols > 0 ? maxCols : DEFAULT_WRAP_COLUMNS;
  const rawLines = text.replace(/\r\n/g, "\n").split("\n");
  const outLines = [];
  let inCodeFence = false;

  for (const line of rawLines) {
    const trimmed = line.trim();
    if (trimmed.startsWith("```")) {
      inCodeFence = !inCodeFence;
      outLines.push(line);
      continue;
    }
    if (inCodeFence || trimmed === "" || visualWidth(line) <= limit) {
      outLines.push(line);
      continue;
    }
    if (
      trimmed.startsWith(">") ||
      line.startsWith("\t") ||
      isNoWrapTagLine(trimmed) ||
      isNoWrapUrlLine(trimmed)
    ) {
      outLines.push(line);
      continue;
    }

    let indentLen = 0;
    while (indentLen < line.length && line[indentLen] === " ") {
      indentLen++;
    }
    const indent = indentLen <= 8 ? line.slice(0, indentLen) : "";
    const indentWidth = visualWidth(indent);

    const words = line.slice(indentLen).trim().split(/\s+/).filter(Boolean);
    if (words.length === 0) {
      outLines.push(line);
      continue;
    }

    let curLine = "";
    let curWidth = 0;

    for (const word of words) {
      const wordWidth = visualWidth(word);
      if (indentWidth + wordWidth <= limit) {
        if (curLine === "") {
          curLine = indent + word;
          curWidth = indentWidth + wordWidth;
        } else if (curWidth + 1 + wordWidth <= limit) {
          curLine += " " + word;
          curWidth += 1 + wordWidth;
        } else {
          outLines.push(curLine);
          curLine = indent + word;
          curWidth = indentWidth + wordWidth;
        }
      } else if (isUrlToken(word)) {
        if (curLine !== "") {
          outLines.push(curLine);
          curLine = "";
          curWidth = 0;
        }
        outLines.push(indent + word);
      } else {
        if (curLine !== "") {
          outLines.push(curLine);
          curLine = "";
          curWidth = 0;
        }
        let chunk = indent;
        let chunkWidth = indentWidth;
        for (const ch of word) {
          let cw = charWidth(ch, chunkWidth);
          if (chunkWidth + cw > limit && chunk !== indent) {
            outLines.push(chunk);
            chunk = indent;
            chunkWidth = indentWidth;
            cw = charWidth(ch, chunkWidth);
          }
          chunk += ch;
          chunkWidth += cw;
        }
        if (chunk !== indent) {
          curLine = chunk;
          curWidth = chunkWidth;
        }
      }
    }

    if (curLine !== "") {
      outLines.push(curLine);
    }
  }

  return outLines.join("\n");
}

/**
 * Wrap unquoted comment blocks inside a composed reply body around maxCols columns,
 * while preserving the attribution line, quoted lines (">"), and signature ("-- ").
 */
export function wrapReplyBodyComments(body, maxCols = DEFAULT_WRAP_COLUMNS) {
  if (!body || typeof body !== "string") return body || "";
  const sigMatch = /(?:^|\n)--[ \t]*(?:\r?\n|$)/g;
  let match;
  let sigStart = -1;
  while ((match = sigMatch.exec(body)) !== null) {
    sigStart = match.index + (match[0].startsWith("\n") ? 1 : 0);
  }

  const mainPart = sigStart !== -1 ? body.slice(0, sigStart) : body;
  const sigPart = sigStart !== -1 ? body.slice(sigStart) : "";

  const lines = mainPart.replace(/\r\n/g, "\n").split("\n");
  const out = [];
  let inCodeFence = false;

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();
    if (trimmed.startsWith("```")) {
      inCodeFence = !inCodeFence;
      out.push(line);
      continue;
    }
    if (
      inCodeFence ||
      (i === 0 && /^On .+ wrote:$/.test(trimmed)) ||
      trimmed === "" ||
      trimmed.startsWith(">")
    ) {
      out.push(line);
      continue;
    }
    out.push(wrapCommentText(line, maxCols));
  }

  return out.join("\n") + sigPart;
}

/**
 * Automatically wrap the current line around maxCols columns while typing in the editor.
 * Returns { value, cursor } if the line was wrapped, or null if no wrap was needed.
 */
export function autoWrapEditorLine(value, cursorOffset, maxCols = DEFAULT_WRAP_COLUMNS) {
  if (!value || typeof value !== "string" || cursorOffset == null) return null;
  const limit = maxCols > 0 ? maxCols : DEFAULT_WRAP_COLUMNS;

  const lineStart = cursorOffset <= 0 ? 0 : value.lastIndexOf("\n", cursorOffset - 1) + 1;
  const nextNl = value.indexOf("\n", cursorOffset);
  const lineEnd = nextNl === -1 ? value.length : nextNl;
  const line = value.slice(lineStart, lineEnd);

  const trimmed = line.trim();
  if (
    trimmed === "" ||
    trimmed.startsWith(">") ||
    trimmed.startsWith("```") ||
    line.startsWith("\t") ||
    line.startsWith("-- ") ||
    line === "--" ||
    /^On .+ wrote:$/.test(trimmed) ||
    isNoWrapTagLine(trimmed) ||
    isNoWrapUrlLine(trimmed) ||
    visualWidth(line) <= limit
  ) {
    return null;
  }

  let indentLen = 0;
  while (indentLen < line.length && line[indentLen] === " ") {
    indentLen++;
  }
  const indent = indentLen <= 8 ? line.slice(0, indentLen) : "";

  let col = 0;
  let idx = 0;
  let lastSpaceIdx = -1;
  for (const ch of line) {
    const cw = charWidth(ch, col);
    if (idx >= indentLen && ch === " " && col <= limit) {
      lastSpaceIdx = idx;
    }
    col += cw;
    idx += ch.length;
  }

  const relCursor = cursorOffset - lineStart;

  if (lastSpaceIdx !== -1) {
    let tokenStart = lastSpaceIdx + 1;
    while (tokenStart < line.length && line[tokenStart] === " ") {
      tokenStart++;
    }
    const nextSpaceIdx = line.indexOf(" ", tokenStart);
    const crossingToken =
      nextSpaceIdx === -1 ? line.slice(tokenStart) : line.slice(tokenStart, nextSpaceIdx);

    if (isUrlToken(crossingToken)) {
      if (nextSpaceIdx !== -1 && line.slice(nextSpaceIdx + 1).trim() !== "") {
        const newLine =
          line.slice(0, nextSpaceIdx) + "\n" + indent + line.slice(nextSpaceIdx + 1);
        const newValue = value.slice(0, lineStart) + newLine + value.slice(lineEnd);
        const newCursor = relCursor > nextSpaceIdx ? cursorOffset + indent.length : cursorOffset;
        return { value: newValue, cursor: newCursor };
      }
      return null;
    }

    const newLine = line.slice(0, lastSpaceIdx) + "\n" + indent + line.slice(lastSpaceIdx + 1);
    const newValue = value.slice(0, lineStart) + newLine + value.slice(lineEnd);
    const newCursor = relCursor > lastSpaceIdx ? cursorOffset + indent.length : cursorOffset;
    return { value: newValue, cursor: newCursor };
  }

  const firstSpaceAfterIndent = line.indexOf(" ", indentLen);
  const firstToken =
    firstSpaceAfterIndent === -1
      ? line.slice(indentLen)
      : line.slice(indentLen, firstSpaceAfterIndent);
  if (isUrlToken(firstToken)) {
    if (firstSpaceAfterIndent !== -1 && line.slice(firstSpaceAfterIndent + 1).trim() !== "") {
      const newLine =
        line.slice(0, firstSpaceAfterIndent) +
        "\n" +
        indent +
        line.slice(firstSpaceAfterIndent + 1);
      const newValue = value.slice(0, lineStart) + newLine + value.slice(lineEnd);
      const newCursor =
        relCursor > firstSpaceAfterIndent ? cursorOffset + indent.length : cursorOffset;
      return { value: newValue, cursor: newCursor };
    }
    return null;
  }

  col = 0;
  idx = 0;
  let splitIdx = -1;
  for (const ch of line) {
    const cw = charWidth(ch, col);
    if (col + cw > limit && idx > indentLen) {
      splitIdx = idx;
      break;
    }
    col += cw;
    idx += ch.length;
  }

  if (splitIdx === -1) {
    return null;
  }

  const newLine = line.slice(0, splitIdx) + "\n" + indent + line.slice(splitIdx);
  const newValue = value.slice(0, lineStart) + newLine + value.slice(lineEnd);
  const newCursor = relCursor >= splitIdx ? cursorOffset + 1 + indent.length : cursorOffset;
  return { value: newValue, cursor: newCursor };
}

/**
 * Open the internal editor pre-populated with quote from the active message.
 */
export async function openReplyForMessage(msg, notes = []) {
  if (!msg) return;

  const rawNotesList = notes && notes.length > 0 ? notes : store.getNotesList();
  const notesList = rawNotesList.map((n) => ({
    ...n,
    text: wrapCommentText(n.text, DEFAULT_WRAP_COLUMNS),
  }));

  // Determine recipients from mail headers
  const cfg = store.getState().config;
  const userEmail = cfg?.from_email || cfg?.smtp?.from_email || "";
  const { to: toAddr, cc: ccAddr } = buildReplyRecipients(msg, userEmail);

  // Determine subject
  const cleanSubj = (msg.subject || "").trim();
  const subject = cleanSubj.toLowerCase().startsWith("re:") ? cleanSubj : `Re: ${cleanSubj}`;

  // Build In-Reply-To and References
  const inReplyTo = msg.message_id ? `<${msg.message_id}>` : "";
  const refs = [msg.references_header, msg.message_id ? `<${msg.message_id}>` : ""]
    .filter(Boolean)
    .join(" ");

  // Default body with review quote
  let quoteBody = "";
  try {
    const targetId = msg.id != null ? String(msg.id) : msg.message_id;
    const quoteRes = await previewQuote(targetId, notesList, "");
    if (quoteRes && quoteRes.quote_text) {
      quoteBody = wrapReplyBodyComments(quoteRes.quote_text, DEFAULT_WRAP_COLUMNS);
    }
  } catch (e) {
    console.warn("Could not preview quote:", e);
  }

  // Fallback quote if preview was empty or failed
  if (!quoteBody || !quoteBody.trim()) {
    const author = msg.from_name ? `${msg.from_name} <${msg.from_addr}>` : (msg.from_addr || "");
    const dateStr = msg.date || "recently";
    const quoted = (msg.body_plain || "").split("\n").map((l) => (l ? `> ${l}` : ">")).join("\n");
    quoteBody = `On ${dateStr}, ${author} wrote:\n${quoted}\n\n`;
  }

  openInternalEditor({
    to: toAddr,
    cc: ccAddr,
    subject: subject,
    body: quoteBody,
    inReplyTo: inReplyTo,
    references: refs,
    origMsgId: msg.message_id,
    notes: notesList,
  });
}

/**
 * Open the internal editor pre-populated with forward template from the active message.
 */
export async function openForwardForMessage(msg) {
  if (!msg) return;

  const cleanSubj = (msg.subject || "").trim();
  const subject = cleanSubj.toLowerCase().startsWith("fwd:") ? cleanSubj : `Fwd: ${cleanSubj}`;

  const fromStr = msg.from_name ? `${msg.from_name} <${msg.from_addr}>` : (msg.from_addr || "");
  const toStr = msg.to_addr || "";
  const ccStr = msg.cc_addr || "";
  const dateStr = msg.date || "";
  const subjStr = msg.subject || "";

  let fwdHeader = `\n---------- Forwarded message ---------\n` +
    `From: ${fromStr}\n` +
    `Date: ${dateStr}\n` +
    `Subject: ${subjStr}\n` +
    `To: ${toStr}\n` +
    (ccStr ? `Cc: ${ccStr}\n` : "") +
    `\n`;

  const body = fwdHeader + (msg.body_plain || "");
  const initialAttachments = (msg.attachments || []).map((att, idx) => ({
    filename: att.filename || `attachment-${idx + 1}`,
    content_type: att.content_type || "",
    size: Number(att.size) || 0,
    content_base64: "",
    source_message_id: msg.id != null ? msg.id : null,
    source_attachment_index: att.index != null ? att.index : idx,
  }));

  openInternalEditor({
    to: "",
    cc: "",
    subject: subject,
    body: body,
    inReplyTo: "",
    references: "",
    origMsgId: "",
    notes: [],
    attachments: initialAttachments,
  });
}

/**
 * Open the internal editor pre-populated with draft message for re-editing.
 */
export function openEditForMessage(msg) {
  if (!msg) return;

  const toAddr = msg.to_addr || "";
  const ccAddr = msg.cc_addr || "";
  const subject = msg.subject || "";
  const body = msg.body_plain || "";
  const inReplyTo = msg.in_reply_to ? (msg.in_reply_to.startsWith("<") ? msg.in_reply_to : `<${msg.in_reply_to}>`) : "";
  const refs = msg.references_header || "";
  const parentMsgId = msg.in_reply_to ? msg.in_reply_to.replace(/^<+|>+$/g, "").trim() : "";
  const initialAttachments = (msg.attachments || []).map((att, idx) => ({
    filename: att.filename || `attachment-${idx + 1}`,
    content_type: att.content_type || "",
    size: Number(att.size) || 0,
    content_base64: "",
    source_message_id: msg.id != null ? msg.id : null,
    source_attachment_index: att.index != null ? att.index : idx,
  }));

  openInternalEditor({
    to: toAddr,
    cc: ccAddr,
    subject: subject,
    body: body,
    inReplyTo: inReplyTo,
    references: refs,
    origMsgId: parentMsgId,
    draftId: msg.id != null ? msg.id : null,
    notes: [],
    attachments: initialAttachments,
    title: "Edit Draft (Internal Editor)",
    titleIcon: "✏️",
  });
}

/**
 * Open the internal editor for composing a new message from scratch.
 */
export function openNewMessage({ to = "", cc = "", subject = "", body = "" } = {}) {
  const cfg = store.getState().config;
  let defaultBody = body;
  if (!defaultBody) {
    if (cfg?.signature) {
      defaultBody = `\n\n-- \n${cfg.signature}\n`;
    } else {
      defaultBody = "";
    }
  }

  openInternalEditor({
    to,
    cc,
    subject,
    body: defaultBody,
    inReplyTo: "",
    references: "",
    origMsgId: "",
    notes: [],
    title: "New Message (Internal Editor)",
    titleIcon: "✏️",
  });
}

/**
 * Calculate the erase range after cursor while preserving the signature ("-- " line).
 * Returns { end: number, sigFound: boolean } or null if nothing to erase.
 */
export function findEraseRange(val, start) {
  if (start >= val.length) return null;

  // Search for signature separator line "-- " (RFC 3676 standard, or "--")
  const regex = /(?:^|\n)--[ \t]*(?:\r?\n|$)/g;
  let match;
  let targetSigIndex = -1;

  while ((match = regex.exec(val)) !== null) {
    const lineStart = match.index + (match[0].startsWith("\n") ? 1 : 0);
    if (lineStart >= start) {
      targetSigIndex = lineStart;
    }
  }

  if (targetSigIndex === -1) {
    return { end: val.length, sigFound: false };
  }

  if (targetSigIndex === start) {
    return { end: start, sigFound: true };
  }

  // Count newlines before signature line
  let newlinesBeforeSig = 0;
  let p = targetSigIndex - 1;
  while (p >= start && (val[p] === "\n" || val[p] === "\r")) {
    if (val[p] === "\n") newlinesBeforeSig++;
    p--;
  }

  // Count newlines before cursor
  let newlinesBeforeCursor = 0;
  p = start - 1;
  while (p >= 0 && (val[p] === "\n" || val[p] === "\r")) {
    if (val[p] === "\n") newlinesBeforeCursor++;
    p--;
  }

  // If start === 0, keep no leading newlines; otherwise preserve up to 2 newlines (one empty line)
  const desiredNewlines = start === 0 ? 0 : Math.min(2, Math.max(1, newlinesBeforeSig));
  const neededFromSig = Math.max(0, desiredNewlines - newlinesBeforeCursor);

  let preservedNewlines = 0;
  let end = targetSigIndex;
  while (end > start && preservedNewlines < neededFromSig) {
    if (val[end - 1] === "\n") {
      preservedNewlines++;
      end--;
    } else if (val[end - 1] === "\r") {
      end--;
    } else {
      break;
    }
  }

  return { end, sigFound: true };
}

function escapeHtml(str) {
  if (str == null || str === "") return "";
  return String(str).replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}

function escapeAttr(str) {
  return escapeHtml(str);
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

function setupAddressAutocomplete(inputEl, dropdownEl) {
  let activeIndex = -1;
  let items = [];
  let debounceTimer = null;

  function getCurrentToken() {
    const val = inputEl.value;
    const cursorPos = inputEl.selectionStart ?? val.length;
    // Find segment around cursor separated by comma
    const beforeCursor = val.substring(0, cursorPos);
    const lastComma = beforeCursor.lastIndexOf(",");
    const start = lastComma === -1 ? 0 : lastComma + 1;
    const afterCursor = val.substring(cursorPos);
    const nextComma = afterCursor.indexOf(",");
    const end = nextComma === -1 ? val.length : cursorPos + nextComma;

    const token = val.substring(start, end).trim();
    return { token, start, end };
  }

  function hideDropdown() {
    dropdownEl.style.display = "none";
    dropdownEl.innerHTML = "";
    items = [];
    activeIndex = -1;
  }

  function renderDropdown(addresses) {
    if (!addresses || addresses.length === 0) {
      hideDropdown();
      return;
    }
    items = addresses;
    activeIndex = 0;
    dropdownEl.innerHTML = addresses
      .map(
        (a, idx) => `
        <div class="composer-autocomplete-item ${idx === 0 ? "active" : ""}" data-idx="${idx}">
          <span class="comp-ac-name">${escapeHtml(a.name || a.address)}</span>
          ${a.name ? `<span class="comp-ac-addr">&lt;${escapeHtml(a.address)}&gt;</span>` : ""}
          <span class="comp-ac-freq" title="Used in ${a.frequency} email(s)">${a.frequency}x</span>
        </div>
      `
      )
      .join("");
    dropdownEl.style.display = "block";

    dropdownEl.querySelectorAll(".composer-autocomplete-item").forEach((el) => {
      el.addEventListener("mousedown", (e) => {
        e.preventDefault(); // prevent input blur
        const idx = parseInt(el.getAttribute("data-idx"), 10);
        selectItem(items[idx]);
      });
    });
  }

  function selectItem(item) {
    if (!item) return;
    const formatted = item.name ? `"${item.name}" <${item.address}>` : item.address;
    const { start, end } = getCurrentToken();
    const val = inputEl.value;

    const before = val.substring(0, start).trim();
    const after = val.substring(end).trim();

    let newVal = "";
    if (before.length > 0) {
      newVal = before.endsWith(",") ? `${before} ${formatted}` : `${before}, ${formatted}`;
    } else {
      newVal = formatted;
    }

    if (after.length > 0) {
      newVal = after.startsWith(",") ? `${newVal}${after}` : `${newVal}, ${after}`;
    }

    inputEl.value = newVal;
    const newPos = (before.length > 0 ? (before.endsWith(",") ? before.length + 1 : before.length + 2) : 0) + formatted.length;
    inputEl.setSelectionRange(newPos, newPos);
    hideDropdown();
    inputEl.focus();
  }

  inputEl.addEventListener("input", () => {
    clearTimeout(debounceTimer);
    const { token } = getCurrentToken();
    const cleanToken = token.replace(/^[<"']+|[>"']+$/g, "").trim();
    if (cleanToken.length === 0) {
      hideDropdown();
      return;
    }

    debounceTimer = setTimeout(async () => {
      try {
        const results = await fetchKnownAddresses(cleanToken, 10);
        renderDropdown(results);
      } catch {
        hideDropdown();
      }
    }, 150);
  });

  inputEl.addEventListener("keydown", (e) => {
    if (dropdownEl.style.display === "none") return;

    if (e.key === "ArrowDown") {
      e.preventDefault();
      if (items.length > 0) {
        activeIndex = (activeIndex + 1) % items.length;
        updateActiveItem();
      }
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      if (items.length > 0) {
        activeIndex = (activeIndex - 1 + items.length) % items.length;
        updateActiveItem();
      }
    } else if (e.key === "Enter" || e.key === "Tab") {
      if (activeIndex >= 0 && activeIndex < items.length) {
        e.preventDefault();
        selectItem(items[activeIndex]);
      }
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      hideDropdown();
    }
  });

  function updateActiveItem() {
    dropdownEl.querySelectorAll(".composer-autocomplete-item").forEach((el, idx) => {
      if (idx === activeIndex) {
        el.classList.add("active");
        el.scrollIntoView({ block: "nearest" });
      } else {
        el.classList.remove("active");
      }
    });
  }

  inputEl.addEventListener("blur", () => {
    setTimeout(hideDropdown, 200);
  });
}

