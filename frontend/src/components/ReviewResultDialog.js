/**
 * ReviewResultDialog: Modal dialog displaying raw AI review output (agy --print)
 * after a review process completes or fails.
 */

let activeReviewModal = null;

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

export function closeReviewResultDialog() {
  if (activeReviewModal) {
    activeReviewModal.remove();
    activeReviewModal = null;
  }
}

/**
 * Open the review result dialog.
 *
 * @param {Object} options
 * @param {string} options.subject - Email/patch subject
 * @param {string} options.output - Raw output text from agy
 * @param {string} [options.status="completed"] - Review status ("completed", "error", etc.)
 * @param {string} [options.error=null] - Error message if failed
 */
export function openReviewResultDialog({ subject = "", output = "", status = "completed", error = null }) {
  closeReviewResultDialog();

  const backdrop = document.createElement("div");
  backdrop.className = "apply-modal-backdrop";
  backdrop.id = "review-result-modal-backdrop";

  const isSuccess = status === "completed";
  const icon = isSuccess ? "🤖" : "⚠️";
  const title = isSuccess ? "AI Review Result" : "AI Review Result (Failed)";
  const rawText = (output || "").trim() || (error ? `Error: ${error}` : "(No output returned by agy)");

  backdrop.innerHTML = `
    <div class="apply-modal review-result-modal" role="dialog" aria-modal="true" aria-labelledby="review-result-title" style="max-width: 760px; max-height: 85vh; display: flex; flex-direction: column;">
      <!-- Header -->
      <div class="apply-modal-header">
        <div class="apply-title-group">
          <span class="apply-modal-icon">${icon}</span>
          <span class="apply-modal-title" id="review-result-title">${escapeHtml(title)}</span>
        </div>
        <button type="button" class="apply-modal-close" id="btn-close-review-result" title="Close (Esc)">✕</button>
      </div>

      <!-- Body -->
      <div class="apply-modal-body" style="flex: 1; overflow-y: auto; padding: 16px 20px; display: flex; flex-direction: column; gap: 10px;">
        ${subject ? `
          <div style="font-size: 11px; color: var(--text-muted); word-break: break-all;">
            <strong>Patch:</strong> ${escapeHtml(subject)}
          </div>
        ` : ""}

        ${error && isSuccess === false ? `
          <div class="apply-error-banner" style="margin-bottom: 4px;">
            ${escapeHtml(error)}
          </div>
        ` : ""}

        <div style="display: flex; align-items: center; justify-content: space-between; margin-top: 4px;">
          <label style="font-size: 11px; font-weight: 600; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.5px;">
            Raw agy Output:
          </label>
          <button type="button" class="apply-btn secondary" id="btn-copy-review-output" style="padding: 2px 8px; font-size: 11px;" title="Copy output to clipboard">
            📋 Copy
          </button>
        </div>

        <pre class="review-result-output" id="review-result-text" style="flex: 1; min-height: 200px; max-height: 480px; overflow-y: auto; background-color: var(--bg-primary, #090d16); border: 1px solid var(--border-color); border-radius: 4px; padding: 12px; font-family: var(--font-mono); font-size: 12px; line-height: 1.5; color: var(--text-main); white-space: pre-wrap; word-break: break-word; user-select: text; margin: 0;"></pre>
      </div>

      <!-- Footer -->
      <div class="apply-modal-footer" style="padding: 12px 20px;">
        <button type="button" class="apply-btn primary" id="btn-ok-review-result">Close</button>
      </div>
    </div>
  `;

  document.body.appendChild(backdrop);
  activeReviewModal = backdrop;

  const outputEl = backdrop.querySelector("#review-result-text");
  if (outputEl) {
    outputEl.textContent = rawText;
  }

  // Close handlers
  const handleClose = () => {
    closeReviewResultDialog();
    window.removeEventListener("keydown", handleKeyDown);
  };

  const handleKeyDown = (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      handleClose();
    }
  };

  window.addEventListener("keydown", handleKeyDown);

  backdrop.querySelector("#btn-close-review-result")?.addEventListener("click", handleClose);
  backdrop.querySelector("#btn-ok-review-result")?.addEventListener("click", handleClose);

  backdrop.addEventListener("click", (e) => {
    if (e.target === backdrop) {
      handleClose();
    }
  });

  // Copy button
  const copyBtn = backdrop.querySelector("#btn-copy-review-output");
  if (copyBtn) {
    copyBtn.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(rawText);
        copyBtn.innerText = "✓ Copied";
        setTimeout(() => {
          if (copyBtn) copyBtn.innerText = "📋 Copy";
        }, 2000);
      } catch {
        const ta = document.createElement("textarea");
        ta.value = rawText;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand("copy");
        document.body.removeChild(ta);
        copyBtn.innerText = "✓ Copied";
        setTimeout(() => {
          if (copyBtn) copyBtn.innerText = "📋 Copy";
        }, 2000);
      }
    });
  }
}
