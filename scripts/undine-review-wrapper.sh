#!/usr/bin/env bash
# ==============================================================================
# Project UndineD - AI Review Wrapper Script Hook
# ==============================================================================
# Executed by UndineD in the background when an AI patch review is triggered.
#
# Parameters:
#   $1 - Linux kernel patch Message-ID (RFC 5322)
#   $2 - Review git commit hash
#
# Standard Input:
#   The review prompt text is piped via stdin.
#
# Environment Variables:
#   UNDINE_MESSAGE_ID - Same as $1
#   UNDINE_COMMIT_ID  - Same as $2
#   UNDINE_PROMPT     - Prompt text
# ==============================================================================

set -euo pipefail

MSGID="${1:-${UNDINE_MESSAGE_ID:-}}"
COMMIT="${2:-${UNDINE_COMMIT_ID:-}}"

# Read prompt from stdin if available, otherwise fallback to environment or default
if [ ! -t 0 ]; then
    PROMPT="$(cat)"
elif [ -n "${UNDINE_PROMPT:-}" ]; then
    PROMPT="$UNDINE_PROMPT"
else
    PROMPT="Review patch for message ${MSGID} (commit ${COMMIT}). Post diff review comments using the Undine MCP review comment tool."
fi

# Execute AI review command (default: agy --print)
# Users can customize this script to invoke agy, gemini CLI, claude CLI, or local LLMs.
exec agy --print "$PROMPT"
