/**
 * ApplyPatchDialog: Modal dialog displayed when clicking "Apply with git am",
 * allowing users to specify a base commit and create a review branch
 * with an automatically generated branch name.
 */

import { applyPatchSeries, abortApplyPatchSeries } from "../api.js";
import { store } from "../store.js";

let activeModal = null;

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

export function extractSeriesVersion(subject) {
  if (!subject) return "";
  const bracketMatch = subject.match(/\[[^\]]*\bv(\d+)\b[^\]]*\]/i);
  if (bracketMatch) {
    return `v${bracketMatch[1]}`;
  }
  const standaloneMatch = subject.match(/\bv(\d+)\b/i);
  if (standaloneMatch) {
    return `v${standaloneMatch[1]}`;
  }
  return "";
}

export function cleanSeriesTitle(subject) {
  if (!subject) return "";
  let cleaned = subject.replace(/^(?:\s*\[[^\]]*\]\s*|\s*(?:re|fwd):\s*)+/gi, "").trim();
  if (!cleaned) {
    cleaned = subject.replace(/^\[+|\]+$/g, "").trim();
  }
  return cleaned || subject;
}

/**
 * Generate a branch name by lowercasing the first 32 characters of the email subject by default,
 * replacing spaces and symbols with hyphens, and prefixing it with "review/".
 * (If the series title has a version, please add it at the end.)
 */
export function generateBranchName(subject) {
  if (!subject) return "review/patch";

  const version = extractSeriesVersion(subject);
  const title = cleanSeriesTitle(subject);

  // Take first 32 characters of the title/subject, lowercase
  const sliced = title.slice(0, 32).toLowerCase();

  // Replace spaces and symbols with hyphens
  let slug = sliced.replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");

  if (!slug) {
    slug = "patch";
  }

  // If the series title has a version, please add it at the end
  if (version) {
    const verLower = version.toLowerCase();
    if (!slug.endsWith(`-${verLower}`) && slug !== verLower) {
      slug = `${slug}-${verLower}`;
    }
  }

  return `review/${slug}`;
}

export function openApplyPatchDialog(msg, onApplySuccess = null) {
  closeApplyPatchDialog();

  if (!msg || !msg.message_id) return;

  const cfg = store.getState().config;
  const defaultRepo = cfg?.kernel_git_dir || "~/linux";
  const defaultBranch = generateBranchName(msg.subject);

  const backdrop = document.createElement("div");
  backdrop.className = "apply-modal-backdrop";

  backdrop.innerHTML = `
    <div class="apply-modal" role="dialog" aria-modal="true" aria-labelledby="apply-dialog-title">
      <!-- Header -->
      <div class="apply-modal-header">
        <div class="apply-title-group">
          <span class="apply-modal-icon">📥</span>
          <span class="apply-modal-title" id="apply-dialog-title">Apply Patch with git am</span>
        </div>
        <button type="button" class="apply-modal-close" id="btn-close-apply-dialog" title="Close (Esc)">✕</button>
      </div>

      <!-- Body / Form -->
      <form id="apply-patch-form" class="apply-modal-body">
        <div class="apply-patch-meta">
          <div class="apply-meta-row">
            <span class="apply-meta-label">Subject:</span>
            <span class="apply-meta-val">${escapeHtml(msg.subject || "(No Subject)")}</span>
          </div>
          <div class="apply-meta-row">
            <span class="apply-meta-label">Msg-ID:</span>
            <span class="apply-meta-val"><code>&lt;${escapeHtml(msg.message_id)}&gt;</code></span>
          </div>
        </div>

        <div id="apply-error-banner" class="apply-error-banner" style="display: none;"></div>

        <!-- Create branch checkbox -->
        <div class="apply-form-group">
          <label class="apply-checkbox-label">
            <input type="checkbox" id="apply-create-branch-chk" checked />
            <span>Create and checkout new branch before applying</span>
          </label>
        </div>

        <!-- Branch Name -->
        <div class="apply-form-group" id="group-branch-name">
          <label class="apply-label" for="apply-branch-input">Branch Name:</label>
          <input
            type="text"
            class="apply-input"
            id="apply-branch-input"
            value="${escapeAttr(defaultBranch)}"
            placeholder="review/..."
            autocomplete="off"
            spellcheck="false"
          />
          <div class="apply-hint">
            Default generated with <code>review/</code> prefix, lowercased first 32 characters, and version suffix.
          </div>
        </div>

        <!-- Base Commit -->
        <div class="apply-form-group" id="group-base-commit">
          <label class="apply-label" for="apply-base-commit-input">Base Commit / Ref:</label>
          <input
            type="text"
            class="apply-input"
            id="apply-base-commit-input"
            value="HEAD"
            placeholder="e.g. HEAD, origin/master, v6.12, commit SHA"
            autocomplete="off"
            spellcheck="false"
          />
          <div class="apply-hint">
            Commit, tag, or branch to start the new branch from (default: <code>HEAD</code>).
          </div>
        </div>

        <!-- Git Repo Path -->
        <div class="apply-form-group">
          <label class="apply-label" for="apply-repo-input">Git Repository Directory:</label>
          <input
            type="text"
            class="apply-input"
            id="apply-repo-input"
            value="${escapeAttr(defaultRepo)}"
            placeholder="~/linux"
            autocomplete="off"
            spellcheck="false"
          />
          <div class="apply-hint">
            Local Linux kernel git repository directory where <code>b4 am | git am</code> executes.
          </div>
        </div>

        <!-- Footer Buttons -->
        <div class="apply-modal-footer">
          <button type="button" class="apply-btn secondary" id="btn-merge-manual" style="display: none;">Merge manually</button>
          <button type="button" class="apply-btn secondary" id="btn-cancel-apply">Cancel</button>
          <button type="submit" class="apply-btn primary" id="btn-submit-apply">
            📥 Apply Patch
          </button>
        </div>
      </form>
    </div>
  `;

  document.body.appendChild(backdrop);
  activeModal = backdrop;

  const form = backdrop.querySelector("#apply-patch-form");
  const branchChk = backdrop.querySelector("#apply-create-branch-chk");
  const branchInput = backdrop.querySelector("#apply-branch-input");
  const baseCommitInput = backdrop.querySelector("#apply-base-commit-input");
  const repoInput = backdrop.querySelector("#apply-repo-input");
  const submitBtn = backdrop.querySelector("#btn-submit-apply");
  const mergeManualBtn = backdrop.querySelector("#btn-merge-manual");
  const cancelBtn = backdrop.querySelector("#btn-cancel-apply");
  const closeBtn = backdrop.querySelector("#btn-close-apply-dialog");
  const errorBanner = backdrop.querySelector("#apply-error-banner");

  let failedApplyInfo = null;
  let isAborting = false;

  // Focus branch input initially
  setTimeout(() => {
    branchInput.focus();
    branchInput.select();
  }, 50);

  // Toggle branch fields when checkbox changes
  branchChk.addEventListener("change", () => {
    const isBranchEnabled = branchChk.checked;
    branchInput.disabled = !isBranchEnabled;
    baseCommitInput.disabled = !isBranchEnabled;
    branchInput.style.opacity = isBranchEnabled ? "1" : "0.5";
    baseCommitInput.style.opacity = isBranchEnabled ? "1" : "0.5";
  });

  const doApply = async () => {
    errorBanner.style.display = "none";
    errorBanner.innerText = "";
    if (mergeManualBtn) mergeManualBtn.style.display = "none";

    const createBranch = branchChk.checked;
    const branchName = createBranch ? branchInput.value.trim() : null;
    const baseCommit = createBranch ? (baseCommitInput.value.trim() || null) : null;
    const targetRepo = repoInput.value.trim() || null;

    if (createBranch && !branchName) {
      errorBanner.innerText = "Please specify a branch name or uncheck 'Create new branch'.";
      errorBanner.style.display = "block";
      branchInput.focus();
      return;
    }

    submitBtn.disabled = true;
    submitBtn.innerText = "⏳ Applying with git am...";

    try {
      const res = await applyPatchSeries(msg.message_id, targetRepo, branchName, baseCommit);
      if (res.success) {
        failedApplyInfo = null;
        closeApplyPatchDialog();
        const branchMsg = res.branch ? ` on branch '${res.branch}'` : "";
        showToast(`Patch applied successfully${branchMsg}!`, "success");
        if (typeof onApplySuccess === "function") {
          onApplySuccess(res);
        }
      } else {
        const errMsg = res.stderr || res.stdout || "Failed to apply patch";
        errorBanner.innerText = errMsg;
        errorBanner.style.display = "block";
        showToast(`git am error: ${errMsg}`, "error");
        submitBtn.disabled = false;
        submitBtn.innerText = "📥 Apply Patch";

        failedApplyInfo = {
          target_git_repo: res.target_git_repo || targetRepo,
          branch_name: res.branch || branchName,
          base_commit: res.base_commit || baseCommit,
          original_branch: res.original_branch || null,
          head_before: res.head_before || null,
        };
        if (mergeManualBtn) mergeManualBtn.style.display = "";
      }
    } catch (err) {
      errorBanner.innerText = err.message || "Request failed";
      errorBanner.style.display = "block";
      showToast(err.message, "error");
      submitBtn.disabled = false;
      submitBtn.innerText = "📥 Apply Patch";
    }
  };

  const handleCancel = async () => {
    if (isAborting) return;
    if (failedApplyInfo) {
      isAborting = true;
      cancelBtn.disabled = true;
      cancelBtn.innerText = "⏳ Aborting...";
      if (mergeManualBtn) mergeManualBtn.disabled = true;
      submitBtn.disabled = true;
      try {
        const abortRes = await abortApplyPatchSeries(failedApplyInfo);
        if (abortRes.success) {
          showToast("Patch application canceled. Aborted git am and removed branch.", "info");
        } else {
          showToast(`Abort error: ${abortRes.error || "Failed to abort git am"}`, "error");
        }
      } catch (err) {
        showToast(`Abort error: ${err.message}`, "error");
      } finally {
        isAborting = false;
        failedApplyInfo = null;
      }
    }
    closeApplyPatchDialog();
  };

  const handleMergeManual = () => {
    const branchMsg = failedApplyInfo?.branch_name ? ` on branch '${failedApplyInfo.branch_name}'` : "";
    showToast(`Left intermediate git am state${branchMsg} for manual merge.`, "info");
    failedApplyInfo = null;
    closeApplyPatchDialog();
  };

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    doApply();
  });

  if (mergeManualBtn) {
    mergeManualBtn.addEventListener("click", handleMergeManual);
  }
  cancelBtn.addEventListener("click", handleCancel);
  closeBtn.addEventListener("click", handleCancel);

  // Close on backdrop outside click
  backdrop.addEventListener("click", (e) => {
    if (e.target === backdrop) {
      handleCancel();
    }
  });

  // Esc key listener
  const escHandler = (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      handleCancel();
    }
  };
  window.addEventListener("keydown", escHandler);
  backdrop._escHandler = escHandler;
}

export function closeApplyPatchDialog() {
  if (activeModal) {
    if (activeModal._escHandler) {
      window.removeEventListener("keydown", activeModal._escHandler);
    }
    activeModal.remove();
    activeModal = null;
  }
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
