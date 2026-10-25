/**
 * TypeScript definitions for UndineD REST entities and payloads.
 */

export interface Folder {
  id: number;
  name: string;
  is_subscribed?: boolean;
  is_system?: boolean;
  sync_enabled?: boolean;
  is_imap?: boolean;
  last_synced_uid?: number;
  unread_count?: number;
  total_count?: number;
}

export interface SyncFolderResult {
  status: string;
  folder_id: number;
  folder_name: string;
  synced_count: number;
  removed_count: number;
}

export interface SyncTodoResult {
  synced_count: number;
  removed_count: number;
  total_delegated: number;
  patches?: any[];
}

export interface FolderSyncStatus {
  folder_id?: number;
  folder_name?: string;
  is_syncing: boolean;
  synced: number;
  total: number;
  removed?: number;
  status: string;
  error?: string;
}

export interface MessageSummary {
  id: number;
  message_id: string;
  subject: string;
  from_addr: string;
  from_name: string;
  date: string;
  is_patch: boolean;
  is_read: boolean;
  is_replied: boolean;
  tag?: string;
  in_reply_to?: string;
  references?: string;
  has_notes?: boolean;
  is_ai_review?: boolean;
}

export interface DiffCell {
  line_no: number | null;
  content: string;
  type: "ctx" | "del" | "add" | "empty";
}

export interface PairedRow {
  row_idx: number;
  left: DiffCell;
  right: DiffCell;
}

export interface UnifiedLine {
  type: "ctx" | "add" | "del" | "header";
  old_no: number | null;
  new_no: number | null;
  content: string;
}

export interface Hunk {
  header: string;
  old_start: number;
  old_count: number;
  new_start: number;
  new_count: number;
  section?: string;
  paired_rows: PairedRow[];
  unified_lines: UnifiedLine[];
}

export interface DiffFile {
  old_path: string;
  new_path: string;
  hunks: Hunk[];
}

export interface ParsedEmailDiff {
  is_patch: boolean;
  preamble: string;
  files: DiffFile[];
  postscript: string;
}

export interface PatchComment {
  id?: number;
  message_id?: string;
  file_path: string;
  file?: string;
  line_no: number;
  lineNo?: number;
  side: "new" | "old" | "left" | "right";
  text: string;
  state?: "draft" | "submitted";
  author?: string;
  created_at?: string;
}

export interface MessageDetail {
  message: MessageSummary & {
    body_plain: string;
    body_html?: string;
    to_addr: string;
    cc_addr: string;
    file_path: string;
  };
  comments?: PatchComment[];
  parsed_diff?: ParsedEmailDiff;
  reviewed_message?: {
    id: number;
    message_id: string;
    folder_id?: number;
    folder_name?: string;
    subject: string;
  };
  parent_info?: {
    found: boolean;
    folder_name?: string;
    parent_id?: number;
  };
}

export interface RawHeaderResponse {
  message_id: string;
  raw_headers: string;
}

export interface PatchworkUser {
  id: number;
  username: string;
}

export interface PatchworkProject {
  id: number;
  name: string;
  link_name: string;
}

export interface PatchworkPatch {
  id: number;
  msgid: string;
  name: string;
  state: string;
  delegate: PatchworkUser | null;
  checks: string;
  project?: PatchworkProject;
  local_message_id?: number | null;
}

export interface PatchworkCheck {
  id: number;
  state: "pending" | "success" | "warning" | "fail";
  target_url: string;
  context: string;
  description: string;
}

export interface B4ImportResult {
  success: boolean;
  message_id: string;
  imported_count: number;
  indexed_ids: number[];
}

export interface PatchApplyResult {
  success: boolean;
  branch: string;
  head: string;
  original_branch?: string;
  head_before?: string;
  error?: string;
  detail?: string;
}

export interface QuotePreviewResult {
  quote_text: string;
  body?: string;
  quote?: string;
}

export interface SendEmailPayload {
  to_addr: string;
  cc_addr?: string | null;
  subject: string;
  body_text: string;
  in_reply_to?: string | null;
  references?: string | null;
  orig_msg_id?: string | null;
  draft_id?: number | null;
}

export interface SaveDraftPayload {
  to_addr: string;
  cc_addr?: string | null;
  subject: string;
  body_text: string;
  in_reply_to?: string | null;
  references?: string | null;
  custom_from?: string | null;
  draft_id?: number | null;
}

export interface KnownAddress {
  id: number;
  name: string;
  address: string;
  frequency: number;
  last_seen: string;
  created_at?: string;
}

