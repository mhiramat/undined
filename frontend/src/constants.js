/**
 * Named constants and global shared properties for Project UndineD frontend.
 *
 * Consolidates standard folder names, store notification events, and UI defaults.
 */

// ==============================================================================
// Standard Mailbox Folders
// ==============================================================================
export const FOLDER_B4_IMPORTS = "b4-imports";
export const FOLDER_DRAFTS = "Drafts";
export const FOLDER_TODO = "TODO";
export const FOLDER_OUTBOX = "Outbox";
export const FOLDER_SENT = "Sent";
export const FOLDER_AI_REVIEWS = "AI-reviews";
export const FOLDER_TRASH = "Trash";

export const STANDARD_FOLDERS = [
  FOLDER_B4_IMPORTS,
  FOLDER_DRAFTS,
  FOLDER_TODO,
  FOLDER_SENT,
  FOLDER_AI_REVIEWS,
];

// ==============================================================================
// Store Event Names
// ==============================================================================
export const EVENT_CONFIG_LOADED = "config_loaded";
export const EVENT_FOLDERS_UPDATED = "folders_updated";
export const EVENT_FOLDER_SELECTED = "folder_selected";
export const EVENT_FOLDER_SYNC_STARTED = "folder_sync_started";
export const EVENT_FOLDER_SYNC_FINISHED = "folder_sync_finished";
export const EVENT_FOLDER_SYNC_PROGRESS = "folder_sync_progress";

export const EVENT_MESSAGES_UPDATED = "messages_updated";
export const EVENT_MESSAGE_SELECTED = "message_selected";
export const EVENT_MESSAGE_READ_UPDATED = "message_read_updated";
export const EVENT_MESSAGE_REPLIED_UPDATED = "message_replied_updated";
export const EVENT_MESSAGE_TAG_UPDATED = "message_tag_updated";
export const EVENT_MESSAGE_DETAIL_LOADED = "message_detail_loaded";

export const EVENT_FILTER_UPDATED = "filter_updated";
export const EVENT_NOTES_UPDATED = "notes_updated";
export const EVENT_AI_REVIEW_STATE_CHANGED = "ai_review_state_changed";
export const EVENT_WIDTH_RESIZED = "width_resized";
