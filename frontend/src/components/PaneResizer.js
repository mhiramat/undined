/**
 * PaneResizer: Provides smooth draggable height resizing between SummaryPane and ViewerPane
 * with localStorage persistence.
 */

export function initPaneResizer() {
  const resizer = document.getElementById("pane-resizer-h");
  const summaryPane = document.getElementById("summary-pane");
  const mainContent = document.getElementById("main-content");

  if (!resizer || !summaryPane || !mainContent) return;

  // Restore saved height from localStorage
  const savedHeight = localStorage.getItem("undine_summary_height");
  if (savedHeight) {
    const h = parseInt(savedHeight, 10);
    const totalHeight = window.innerHeight;
    if (!isNaN(h) && h >= 100 && h <= totalHeight - 140) {
      summaryPane.style.flex = "none";
      summaryPane.style.height = `${h}px`;
    }
  }

  let isDragging = false;
  let startY = 0;
  let startHeight = 0;

  const startResize = (clientY) => {
    isDragging = true;
    startY = clientY;
    startHeight = summaryPane.getBoundingClientRect().height;
    summaryPane.style.flex = "none";
    resizer.classList.add("dragging");
    document.body.style.cursor = "row-resize";
    document.body.style.userSelect = "none";
  };

  const doResize = (clientY) => {
    if (!isDragging) return;
    const dy = clientY - startY;
    const totalHeight = mainContent.getBoundingClientRect().height;
    const minSummary = 100;
    const minViewer = 140;
    const maxSummary = Math.max(minSummary, totalHeight - minViewer);

    let newHeight = startHeight + dy;
    if (newHeight < minSummary) newHeight = minSummary;
    if (newHeight > maxSummary) newHeight = maxSummary;

    summaryPane.style.flex = "none";
    summaryPane.style.height = `${newHeight}px`;
    localStorage.setItem("undine_summary_height", Math.round(newHeight).toString());
  };

  const endResize = () => {
    if (!isDragging) return;
    isDragging = false;
    resizer.classList.remove("dragging");
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
    // Trigger window resize event so SideDiff and other responsive elements re-align
    window.dispatchEvent(new Event("resize"));
  };

  // Pointer events support with capture for smooth dragging
  resizer.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    startResize(e.clientY);
    try {
      resizer.setPointerCapture(e.pointerId);
    } catch (_) {}
  });

  resizer.addEventListener("pointermove", (e) => {
    if (isDragging) {
      e.preventDefault();
      doResize(e.clientY);
    }
  });

  const onPointerEnd = (e) => {
    if (isDragging) {
      try {
        resizer.releasePointerCapture(e.pointerId);
      } catch (_) {}
      endResize();
    }
  };

  resizer.addEventListener("pointerup", onPointerEnd);
  resizer.addEventListener("pointercancel", onPointerEnd);

  // Fallback mouse listeners
  window.addEventListener("mousemove", (e) => {
    if (isDragging) {
      doResize(e.clientY);
    }
  });

  window.addEventListener("mouseup", () => {
    if (isDragging) {
      endResize();
    }
  });
}
