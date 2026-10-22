# Patch Integrity and Corrupted Diff Policy

This document defines UndineD's policy on patch diff integrity, the detection and handling of corrupted patch emails, and the rationale against silent whitespace/Unicode normalization.

---

## 1. Executive Summary

In Linux kernel development, patch emails sent over the Linux Kernel Mailing List (LKML) and subsystem lists must adhere to strict plain-text formatting standards. 

When contributors submit patches using webmail interfaces (e.g., QQ Mail, Outlook Web Access) or rich-text email editors, mail user agents frequently alter the whitespace formatting—converting standard ASCII spaces (`0x20`) into non-breaking spaces (NBSP: `U+00A0`, UTF-8 `0xC2 0xA0`) or en-spaces (`U+2002`, UTF-8 `0xE2 0x80 0x82`), or corrupting context line prefixes.

**UndineD Policy**:
1. **Zero Silent Normalization**: UndineD will **never** silently sanitize, alter, or normalize non-ASCII characters within patch diff lines to make them parse as valid diffs.
2. **Explicit Corruption Marking**: Any email containing non-ASCII characters (`code point > 127`) within its diff lines is marked as **corrupted** (`is_corrupted = true`).
3. **Rejection of SideDiff**: Corrupted patches are rejected from Side-by-Side diff rendering (`SideDiff`).
4. **Prominent User Warning**: The UI displays a warning banner stating: **"This patch is corrupted."** along with the specific Unicode character detected.
5. **Safe Plaintext Display with Escaped Unicode**: The email is displayed as raw plain text with non-ASCII characters safely escaped into visually identifiable badges (e.g. `<U+00A0>`).
6. **Application Guard**: "Apply with git am" is disabled for corrupted patches to prevent broken patches from entering local Git repositories.

---

## 2. Background: How Patches Become Corrupted

Unified diff format (RFC 3676 / Git diff) relies on exact first-character semantics:
* `' '` (ASCII space, `0x20`): Context line present in both pre-image and post-image.
* `'+'` (ASCII plus, `0x2B`): Added line in post-image.
* `'-'` (ASCII minus, `0x2D`): Deleted line in pre-image.
* `'\'` (ASCII backslash, `0x5C`): Git metadata indicator (e.g. `\ No newline at end of file`).
* `'\t'` (ASCII tab, `0x09`): Standard code indentation.

When a patch is pasted into a web browser form or processed by webmail servers:
1. Multiple consecutive spaces are automatically converted into non-breaking spaces (`&nbsp;` / `\u00a0`) or en-spaces (`\u2002`) to prevent HTML whitespace collapsing.
2. The initial column context space `' '` is often transformed into `\u00a0` (UTF-8 bytes `0xC2 0xA0`).
3. When received over SMTP/IMAP, diff parsers reading the first byte encounter `0xC2` instead of `' '`, causing the parser to treat the line as neither context, addition, nor deletion.

---

## 3. Rationale: Why Silent Normalization is Prohibited

It is tempting for an email client or diff viewer to silently replace `\u00a0` with an ASCII space `' '` to force the patch into rendering. UndineD strictly rejects this approach for the following critical reasons:

### 3.1 Codebase Hygiene & Build Invariants
The Linux kernel codebase requires clean ASCII source files (with rare exceptions for international datasets or author names in documentation). Introducing non-breaking spaces into C source files causes:
* Obscure compiler errors or preprocessor tokenization failures.
* Failures in `scripts/checkpatch.pl` (flagging invalid characters and space-before-tab warnings).
* Breakages in automated build bots and CI pipelines (e.g. KernelCI, 0-DAY CI).

### 3.2 Security & Supply-Chain Integrity (Trojan Source Defense)
Non-ASCII characters in source code represent a known attack vector (e.g., CVE-2021-42574 / Trojan Source, homoglyph attacks, zero-width space smuggling).
* Silently normalizing Unicode whitespace masks potential tampering or formatting attacks.
* Explicitly escaping non-ASCII characters exposes invisible glyphs to maintainers before any code execution or application occurs.

### 3.3 Fidelity to Transmitted Bytes & Reviewer Honesty
An email workstation for kernel maintainers must reflect the true state of the message as transmitted across the wire.
* If UndineD displayed a corrupted patch as a clean, working Side-by-Side diff, a reviewer might review and approve it.
* However, when other subsystem maintainers, Linus Torvalds, or automated tools try to apply the patch from LKML via `git am` or `b4 am`, the command will fail with `patch does not apply` or whitespace errors.
* Displaying the email in plaintext with highlighted `<U+XXXX>` badges ensures the reviewer immediately sees what failed and why.

### 3.4 Contributor Education & Workflow Correction
New contributors frequently do not realize that their webmail client mangles patches.
* By identifying the exact character (e.g., `non-ASCII character detected in patch diff (U+00A0)`), the maintainer can quickly explain to the contributor why their patch cannot be reviewed or applied and advise them to use `git send-email`.

---

## 4. Policy Rules & Architecture

### 4.1 Permitted Unicode Contexts vs. Diff Lines
* **Permitted in Preamble/Commit Message**: Linux kernel commit preambles regularly contain non-ASCII Unicode characters in author names, attributions, or foreign quotes (e.g. `Signed-off-by: Björn Developer <dev@example.com>`). These characters are valid in Git commit headers and do **not** trigger patch corruption.
* **Prohibited in Diff Lines**: Once the patch diff begins (`diff --git`, `--- a/`, `+++ b/`, or `@@`), every line until the signature postscript (`-- `) must consist strictly of valid ASCII characters (`r <= 127`). Any rune with `r > 127` flags the patch as corrupted.

### 4.2 Handling Corrupted Patches in UndineD

```
                          Patch Email Received
                                   │
                     ┌─────────────┴─────────────┐
                     ▼                           ▼
          Preamble (Commit Log)           Diff Section
             [Unicode Allowed]           [ASCII-Strict]
                     │                           │
                     │                 Does any line have r > 127?
                     │                           │
                     │                  ┌────────┴────────┐
                     │                  │ YES             │ NO
                     │                  ▼                 ▼
                     │           is_corrupted=true    Normal Patch
                     │                  │                 │
                     └──────────┬───────┘                 │
                                ▼                         ▼
                        [ViewerPane Render]        [SideDiff Render]
                        • Show Warning Banner       • Dual Column Table
                        • Reject SideDiff           • Gutter annotations
                        • Disable 'git am'          • Apply via git am
                        • Escape Unicode Badges
                          (<U+00A0>, <U+2002>)
```

### 4.3 UI Presentation Invariants
1. **Warning Banner**:
   ```
   ⚠️ This patch is corrupted.
   non-ASCII character detected in patch diff (U+00A0). SideDiff has been disabled and non-ASCII characters are highlighted below.
   ```
2. **Escaped Badge Formatting**:
   Every non-ASCII character in the corrupted message is escaped to a badge:
   ```html
   <span class="unicode-escape" title="Unicode U+00A0">&lt;U+00A0&gt;</span>
   ```
   Rendered with a subtle reddish alert background (`--diff-del-bg`) and mono font, making spaces visible and copyable without executing as raw bytes.
3. **Application Guard**:
   The `[ 📥 Apply with git am ]` button is replaced with a disabled `[ ⚠️ Corrupted patch ]` button, preventing accidental checkout or branch application.

---

## 5. Summary for AI Agents & Developers

* When developing or reviewing tools for UndineD, **never** introduce automatic normalization or stripping of non-ASCII characters from patch diffs.
* Always preserve the raw byte stream of received emails.
* Always verify diff validity and flag corrupted patches early in the parsing pipeline.
