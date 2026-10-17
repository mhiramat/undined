/**
 * NoteBubble: Inline review annotation popup mounted beneath target code lines.
 */

import { store } from "../store.js";

export function mountNoteBubble(targetRowEl, file, lineNo, side = "new", initialText = "", onDone = null) {
  // Remove existing bubble on this row if already open
  const nextEl = targetRowEl.nextElementSibling;
  if (nextEl && nextEl.classList.contains("note-bubble-row")) {
    nextEl.remove();
    return;
  }

  const colSpan = targetRowEl.querySelectorAll("td").length || 4;

  const bubbleRow = document.createElement("tr");
  bubbleRow.className = "note-bubble-row";

  bubbleRow.innerHTML = `
    <td colspan="${colSpan}">
      <div class="note-bubble-container">
        <div class="note-bubble-header">
          <span>Review note for <strong>${escapeHtml(file)}:${lineNo}</strong></span>
          <span style="font-size: 10px; color: #94a3b8;">Ctrl+Enter to save, Esc to cancel</span>
        </div>
        <textarea class="note-textarea" placeholder="Add review comment...">${escapeHtml(initialText)}</textarea>
        <div class="note-actions">
          <button type="button" class="btn-note-discard">Discard</button>
          <button type="button" class="btn-note-cancel">Cancel</button>
          <button type="button" class="btn-note-save">Save Note</button>
        </div>
      </div>
    </td>
  `;

  targetRowEl.parentNode.insertBefore(bubbleRow, targetRowEl.nextSibling);

  const textarea = bubbleRow.querySelector(".note-textarea");
  textarea.focus();
  if (initialText) {
    textarea.setSelectionRange(initialText.length, initialText.length);
  }

  function commit() {
    const val = textarea.value;
    store.setNote(file, lineNo, val, side);
    bubbleRow.remove();
    if (onDone) onDone(true, val);
  }

  function cancel() {
    bubbleRow.remove();
    if (onDone) onDone(false, null);
  }

  function discard() {
    store.setNote(file, lineNo, "", side);
    bubbleRow.remove();
    if (onDone) onDone(false, "");
  }

  bubbleRow.querySelector(".btn-note-save").addEventListener("click", commit);
  bubbleRow.querySelector(".btn-note-cancel").addEventListener("click", cancel);
  bubbleRow.querySelector(".btn-note-discard").addEventListener("click", discard);

  textarea.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      commit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      cancel();
    }
  });
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
