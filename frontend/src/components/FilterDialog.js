/**
 * FilterDialog: Modal dialog allowing users to filter messages in the summary pane
 * by subject, from (e.g. from:developer), date (e.g. before:2026/Sep, after:2026/Jan),
 * color tags (selectable dropdown with multiple boxes), or patch/unread status.
 */

import { store } from "../store.js";
import { parseFilterQuery, buildFilterQuery } from "../filter.js";
import { getTags, isTagMatch } from "../tags.js";

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

export async function openFilterDialog() {
  closeFilterDialog();

  const tags = await getTags();
  const state = store.getState();
  const currentQuery = state.filterQuery || "";
  const parsed = state.parsedFilter || (currentQuery ? parseFilterQuery(currentQuery) : null);

  const initialFrom = parsed?.from || "";
  const initialSubject = parsed?.subject || "";
  const initialBefore = parsed?.beforeStr || "";
  const initialAfter = parsed?.afterStr || "";
  const initialTags = parsed?.tags || [];
  const initialIsPatch = parsed?.isPatch === true;
  let initialReadStatus = "all";
  if (parsed?.isRead === true) initialReadStatus = "read";
  else if (parsed?.isRead === false) initialReadStatus = "unread";

  // Build tag items list HTML
  let tagItemsHtml = "";
  for (const [key, val] of Object.entries(tags)) {
    const isSelected = isTagMatch(key, val, initialTags);
    tagItemsHtml += `
      <label class="filter-tag-item" title="${escapeAttr(val.tag)} (${escapeAttr(key)})">
        <input
          type="checkbox"
          class="filter-tag-checkbox"
          value="${escapeAttr(val.tag)}"
          data-tag-key="${escapeAttr(key)}"
          data-tag-color="${escapeAttr(val.color)}"
          ${isSelected ? "checked" : ""}
        />
        <span class="tag-color-dot" style="background-color: ${escapeAttr(val.color)};"></span>
        <span class="filter-tag-name">${escapeHtml(val.tag)}</span>
        <span class="filter-tag-key-hint">${escapeHtml(key)}</span>
      </label>
    `;
  }

  const backdrop = document.createElement("div");
  backdrop.className = "filter-modal-backdrop";

  backdrop.innerHTML = `
    <div class="filter-modal" role="dialog" aria-modal="true" aria-labelledby="filter-dialog-title">
      <!-- Header -->
      <div class="filter-modal-header">
        <div class="filter-title-group">
          <span class="filter-modal-icon">🔍</span>
          <span class="filter-modal-title" id="filter-dialog-title">Filter Messages</span>
        </div>
        <button type="button" class="filter-modal-close" id="btn-close-filter-dialog" title="Close (Esc)">✕</button>
      </div>

      <!-- Body / Form -->
      <form id="filter-form" class="filter-modal-body">
        <!-- Unified Query Bar -->
        <div class="filter-form-group">
          <label class="filter-label" for="filter-query-input">
            Filter Query Expression:
          </label>
          <input
            type="text"
            class="filter-input query-main"
            id="filter-query-input"
            value="${escapeAttr(currentQuery)}"
            placeholder="e.g. from:developer subject:kprobes tag:Red,Blue before:2026/Sep is:patch"
            autocomplete="off"
            autofocus
          />
          <div class="filter-syntax-hint">
            <span>Syntax:</span>
            <code>from:&lt;author&gt;</code>
            <code>subject:&lt;text&gt;</code>
            <code>tag:&lt;color&gt;</code>
            <code>before:&lt;YYYY/Mon&gt;</code>
            <code>after:&lt;YYYY/Mon&gt;</code>
            <code>is:patch</code>
            <code>is:unread</code>
          </div>
        </div>

        <div class="filter-form-divider">
          <span>OR FILTER BY SPECIFIC CRITERIA</span>
        </div>

        <!-- Structured Grid -->
        <div class="filter-grid">
          <div class="filter-form-group">
            <label class="filter-label" for="field-filter-from">From (Author / Email):</label>
            <input
              type="text"
              class="filter-input"
              id="field-filter-from"
              value="${escapeAttr(initialFrom)}"
              placeholder="e.g. dev or developer"
            />
          </div>

          <div class="filter-form-group">
            <label class="filter-label" for="field-filter-subject">Subject keyword:</label>
            <input
              type="text"
              class="filter-input"
              id="field-filter-subject"
              value="${escapeAttr(initialSubject)}"
              placeholder="e.g. kprobes, ftrace, net"
            />
          </div>

          <div class="filter-form-group">
            <label class="filter-label" for="field-filter-before">Before Date:</label>
            <input
              type="text"
              class="filter-input"
              id="field-filter-before"
              value="${escapeAttr(initialBefore)}"
              placeholder="e.g. 2026/Sep or 2026-09-01"
            />
          </div>

          <div class="filter-form-group">
            <label class="filter-label" for="field-filter-after">After Date:</label>
            <input
              type="text"
              class="filter-input"
              id="field-filter-after"
              value="${escapeAttr(initialAfter)}"
              placeholder="e.g. 2026/Jan or 2026-01-01"
            />
          </div>
        </div>

        <!-- Color Tags Dropdown Field -->
        <div class="filter-form-group filter-tag-control-group">
          <label class="filter-label" id="filter-tag-label" for="btn-filter-tag-dropdown">
            Color Tags:
          </label>
          <div class="filter-tag-dropdown-wrapper" id="filter-tag-dropdown-wrapper">
            <button
              type="button"
              class="filter-dropdown-btn"
              id="btn-filter-tag-dropdown"
              aria-haspopup="true"
              aria-expanded="false"
              aria-labelledby="filter-tag-label btn-filter-tag-dropdown"
            >
              <div class="filter-dropdown-selection" id="filter-tag-selection-display"></div>
              <span class="filter-dropdown-arrow">▼</span>
            </button>

            <div class="filter-tag-dropdown-menu" id="filter-tag-dropdown-menu" style="display: none;" role="menu">
              <div class="filter-tag-dropdown-header">
                <span class="filter-tag-dropdown-title">Select Color Tags</span>
                <div class="filter-tag-quick-actions">
                  <button type="button" class="filter-tag-quick-btn" id="btn-tag-select-all" title="Select all tags">Select All</button>
                  <button type="button" class="filter-tag-quick-btn" id="btn-tag-clear-all" title="Clear tag selection">Clear</button>
                </div>
              </div>
              <div class="filter-tag-items-list" id="filter-tag-items-list">
                ${tagItemsHtml}
              </div>
            </div>
          </div>
        </div>

        <div class="filter-options-row">
          <label class="filter-checkbox-label">
            <input type="checkbox" id="field-filter-patch" ${initialIsPatch ? "checked" : ""} />
            <span>Only patch emails (<code>[PATCH]</code>)</span>
          </label>

          <label class="filter-checkbox-label">
            <input type="checkbox" id="field-filter-unread" ${initialReadStatus === "unread" ? "checked" : ""} />
            <span>Only unread emails</span>
          </label>
        </div>

        <!-- Footer Actions -->
        <div class="filter-modal-footer">
          <div class="footer-left">
            ${currentQuery ? `
              <button type="button" class="filter-btn danger" id="btn-dialog-clear-filter">
                ✕ Clear Filter
              </button>
            ` : ""}
          </div>
          <div class="footer-right">
            <button type="button" class="filter-btn secondary" id="btn-dialog-cancel">
              Cancel
            </button>
            <button type="submit" class="filter-btn primary" id="btn-dialog-apply">
              🔍 Apply Filter
            </button>
          </div>
        </div>
      </form>
    </div>
  `;

  document.body.appendChild(backdrop);
  activeModal = backdrop;

  const queryInput = backdrop.querySelector("#filter-query-input");
  const fieldFrom = backdrop.querySelector("#field-filter-from");
  const fieldSubject = backdrop.querySelector("#field-filter-subject");
  const fieldBefore = backdrop.querySelector("#field-filter-before");
  const fieldAfter = backdrop.querySelector("#field-filter-after");
  const fieldPatch = backdrop.querySelector("#field-filter-patch");
  const fieldUnread = backdrop.querySelector("#field-filter-unread");
  const form = backdrop.querySelector("#filter-form");

  const tagDropdownBtn = backdrop.querySelector("#btn-filter-tag-dropdown");
  const tagDropdownMenu = backdrop.querySelector("#filter-tag-dropdown-menu");
  const tagDropdownWrapper = backdrop.querySelector("#filter-tag-dropdown-wrapper");
  const tagSelectionDisplay = backdrop.querySelector("#filter-tag-selection-display");
  const tagItemsList = backdrop.querySelector("#filter-tag-items-list");

  function updateSelectionDisplay() {
    const checkedBoxes = Array.from(backdrop.querySelectorAll(".filter-tag-checkbox:checked"));
    if (!tagSelectionDisplay) return;

    if (checkedBoxes.length === 0) {
      tagSelectionDisplay.innerHTML = `<span class="filter-dropdown-placeholder">Any tag (No tag filter)</span>`;
      return;
    }

    if (checkedBoxes.length <= 2) {
      let pillsHtml = "";
      checkedBoxes.forEach((chk) => {
        const tagName = chk.value;
        const tagColor = chk.getAttribute("data-tag-color") || "#94a3b8";
        pillsHtml += `
          <span class="filter-selected-tag-pill" style="border-color: ${escapeAttr(tagColor)}; color: ${escapeAttr(tagColor)};">
            <span class="tag-color-dot" style="background-color: ${escapeAttr(tagColor)};"></span>
            <span>${escapeHtml(tagName)}</span>
          </span>
        `;
      });
      tagSelectionDisplay.innerHTML = pillsHtml;
      return;
    }

    // More than 2 tags selected: show first 2 pills + count pill
    let pillsHtml = "";
    checkedBoxes.slice(0, 2).forEach((chk) => {
      const tagName = chk.value;
      const tagColor = chk.getAttribute("data-tag-color") || "#94a3b8";
      pillsHtml += `
        <span class="filter-selected-tag-pill" style="border-color: ${escapeAttr(tagColor)}; color: ${escapeAttr(tagColor)};">
          <span class="tag-color-dot" style="background-color: ${escapeAttr(tagColor)};"></span>
          <span>${escapeHtml(tagName)}</span>
        </span>
      `;
    });
    pillsHtml += `
      <span class="filter-selected-tag-pill count-pill" title="${checkedBoxes.length} tags selected">
        +${checkedBoxes.length - 2} more (${checkedBoxes.length})
      </span>
    `;
    tagSelectionDisplay.innerHTML = pillsHtml;
  }

  // Initialize selection display
  updateSelectionDisplay();

  function getSelectedTags() {
    const checked = [];
    backdrop.querySelectorAll(".filter-tag-checkbox:checked").forEach((chk) => {
      checked.push(chk.value);
    });
    return checked;
  }

  // Keep query input in sync when typing in structured fields or selecting tags
  function syncStructuredToQuery() {
    const query = buildFilterQuery({
      from: fieldFrom.value,
      subject: fieldSubject.value,
      before: fieldBefore.value,
      after: fieldAfter.value,
      tags: getSelectedTags(),
      isPatch: fieldPatch.checked,
      isRead: fieldUnread.checked ? "unread" : "",
    });
    queryInput.value = query;
  }

  function syncQueryToTagCheckboxes(parsedTags) {
    backdrop.querySelectorAll(".filter-tag-checkbox").forEach((chk) => {
      const key = chk.getAttribute("data-tag-key") || "";
      const color = chk.getAttribute("data-tag-color") || "";
      const name = chk.value;
      chk.checked = isTagMatch(key, { tag: name, color: color }, parsedTags);
    });
    updateSelectionDisplay();
  }

  [fieldFrom, fieldSubject, fieldBefore, fieldAfter].forEach((input) => {
    input.addEventListener("input", syncStructuredToQuery);
  });
  [fieldPatch, fieldUnread].forEach((chk) => {
    chk.addEventListener("change", syncStructuredToQuery);
  });

  // When editing query directly, update structured fields and tag checkboxes
  queryInput.addEventListener("input", () => {
    const p = parseFilterQuery(queryInput.value);
    if (p) {
      fieldFrom.value = p.from || "";
      fieldSubject.value = p.subject || "";
      fieldBefore.value = p.beforeStr || "";
      fieldAfter.value = p.afterStr || "";
      fieldPatch.checked = p.isPatch === true;
      fieldUnread.checked = p.isRead === false;
      syncQueryToTagCheckboxes(p.tags || []);
    } else {
      fieldFrom.value = "";
      fieldSubject.value = "";
      fieldBefore.value = "";
      fieldAfter.value = "";
      fieldPatch.checked = false;
      fieldUnread.checked = false;
      syncQueryToTagCheckboxes([]);
    }
  });

  // Tag dropdown toggling
  function toggleTagDropdown(show) {
    const shouldShow = typeof show === "boolean" ? show : tagDropdownMenu.style.display === "none";
    if (shouldShow) {
      tagDropdownMenu.style.display = "flex";
      tagDropdownBtn.setAttribute("aria-expanded", "true");
    } else {
      tagDropdownMenu.style.display = "none";
      tagDropdownBtn.setAttribute("aria-expanded", "false");
    }
  }

  tagDropdownBtn.addEventListener("click", (e) => {
    e.preventDefault();
    e.stopPropagation();
    toggleTagDropdown();
  });

  // Prevent clicks inside dropdown menu from closing it
  tagDropdownMenu.addEventListener("click", (e) => {
    e.stopPropagation();
  });

  // Checkbox change listener
  tagItemsList.addEventListener("change", (e) => {
    if (e.target.classList.contains("filter-tag-checkbox")) {
      updateSelectionDisplay();
      syncStructuredToQuery();
    }
  });

  // Quick actions: Select All & Clear
  backdrop.querySelector("#btn-tag-select-all")?.addEventListener("click", (e) => {
    e.preventDefault();
    e.stopPropagation();
    backdrop.querySelectorAll(".filter-tag-checkbox").forEach((chk) => {
      chk.checked = true;
    });
    updateSelectionDisplay();
    syncStructuredToQuery();
  });

  backdrop.querySelector("#btn-tag-clear-all")?.addEventListener("click", (e) => {
    e.preventDefault();
    e.stopPropagation();
    backdrop.querySelectorAll(".filter-tag-checkbox").forEach((chk) => {
      chk.checked = false;
    });
    updateSelectionDisplay();
    syncStructuredToQuery();
  });

  // Form submit (Apply)
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const finalQuery = queryInput.value.trim();
    if (finalQuery) {
      store.setFilter(finalQuery);
    } else {
      store.clearFilter();
    }
    closeFilterDialog();
  });

  // Clear button in footer
  const clearBtn = backdrop.querySelector("#btn-dialog-clear-filter");
  if (clearBtn) {
    clearBtn.addEventListener("click", () => {
      store.clearFilter();
      closeFilterDialog();
    });
  }

  // Cancel / Close
  const closeBtn = backdrop.querySelector("#btn-close-filter-dialog");
  const cancelBtn = backdrop.querySelector("#btn-dialog-cancel");
  if (closeBtn) closeBtn.addEventListener("click", closeFilterDialog);
  if (cancelBtn) cancelBtn.addEventListener("click", closeFilterDialog);

  backdrop.addEventListener("click", (e) => {
    if (tagDropdownMenu.style.display !== "none" && !tagDropdownWrapper.contains(e.target)) {
      toggleTagDropdown(false);
    }
    if (e.target === backdrop) {
      closeFilterDialog();
    }
  });

  // Focus query input
  setTimeout(() => queryInput?.focus(), 50);

  // Keydown: Esc to close dropdown first if open, or modal
  function handleKeyDown(e) {
    if (e.key === "Escape") {
      e.preventDefault();
      if (tagDropdownMenu.style.display !== "none") {
        toggleTagDropdown(false);
      } else {
        closeFilterDialog();
      }
    }
  }
  window.addEventListener("keydown", handleKeyDown);
  backdrop._escHandler = handleKeyDown;
}

export function closeFilterDialog() {
  if (activeModal) {
    if (activeModal._escHandler) {
      window.removeEventListener("keydown", activeModal._escHandler);
    }
    activeModal.remove();
    activeModal = null;
  }
}
