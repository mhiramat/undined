/**
 * EditorHistory: Manages undo/redo history stack for textarea editor.
 * Preserves cursor selection coordinates and groups rapid typing changes into
 * logical undo steps, while immediately recording discrete actions.
 */
export class EditorHistory {
  constructor(initialValue = "", initialStart = 0, initialEnd = 0, options = {}) {
    this.maxSize = options.maxSize || 300;
    this.groupTimeout = options.groupTimeout || 600; // ms to group continuous typing

    this.stack = [
      {
        value: initialValue,
        selectionStart: initialStart,
        selectionEnd: initialEnd,
      },
    ];
    this.index = 0;

    this.lastActionType = null;
    this.lastRecordTime = Date.now();
  }

  getCurrent() {
    return this.stack[this.index];
  }

  canUndo() {
    return this.index > 0;
  }

  canRedo() {
    return this.index < this.stack.length - 1;
  }

  /**
   * Break the current typing group so that the next change starts a new undo step.
   */
  breakGroup() {
    this.lastRecordTime = 0;
    this.lastActionType = null;
  }

  /**
   * Record a new state in history.
   *
   * @param {string} value
   * @param {number} selectionStart
   * @param {number} selectionEnd
   * @param {string} actionType - 'input', 'delete', 'tab', 'tag', 'erase', 'paste', etc.
   * @param {boolean} forceNew - Force creating a distinct undo step (e.g. Tab, Tag, Paste)
   */
  record(value, selectionStart, selectionEnd, actionType = "input", forceNew = false) {
    const now = Date.now();
    const timeDiff = now - this.lastRecordTime;

    const current = this.getCurrent();
    if (current && current.value === value) {
      // Content didn't change, only update cursor selection of current state
      current.selectionStart = selectionStart;
      current.selectionEnd = selectionEnd;
      return;
    }

    const isTyping = actionType === "input" || actionType === "delete";
    const sameAction = actionType === this.lastActionType;
    const isGroupable = !forceNew && isTyping && sameAction && timeDiff < this.groupTimeout;

    if (isGroupable && this.index > 0 && this.index === this.stack.length - 1) {
      // Update top snapshot in current typing group
      this.stack[this.index] = {
        value,
        selectionStart,
        selectionEnd,
      };
    } else {
      // Discard future redo branch if we are in the middle of history
      if (this.index < this.stack.length - 1) {
        this.stack.splice(this.index + 1);
      }

      this.stack.push({
        value,
        selectionStart,
        selectionEnd,
      });
      this.index = this.stack.length - 1;

      // Enforce maxSize
      if (this.stack.length > this.maxSize) {
        this.stack.shift();
        this.index--;
      }
    }

    this.lastRecordTime = now;
    this.lastActionType = actionType;
  }

  undo() {
    if (!this.canUndo()) {
      return null;
    }
    this.index--;
    this.breakGroup();
    return this.getCurrent();
  }

  redo() {
    if (!this.canRedo()) {
      return null;
    }
    this.index++;
    this.breakGroup();
    return this.getCurrent();
  }

  reset(initialValue = "", initialStart = 0, initialEnd = 0) {
    this.stack = [
      {
        value: initialValue,
        selectionStart: initialStart,
        selectionEnd: initialEnd,
      },
    ];
    this.index = 0;
    this.breakGroup();
  }
}
