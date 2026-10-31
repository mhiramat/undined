#!/usr/bin/env bash
# ==============================================================================
# Project UndineD - agy CLI Skill & MCP Server Installer
# ==============================================================================
# Installs or links the .agents/skills/undine-review and undine-mcp-review skills
# and registers Undine's MCP server into the agy CLI for prompt-only (oneshot) review workflows.
# ==============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Defaults
MODE="link"
SCOPE="user"
SKILL_TARGET="all"
CONFIGURE_MCP=true
AUTO_TRUST=true
QUIET=false

show_help() {
    cat <<EOF
Usage: $(basename "$0") [OPTIONS]

Install or link Undine's Linux kernel patch review skill into agy CLI
for autonomous prompt-only (oneshot) reviews.

Options:
  --link               Link skill directory (changes in repo apply immediately; default)
  --copy, --install    Copy skill into configuration directory
  --skill <name|all>   Target skill to install: "undine-review", "undine-mcp-review", or "all" (default: "all")
  --scope <scope>      Installation scope: "user" (default: ~/.gemini/config/skills) or "workspace"
  --skip-mcp           Only install/link the skill; do not configure Undine MCP server
  --no-trust           Do not add project directory to ~/.gemini/trustedFolders.json
  -q, --quiet          Suppress banner and informational messages
  -h, --help           Show this help message and exit

Prompt-Only (Oneshot) Review Usage:
  agy --print "Review patch for message <message_id> (commit <commit_id>). Post diff review comments using the Undine MCP review comment tool."

Undine Configuration (~/.config/undined/config.toml):
  [review]
  command = "agy --print"
EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --link)
            MODE="link"
            shift
            ;;
        --copy|--install)
            MODE="copy"
            shift
            ;;
        --skill)
            if [[ -z "${2:-}" ]]; then
                echo "Error: --skill requires an argument ('all', 'undine-review', or 'undine-mcp-review')" >&2
                exit 1
            fi
            SKILL_TARGET="$2"
            shift 2
            ;;
        --scope)
            if [[ -z "${2:-}" ]]; then
                echo "Error: --scope requires an argument ('user' or 'workspace')" >&2
                exit 1
            fi
            SCOPE="$2"
            shift 2
            ;;
        --skip-mcp)
            CONFIGURE_MCP=false
            shift
            ;;
        --no-trust)
            AUTO_TRUST=false
            shift
            ;;
        -q|--quiet)
            QUIET=true
            shift
            ;;
        -h|--help)
            show_help
            exit 0
            ;;
        *)
            echo "Error: Unknown option '$1'" >&2
            echo "Run '$(basename "$0") --help' for usage." >&2
            exit 1
            ;;
    esac
done

if [ "$QUIET" = false ]; then
    echo "================================================================================"
    echo " Project UndineD - agy CLI Skill & MCP Installer (Prompt-Only Mode)"
    echo "================================================================================"
fi

# 1. Verify agy CLI is installed
if ! command -v agy >/dev/null 2>&1; then
    echo "Error: 'agy' CLI command not found in PATH." >&2
    echo "Please ensure agy (Antigravity CLI) is installed and available in PATH." >&2
    exit 1
fi

AGY_PATH="$(command -v agy)"
if [ "$QUIET" = false ]; then
    echo "✓ Found agy CLI: $AGY_PATH"
fi

# 2. Discover skills to install
SKILLS_TO_INSTALL=()
if [ "$SKILL_TARGET" = "all" ]; then
    for dir in "$PROJECT_ROOT/.agents/skills"/*; do
        if [ -d "$dir" ] && [ -f "$dir/SKILL.md" ]; then
            SKILLS_TO_INSTALL+=("$dir")
        fi
    done
else
    target_dir="$PROJECT_ROOT/.agents/skills/$SKILL_TARGET"
    if [ -d "$target_dir" ] && [ -f "$target_dir/SKILL.md" ]; then
        SKILLS_TO_INSTALL+=("$target_dir")
    else
        echo "Error: Skill '$SKILL_TARGET' not found at $target_dir/SKILL.md" >&2
        exit 1
    fi
fi

if [ ${#SKILLS_TO_INSTALL[@]} -eq 0 ]; then
    echo "Error: No agent skills found in $PROJECT_ROOT/.agents/skills" >&2
    exit 1
fi

# 3. Locate or build Undine executable
UNDINED_BIN=""
if [ -x "$PROJECT_ROOT/bin/undined" ]; then
    UNDINED_BIN="$PROJECT_ROOT/bin/undined"
elif [ -f "$PROJECT_ROOT/main.go" ] || [ -d "$PROJECT_ROOT/cmd/undined" ]; then
    if command -v go >/dev/null 2>&1; then
        if [ "$QUIET" = false ]; then
            echo "Building UndineD binary..."
        fi
        (cd "$PROJECT_ROOT" && go build -tags "fts5" -o bin/undined ./cmd/undined) || true
        if [ -x "$PROJECT_ROOT/bin/undined" ]; then
            UNDINED_BIN="$PROJECT_ROOT/bin/undined"
        fi
    fi
fi

if [ -z "$UNDINED_BIN" ] && command -v undined >/dev/null 2>&1; then
    UNDINED_BIN="$(command -v undined)"
fi

# Fallback: check Python backend
PYTHON_BIN=""
if [ -z "$UNDINED_BIN" ]; then
    if [ -x "$PROJECT_ROOT/.venv/bin/python" ]; then
        PYTHON_BIN="$PROJECT_ROOT/.venv/bin/python"
    elif [ -n "${VIRTUAL_ENV:-}" ] && [ -x "$VIRTUAL_ENV/bin/python" ]; then
        PYTHON_BIN="$VIRTUAL_ENV/bin/python"
    elif command -v python3 >/dev/null 2>&1; then
        PYTHON_BIN="$(command -v python3)"
    fi
fi

if [ "$QUIET" = false ]; then
    if [ -n "$UNDINED_BIN" ]; then
        echo "✓ Undine executable: $UNDINED_BIN"
    elif [ -n "$PYTHON_BIN" ]; then
        echo "✓ Python interpreter: $PYTHON_BIN"
    else
        echo "Warning: Neither undined binary nor python interpreter found."
    fi
fi

# 4. Determine target skills directory
if [ "$SCOPE" = "workspace" ]; then
    TARGET_SKILLS_DIR="$PROJECT_ROOT/.agents/skills"
else
    # Install under ~/.gemini/config/skills for compatibility with Antigravity and Antigravity CLI
    TARGET_SKILLS_DIR="${AGY_SKILLS_DIR:-$HOME/.gemini/config/skills}"
fi
mkdir -p "$TARGET_SKILLS_DIR"

# Install or link skills
for skill_dir in "${SKILLS_TO_INSTALL[@]}"; do
    skill_name="$(basename "$skill_dir")"
    dest_path="$TARGET_SKILLS_DIR/$skill_name"
    resolved_src="$(cd -P "$skill_dir" && pwd)"
    resolved_dest_parent="$(cd -P "$TARGET_SKILLS_DIR" && pwd)"
    resolved_dest="$resolved_dest_parent/$skill_name"

    if [ "$resolved_src" = "$resolved_dest" ]; then
        if [ "$QUIET" = false ]; then
            echo "Skill '$skill_name' is already present in $TARGET_SKILLS_DIR (scope: $SCOPE)."
        fi
    else
        if [ "$MODE" = "copy" ]; then
            if [ "$QUIET" = false ]; then
                echo "Installing skill '$skill_name' via copy (scope: $SCOPE)..."
            fi
            rm -rf "$dest_path"
            cp -r "$skill_dir" "$dest_path"
        else
            if [ "$QUIET" = false ]; then
                echo "Linking skill '$skill_name' (scope: $SCOPE)..."
            fi
            ln -sfn "$skill_dir" "$dest_path"
        fi
    fi

    # Also maintain compatibility with Antigravity CLI directory if present
    if [ "$SCOPE" = "user" ] && [ -z "${AGY_SKILLS_DIR:-}" ] && [ -d "$HOME/.gemini/antigravity-cli" ]; then
        cli_skills_dir="$HOME/.gemini/antigravity-cli/skills"
        if [ "$cli_skills_dir" != "$TARGET_SKILLS_DIR" ]; then
            mkdir -p "$cli_skills_dir"
            cli_dest="$cli_skills_dir/$skill_name"
            if [ "$MODE" = "copy" ]; then
                rm -rf "$cli_dest"
                cp -r "$skill_dir" "$cli_dest"
            else
                ln -sfn "$skill_dir" "$cli_dest"
            fi
        fi
    fi
done

# 5. Configure Undine MCP Server in agy (if enabled)
if [ "$CONFIGURE_MCP" = true ]; then
    if [ "$QUIET" = false ]; then
        echo "Configuring Undine MCP server in agy..."
    fi

    if [ -n "$UNDINED_BIN" ]; then
        agy mcp add undine -- "$UNDINED_BIN" --mcp-stdio
    elif [ -n "$PYTHON_BIN" ]; then
        agy mcp add --env "PYTHONPATH=$PROJECT_ROOT" undine "$PYTHON_BIN" -m backend.mcp.server
    else
        echo "Warning: Could not configure MCP server: no undined binary or python available" >&2
    fi
fi

# 6. Ensure project root is registered in trustedFolders.json (if enabled)
if [ "$AUTO_TRUST" = true ]; then
    TRUST_FILE="$HOME/.gemini/trustedFolders.json"
    if [ -d "$HOME/.gemini" ]; then
        PY_EXE="${PYTHON_BIN:-$(command -v python3 || echo "")}"
        if [ -n "$PY_EXE" ]; then
            "$PY_EXE" -c '
import json, os, sys
p = os.path.expanduser(sys.argv[1])
target = sys.argv[2]
try:
    if os.path.exists(p):
        with open(p, "r", encoding="utf-8") as f:
            data = json.load(f)
    else:
        data = {}
    if data.get(target) != "TRUST_FOLDER":
        data[target] = "TRUST_FOLDER"
        with open(p, "w", encoding="utf-8") as f:
            json.dump(data, f, indent=2)
except Exception:
    pass
' "$TRUST_FILE" "$PROJECT_ROOT" 2>/dev/null || true
        fi
    fi
fi

if [ "$QUIET" = false ]; then
    echo ""
    echo "--------------------------------------------------------------------------------"
    echo " Verification & Discovered Configuration"
    echo "--------------------------------------------------------------------------------"
    echo "Installed skills in $TARGET_SKILLS_DIR:"
    for skill_dir in "${SKILLS_TO_INSTALL[@]}"; do
        skill_name="$(basename "$skill_dir")"
        if [ -e "$TARGET_SKILLS_DIR/$skill_name" ]; then
            echo "  ✓ $skill_name -> $(readlink -f "$TARGET_SKILLS_DIR/$skill_name" 2>/dev/null || echo "$TARGET_SKILLS_DIR/$skill_name")"
        fi
    done
    echo ""
    if [ "$CONFIGURE_MCP" = true ]; then
        agy mcp list || true
        echo ""
    fi
    echo "================================================================================"
    echo " Installation Complete!"
    echo "================================================================================"
    echo ""
    echo " Prompt-Only (Oneshot) Review Execution:"
    echo "   agy --print \"Review patch for message <msgid> (commit <commit_id>). Post diff review comments using the Undine MCP review comment tool.\""
    echo ""
    echo " Configure UndineD background reviewer in ~/.config/undined/config.toml:"
    echo "   [review]"
    echo "   command = \"agy --print\""
    echo ""
fi
