/**
 * SideDiff: Responsive Side-by-Side and Unified patch diff renderer.
 * Automatically switches between dual-column (>= 1050px) and unified (< 1050px).
 */

import { store } from "../store.js";
import { mountNoteBubble } from "./NoteBubble.js";
import { linkifyUrls } from "../linkify.js";

const BREAKPOINT_WIDTH = 1050;

export function renderSideDiff(container, parsedDiff) {
  if (!parsedDiff || !parsedDiff.files || parsedDiff.files.length === 0) {
    container.innerHTML = `<div style="padding: 16px; color: var(--text-muted);">No patch diff found.</div>`;
    return;
  }

  let resizeObserver = null;

  function render() {
    const width = container.clientWidth || store.getState().containerWidth || 1200;
    const isSideBySide = width >= BREAKPOINT_WIDTH;
    const notes = store.getState().activeNotes;

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
        const noteKey = `DESCRIPTION:${lineNo}`;
        const hasNote = notes && notes[noteKey];

        html += `
          <tr class="diff-row">
            <td class="diff-gutter ${hasNote ? "has-note" : ""}" data-side="new" data-file="DESCRIPTION" data-line="${lineNo}">${lineNo}</td>
            <td class="diff-code ctx" style="padding-left: 8px;">${lineContent}</td>
          </tr>
        `;
      }

      html += `</tbody></table></div>`;
    }

    // Render each changed file
    for (const file of (parsedDiff.files || [])) {
      const fileName = file.new_path || file.old_path;
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
            const leftNoteKey = `${fileName}:${row.left.line_no}`;
            const rightNoteKey = `${fileName}:${row.right.line_no}`;
            const leftHasNote = row.left.line_no && notes[leftNoteKey];
            const rightHasNote = row.right.line_no && notes[rightNoteKey];
            const leftContent = row.left.type === "empty" ? "" : linkifyUrls((row.left.content || "").replace(/[\r\n]/g, ""));
            const rightContent = row.right.type === "empty" ? "" : linkifyUrls((row.right.content || "").replace(/[\r\n]/g, ""));

            html += `
              <tr class="diff-row" data-row-idx="${row.row_idx}">
                <td class="diff-gutter ${leftHasNote ? "has-note" : ""}" data-side="left" data-file="${escapeHtml(fileName)}" data-line="${row.left.line_no || ""}">${row.left.line_no || ""}</td>
                <td class="diff-code ${row.left.type}">${leftContent}</td>
                <td class="diff-gutter right-side ${rightHasNote ? "has-note" : ""}" data-side="right" data-file="${escapeHtml(fileName)}" data-line="${row.right.line_no || ""}">${row.right.line_no || ""}</td>
                <td class="diff-code ${row.right.type}">${rightContent}</td>
              </tr>
            `;
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
            const targetLine = u.new_no || u.old_no;
            const noteKey = `${fileName}:${targetLine}`;
            const hasNote = targetLine && notes[noteKey];
            const prefix = u.type === "add" ? "+" : u.type === "del" ? "-" : " ";
            const uContent = linkifyUrls((u.content || "").replace(/[\r\n]/g, ""));

            html += `
              <tr class="diff-row ${u.type}">
                <td class="diff-gutter" data-side="old" data-file="${escapeHtml(fileName)}" data-line="${u.old_no || ""}">${u.old_no || ""}</td>
                <td class="diff-gutter ${hasNote ? "has-note" : ""}" data-side="new" data-file="${escapeHtml(fileName)}" data-line="${u.new_no || ""}">${u.new_no || ""}</td>
                <td class="diff-code ${u.type}">${prefix}${uContent}</td>
              </tr>
            `;
          }

          html += `</tbody></table>`;
        }
      }

      html += `</div>`;
    }

    // Postscript / Signature
    if (parsedDiff.postscript) {
      html += `
        <div style="background-color: var(--bg-secondary); padding: 12px 16px; margin-top: 16px; border-radius: 4px; border: 1px solid var(--border-color); min-width: 100%; width: max-content;">
          <pre class="plaintext-email" style="color: var(--text-muted); font-size: 11px;">${linkifyUrls(parsedDiff.postscript)}</pre>
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
        const existingNote = notes[`${file}:${line}`]?.text || "";
        mountNoteBubble(row, file, line, side, existingNote, (saved) => {
          if (saved) {
            gutter.classList.add("has-note");
          } else if (!store.getState().activeNotes[`${file}:${line}`]) {
            gutter.classList.remove("has-note");
          }
        });
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
