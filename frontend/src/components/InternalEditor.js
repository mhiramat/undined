/**
 * InternalEditor: In-browser plaintext email composer and review response editor
 * with strict 8-bit plain-text formatting, Tab-key preservation (tab-size: 8),
 * and direct SMTP dispatch / Drafts saving.
 */

import { fetchConfig, fetchFolders, fetchKnownAddresses, fetchMessages, openEditor, previewQuote, saveDraft, sendEmail } from "../api.js";
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
    showToast("Pop-up window was blocked. Please allow pop-ups for UndineD.", "warning");
    return false;
  }
  if (w.focus) {
    w.focus();
  }
  return true;
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
          <span class="composer-badge">8-bit plain-text (UTF-8)</span>
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

      <!-- Quick Action Toolbar for Review Tags -->
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
          <span class="composer-hint">Alt+A: Acked-by · Alt+R: Reviewed-by · Ctrl+K: Erase after cursor · Ctrl+Enter: Send</span>
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

    let inserted = false;
    try {
      inserted = document.execCommand("insertText", false, textToInsert);
    } catch {
      inserted = false;
    }

    if (!inserted) {
      bodyTextarea.value = before + textToInsert + after;
      const newPos = start + textToInsert.length;
      bodyTextarea.selectionStart = bodyTextarea.selectionEnd = newPos;
    }

    updateCursorPos();
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

    // Use selection + execCommand to preserve browser undo history (Ctrl+Z) per Rule 3
    bodyTextarea.selectionStart = start;
    bodyTextarea.selectionEnd = end;

    let success = false;
    try {
      success = document.execCommand("delete", false, null);
    } catch {
      success = false;
    }

    if (!success) {
      bodyTextarea.value = val.substring(0, start) + val.substring(end);
      bodyTextarea.selectionStart = bodyTextarea.selectionEnd = start;
    }

    updateCursorPos();
    if (range.sigFound) {
      showToast(`Erased ${removedLines} line${removedLines > 1 ? "s" : ""} before signature`, "info");
    } else {
      showToast(`Erased ${removedLines} line${removedLines > 1 ? "s" : ""} after cursor`, "info");
    }
  }

  insertAckedBtn?.addEventListener("click", () => insertTag("Acked-by"));
  insertReviewedBtn?.addEventListener("click", () => insertTag("Reviewed-by"));
  eraseAfterCursorBtn?.addEventListener("click", eraseLinesAfterCursor);

  // Key listener on modal for Alt+A, Alt+R, and Ctrl+K / Alt+K
  modalEl.addEventListener("keydown", (e) => {
    if (e.repeat) return;
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

  // Tab key preservation in textarea (insert literal \t without losing focus)
  bodyTextarea.addEventListener("keydown", (e) => {
    if (e.key === "Tab") {
      e.preventDefault();
      const start = bodyTextarea.selectionStart;
      const end = bodyTextarea.selectionEnd;
      bodyTextarea.value = bodyTextarea.value.substring(0, start) + "\t" + bodyTextarea.value.substring(end);
      bodyTextarea.selectionStart = bodyTextarea.selectionEnd = start + 1;
      updateCursorPos();
    } else if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      triggerSend();
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
  bodyTextarea.addEventListener("input", updateCursorPos);

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
    if (!bodyVal.trim()) {
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
      });

      if (origMsgId) {
        store.updateMessageReplied(origMsgId, true);
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
              origMsgId: origMsgId || null,
            },
            window.location.origin !== "null" ? window.location.origin : "*"
          );
        } catch {
          // ignore
        }
      }

      if (isStandalone) {
        setTimeout(() => {
          window.close();
        }, 500);
      } else {
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

  // Event: Save Draft
  saveDraftBtn.addEventListener("click", async () => {
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
      });

      if (res && res.db_id) {
        sessionDraftId = res.db_id;
      }

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
    } catch (err) {
      showToast(`Save draft failed: ${err.message}`, "error");
      saveDraftBtn.disabled = false;
      saveDraftBtn.innerText = "💾 Save Draft";
    }
  });

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

  // Close handlers
  const handleEditorClose = () => {
    if (isStandalone) {
      window.close();
    } else {
      closeInternalEditor();
    }
  };

  closeBtn.addEventListener("click", handleEditorClose);

  // Global key listener for Esc inside modal/standalone
  const escHandler = (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      handleEditorClose();
    }
  };
  window.addEventListener("keydown", escHandler);
  modalEl._escHandler = escHandler;
  modalEl._escHandler = escHandler;
}

export function closeInternalEditor() {
  if (activeModal) {
    if (activeModal._escHandler) {
      window.removeEventListener("keydown", activeModal._escHandler);
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

/**
 * Open the internal editor pre-populated with quote from the active message.
 */
export async function openReplyForMessage(msg, notes = []) {
  if (!msg) return;

  const notesList = notes && notes.length > 0 ? notes : store.getNotesList();

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
      quoteBody = quoteRes.quote_text;
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

  openInternalEditor({
    to: "",
    cc: "",
    subject: subject,
    body: body,
    inReplyTo: "",
    references: "",
    origMsgId: msg.message_id,
    notes: [],
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

  openInternalEditor({
    to: toAddr,
    cc: ccAddr,
    subject: subject,
    body: body,
    inReplyTo: inReplyTo,
    references: refs,
    origMsgId: msg.message_id || (msg.id != null ? String(msg.id) : ""),
    draftId: msg.id != null ? msg.id : null,
    notes: [],
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
  if (!str) return "";
  return str.replace(/[&<>"']/g, (m) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[m]));
}

function escapeAttr(str) {
  if (!str) return "";
  return str.replace(/"/g, "&quot;");
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

