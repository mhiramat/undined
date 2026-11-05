/**
 * SideDiff: Responsive Side-by-Side and Unified patch diff renderer.
 * Automatically switches between dual-column (>= 1050px) and unified (< 1050px).
 */

import { store } from "../store.js";
import { mountNoteBubble } from "./NoteBubble.js";
import { linkifyUrls } from "../linkify.js";

const BREAKPOINT_WIDTH = 1050;

function normalizeFilePath(p) {
  if (!p) return "";
  return String(p).trim().replace(/^[ab]\//, "").replace(/^\/+/, "");
}

function filePathsMatch(noteFile, diffFile) {
  const nf = normalizeFilePath(noteFile);
  const df = normalizeFilePath(diffFile);
  if (!nf || !df) return false;
  if (nf === df) return true;
  if (nf.toUpperCase() === "DESCRIPTION" && df.toUpperCase() === "DESCRIPTION") return true;
  return df.endsWith("/" + nf) || nf.endsWith("/" + df);
}

export function findMatchingNote(notes, fileName, lineNo, side = "new") {
  if (!notes || !fileName || !lineNo) return null;
  const targetLine = Number(lineNo);
  if (isNaN(targetLine) || targetLine <= 0) return null;
  const normSide = (side === "left" || side === "old") ? "old" : "new";
  const isDesc = String(fileName).toUpperCase() === "DESCRIPTION";

  const sideKey = `${fileName}:${targetLine}:${normSide}`;
  if (notes[sideKey]) return notes[sideKey];

  const baseNote = notes[`${fileName}:${targetLine}`];
  if (baseNote) {
    if (isDesc) return baseNote;
    const bSide = (baseNote.side === "left" || baseNote.side === "old") ? "old" : "new";
    if (bSide === normSide) return baseNote;
  }

  for (const n of Object.values(notes)) {
    if (!n) continue;
    const nLine = Number(n.lineNo != null ? n.lineNo : n.line_no);
    if (nLine !== targetLine) continue;
    const nFile = n.file || n.file_path;
    if (!filePathsMatch(nFile, fileName)) continue;
    if (isDesc) return n;
    const nSide = (n.side === "left" || n.side === "old") ? "old" : "new";
    if (nSide === normSide) return n;
  }

  return null;
}

export function renderSavedNoteRowHtml(note, colSpan, fallbackFile = "") {
  if (!note || !note.text) return "";
  const file = note.file || note.file_path || fallbackFile;
  const lineNo = note.lineNo != null ? note.lineNo : note.line_no;
  const side = (note.side === "left" || note.side === "old") ? "old" : "new";
  const isAgent = note.author === "agent";
  const badgeLabel = isAgent ? "🤖 AI Review Comment" : "💬 Review Comment";
  const sideSuffix = side === "old" && String(file).toUpperCase() !== "DESCRIPTION" ? " [old]" : "";

  return `
    <tr class="diff-saved-note-row" data-file="${escapeHtml(String(file))}" data-line="${escapeHtml(String(lineNo))}" data-side="${escapeHtml(side)}">
      <td colspan="${colSpan}">
        <div class="diff-saved-note-box ${isAgent ? "is-agent-note" : ""}">
          <div class="diff-saved-note-header">
            <span class="diff-saved-note-title">
              <strong>${badgeLabel}</strong>
              <span class="diff-saved-note-target">${escapeHtml(String(file))}:${escapeHtml(String(lineNo))}${sideSuffix}</span>
            </span>
            <span class="diff-saved-note-actions">
              <button type="button" class="btn-saved-note-edit" data-file="${escapeHtml(String(file))}" data-line="${escapeHtml(String(lineNo))}" data-side="${escapeHtml(side)}">✏️ Edit</button>
              <button type="button" class="btn-saved-note-discard" data-file="${escapeHtml(String(file))}" data-line="${escapeHtml(String(lineNo))}" data-side="${escapeHtml(side)}">🗑️ Discard</button>
            </span>
          </div>
          <pre class="diff-saved-note-text">${escapeHtml(note.text)}</pre>
        </div>
      </td>
    </tr>
  `;
}

export function renderSideDiff(container, parsedDiff) {
  if (!parsedDiff || ((!parsedDiff.files || parsedDiff.files.length === 0) && !parsedDiff.preamble)) {
    container.innerHTML = `<div style="padding: 16px; color: var(--text-muted);">No patch diff found.</div>`;
    return;
  }

  let resizeObserver = null;

  function render() {
    const width = container.clientWidth || store.getState().containerWidth || 1200;
    const isSideBySide = width >= BREAKPOINT_WIDTH;
    const notes = store.getState().activeNotes || {};
    const renderedNotes = new Set();

    let html = "";

    // Preamble / Commit message
    if (parsedDiff.preamble) {
      const preambleLines = parsedDiff.preamble.split("\n");
      html += `
        <div class="diff-file-card" data-file="DESCRIPTION" style="margin-bottom: 16px;">
          <div class="diff-file-header">
            <span>📝 Commit Description</span>
            <span style="font-size: 11px; color: var(--text-muted); float: right;">
              Click line number to comment
            </span>
          </div>
          <table class="diff-table unified">
            <colgroup>
              <col class="diff-col-gutter">
              <col class="diff-col-code-unified">
            </colgroup>
            <tbody>
      `;

      for (let i = 0; i < preambleLines.length; i++) {
        const lineNo = i + 1;
        const lineContent = linkifyUrls((preambleLines[i] || "").replace(/[\r\n]/g, ""));
        const descNote = findMatchingNote(notes, "DESCRIPTION", lineNo, "new");
        if (descNote) renderedNotes.add(descNote);

        html += `
          <tr class="diff-row">
            <td class="diff-gutter ${descNote ? "has-note" : ""}" data-side="new" data-file="DESCRIPTION" data-line="${lineNo}">${lineNo}</td>
            <td class="diff-code ctx" style="padding-left: 8px;">${lineContent}</td>
          </tr>
        `;
        if (descNote) {
          html += renderSavedNoteRowHtml(descNote, 2, "DESCRIPTION");
        }
      }

      html += `</tbody></table></div>`;
    }

    // Render each changed file
    for (const file of (parsedDiff.files || [])) {
      const fileName = (file.new_path && file.new_path !== "/dev/null") ? file.new_path : (file.old_path || file.new_path);
      html += `
        <div class="diff-file-card" data-file="${escapeHtml(fileName)}">
          <div class="diff-file-header">
            <span>📄 ${escapeHtml(fileName)}</span>
            <span style="font-size: 11px; color: var(--text-muted); float: right;">
              ${isSideBySide ? "Side-by-Side (Pre / Post)" : "Unified Diff"}
            </span>
          </div>
      `;

      for (const hunk of (file.hunks || [])) {
        html += `<div class="diff-hunk-header">${escapeHtml(hunk.header)} ${escapeHtml(hunk.section || "")}</div>`;

        if (isSideBySide) {
          // Dual Column Table
          html += `
            <table class="diff-table side-by-side">
              <colgroup>
                <col class="diff-col-gutter">
                <col class="diff-col-code">
                <col class="diff-col-gutter">
                <col class="diff-col-code">
              </colgroup>
              <tbody>
          `;

          for (const row of (hunk.paired_rows || [])) {
            const leftNote = row.left.line_no ? findMatchingNote(notes, fileName, row.left.line_no, "old") : null;
            const rightNote = row.right.line_no ? findMatchingNote(notes, fileName, row.right.line_no, "new") : null;
            if (leftNote) renderedNotes.add(leftNote);
            if (rightNote) renderedNotes.add(rightNote);

            const leftContent = row.left.type === "empty" ? "" : linkifyUrls((row.left.content || "").replace(/[\r\n]/g, ""));
            const rightContent = row.right.type === "empty" ? "" : linkifyUrls((row.right.content || "").replace(/[\r\n]/g, ""));

            html += `
              <tr class="diff-row" data-row-idx="${row.row_idx}">
                <td class="diff-gutter ${leftNote ? "has-note" : ""}" data-side="left" data-file="${escapeHtml(fileName)}" data-line="${row.left.line_no || ""}">${row.left.line_no || ""}</td>
                <td class="diff-code ${row.left.type}">${leftContent}</td>
                <td class="diff-gutter right-side ${rightNote ? "has-note" : ""}" data-side="right" data-file="${escapeHtml(fileName)}" data-line="${row.right.line_no || ""}">${row.right.line_no || ""}</td>
                <td class="diff-code ${row.right.type}">${rightContent}</td>
              </tr>
            `;
            if (leftNote) {
              html += renderSavedNoteRowHtml(leftNote, 4, fileName);
            }
            if (rightNote && rightNote !== leftNote) {
              html += renderSavedNoteRowHtml(rightNote, 4, fileName);
            }
          }

          html += `</tbody></table>`;
        } else {
          // Unified Diff Table
          html += `
            <table class="diff-table unified">
              <colgroup>
                <col class="diff-col-gutter">
                <col class="diff-col-gutter">
                <col class="diff-col-code-unified">
              </colgroup>
              <tbody>
          `;

          for (const u of (hunk.unified_lines || [])) {
            const oldNote = u.old_no ? findMatchingNote(notes, fileName, u.old_no, "old") : null;
            const newNote = u.new_no ? findMatchingNote(notes, fileName, u.new_no, "new") : null;
            if (oldNote) renderedNotes.add(oldNote);
            if (newNote) renderedNotes.add(newNote);

            const prefix = u.type === "add" ? "+" : u.type === "del" ? "-" : " ";
            const uContent = linkifyUrls((u.content || "").replace(/[\r\n]/g, ""));

            html += `
              <tr class="diff-row ${u.type}">
                <td class="diff-gutter ${oldNote ? "has-note" : ""}" data-side="old" data-file="${escapeHtml(fileName)}" data-line="${u.old_no || ""}">${u.old_no || ""}</td>
                <td class="diff-gutter ${newNote ? "has-note" : ""}" data-side="new" data-file="${escapeHtml(fileName)}" data-line="${u.new_no || ""}">${u.new_no || ""}</td>
                <td class="diff-code ${u.type}">${prefix}${uContent}</td>
              </tr>
            `;
            if (oldNote) {
              html += renderSavedNoteRowHtml(oldNote, 3, fileName);
            }
            if (newNote && newNote !== oldNote) {
              html += renderSavedNoteRowHtml(newNote, 3, fileName);
            }
          }

          html += `</tbody></table>`;
        }
      }

      html += `</div>`;
    }

    // Render any remaining notes whose file/line did not match a specific rendered hunk row
    const unmappedNotes = Object.values(notes).filter((n) => n && n.text && !renderedNotes.has(n));
    if (unmappedNotes.length > 0) {
      html += `
        <div class="diff-file-card" data-file="UNMAPPED_NOTES" style="margin-top: 16px;">
          <div class="diff-file-header">
            <span>💬 Review Comments (${unmappedNotes.length})</span>
          </div>
          <table class="diff-table unified">
            <tbody>
      `;
      for (const note of unmappedNotes) {
        html += renderSavedNoteRowHtml(note, 1, note.file || note.file_path || "PATCH");
      }
      html += `</tbody></table></div>`;
    }

    // Postscript / Signature / Continued message body
    if (parsedDiff.postscript) {
      const isContinuation = parsedDiff.body_continues_after;
      const headerTitle = isContinuation ? "📝 Message Body (continued)" : "📝 Postscript / Signature";
      html += `
        <div class="diff-file-card" style="margin-top: 16px;">
          <div class="diff-file-header">
            <span>${headerTitle}</span>
          </div>
          <div style="padding: 12px 16px; background-color: var(--bg-primary);">
            <pre class="plaintext-email" style="margin: 0; font-size: 13px; line-height: 1.5; color: var(--text-primary);">${linkifyUrls(parsedDiff.postscript)}</pre>
          </div>
        </div>
      `;
    }

    container.innerHTML = html;

    // Attach gutter click listeners for mounting NoteBubbles
    container.querySelectorAll(".diff-gutter").forEach((gutter) => {
      gutter.addEventListener("click", (e) => {
        const file = gutter.getAttribute("data-file");
        const line = parseInt(gutter.getAttribute("data-line"), 10);
        if (!file || !line || isNaN(line)) return;

        const rawSide = gutter.getAttribute("data-side");
        const side = (rawSide === "left" || rawSide === "old") ? "old" : "new";
        const row = gutter.closest("tr");
        const matchedNote = findMatchingNote(store.getState().activeNotes, file, line, side);
        const existingNote = matchedNote?.text || "";
        const targetFile = matchedNote?.file || matchedNote?.file_path || file;
        const targetSide = matchedNote?.side || side;
        mountNoteBubble(row, targetFile, line, targetSide, existingNote, (saved) => {
          if (saved) {
            gutter.classList.add("has-note");
          } else if (!findMatchingNote(store.getState().activeNotes, targetFile, line, targetSide)) {
            gutter.classList.remove("has-note");
          }
        });
      });
    });

    // Attach Edit and Discard button listeners on inline saved notes
    container.querySelectorAll(".btn-saved-note-edit").forEach((btn) => {
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        const file = btn.getAttribute("data-file");
        const line = parseInt(btn.getAttribute("data-line"), 10);
        const side = btn.getAttribute("data-side") || "new";
        if (!file || !line || isNaN(line)) return;
        const noteRow = btn.closest("tr");
        const targetRow = (noteRow?.previousElementSibling && noteRow.previousElementSibling.classList.contains("diff-row"))
          ? noteRow.previousElementSibling
          : noteRow;
        if (!targetRow) return;
        const matchedNote = findMatchingNote(store.getState().activeNotes, file, line, side);
        mountNoteBubble(targetRow, file, line, side, matchedNote?.text || "");
      });
    });

    container.querySelectorAll(".btn-saved-note-discard").forEach((btn) => {
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        const file = btn.getAttribute("data-file");
        const line = parseInt(btn.getAttribute("data-line"), 10);
        const side = btn.getAttribute("data-side") || "new";
        if (!file || !line || isNaN(line)) return;
        store.setNote(file, line, "", side);
      });
    });
  }

  // Setup ResizeObserver for responsive dual-column switching
  if (window.ResizeObserver) {
    let lastWidth = container.clientWidth;
    resizeObserver = new ResizeObserver((entries) => {
      for (const entry of entries) {
        const w = entry.contentRect.width;
        if ((lastWidth < BREAKPOINT_WIDTH && w >= BREAKPOINT_WIDTH) ||
            (lastWidth >= BREAKPOINT_WIDTH && w < BREAKPOINT_WIDTH)) {
          lastWidth = w;
          store.setContainerWidth(w);
          render();
        }
      }
    });
    resizeObserver.observe(container);
  }

  render();
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
