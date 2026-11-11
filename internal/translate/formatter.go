// Package translate formats hard-wrapped LKML emails into natural sentences
// and translates them via the local-jp-translate REST API.
package translate

import (
	"regexp"
	"strings"
	"unicode"
)

// SegmentKind classifies a block of lines in an email body.
type SegmentKind int

const (
	// SegmentBlank represents one or more empty lines (possibly inside a quote block).
	SegmentBlank SegmentKind = iota
	// SegmentProse represents a natural-language paragraph or list item (unquoted or quoted)
	// whose hard-wrapped lines have been unwrapped into natural sentences.
	SegmentProse
	// SegmentVerbatim represents code blocks, commit trailers, diff sections, or signatures
	// that must not be line-unwrapped or translated.
	SegmentVerbatim
)

// Segment represents a structural unit of an email body.
type Segment struct {
	Kind        SegmentKind
	QuoteDepth  int
	QuotePrefix string // Normalized prefix, e.g. "", "> ", ">> "
	Text        string // Unwrapped prose text (without QuotePrefix) or raw verbatim lines
}

var (
	// invisibleEmailPadding matches preheader filler runes used in marketing/automated emails.
	invisibleEmailPadding = regexp.MustCompile(`[\x{2007}\x{200b}\x{034f}\x{feff}]+`)

	// listBulletRe matches bullet or numbered list item markers at the start of a line (after quote prefix).
	listBulletRe = regexp.MustCompile(`^(\s*(?:[-*+]|\d+[.)])\s+)(.*)$`)

	// trailerRe matches standard Linux kernel commit/email trailers and git log headers.
	trailerRe = regexp.MustCompile(`^(?i)(Signed-off-by|Reviewed-by|Acked-by|Tested-by|Reported-by|Suggested-by|Co-developed-by|Fixes|Link|Closes|Cc|Message-ID|Change-Id|Author|CommitDate|AuthorDate):\s+\S+`)

	// commitHeaderRe matches "commit <sha>" lines in git log / review emails.
	commitHeaderRe = regexp.MustCompile(`^commit\s+[0-9a-f]{12,40}$`)

	// attributionStartRe matches the start of an RFC quote attribution line ("On ... wrote:").
	attributionStartRe = regexp.MustCompile(`^(?:On\s+.+|.+<\S+@\S+>.+wrote:\s*)$`)

	// diffHeaderRe matches unified diff or git diff headers.
	diffHeaderRe = regexp.MustCompile(`^(?:diff --git\s+|index [0-9a-f]{7,}\.\.[0-9a-f]{7,}|---\s+\S+|\+\+\+\s+\S+|@@\s+-\d+|new file mode\s+\d+|deleted file mode\s+\d+|similarity index\s+\d+%|rename (?:from|to)\s+\S+|Binary files\s+.+\s+differ|base-commit:\s+[0-9a-f]+)`)

	// diffstatLineRe matches git diffstat summary lines (e.g. " kernel/trace.c | 4 ++--" or " 1 file changed, 2 insertions(+)").
	diffstatLineRe = regexp.MustCompile(`^\s+(?:\S+\s+\|\s+(?:\d+|Bin\b).*|\d+\s+files?\s+changed\b.*)$`)
)

// CleanEmailText normalizes line endings and strips invisible preheader padding characters.
func CleanEmailText(raw string) string {
	if raw == "" {
		return ""
	}
	s := strings.ReplaceAll(raw, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = invisibleEmailPadding.ReplaceAllString(s, "")
	s = strings.TrimRight(s, "\n")
	return s
}

// parseQuotePrefix extracts the quote depth and the remainder of the line after leading '>' markers.
func parseQuotePrefix(line string) (depth int, normalizedPrefix string, rest string) {
	i := 0
	n := len(line)
	for i < n {
		// Allow optional single space before '>' only if we haven't seen non-space
		if line[i] == '>' {
			depth++
			i++
			// Consume at most one optional space immediately following '>'
			if i < n && line[i] == ' ' {
				i++
			}
		} else if line[i] == ' ' && depth > 0 {
			// Stop consuming spaces so indentation inside the quote is preserved in rest
			break
		} else {
			break
		}
	}
	if depth == 0 {
		return 0, "", line
	}
	return depth, strings.Repeat(">", depth) + " ", line[i:]
}

func isIndentedCodeLine(rest string) bool {
	if strings.HasPrefix(rest, "\t") {
		return true
	}
	spaces := 0
	for _, r := range rest {
		if r == ' ' {
			spaces++
		} else {
			break
		}
	}
	return spaces >= 2
}

func isFenceLine(rest string) bool {
	trimmed := strings.TrimSpace(rest)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func isTableOrASCIIArtLine(rest string) bool {
	trimmed := strings.TrimSpace(rest)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") {
		return true
	}
	if strings.HasPrefix(trimmed, "+--") || strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "===") {
		return true
	}
	if trimmed == "[ ... ]" || trimmed == "[...]" || trimmed == "..." {
		return true
	}
	return false
}

// isUnifiedDiffBodyLine returns true when rest (after stripping '>' quote markers)
// is a unified diff addition/deletion line (e.g. "+static void...", "+\t...", "-old_call();", "+", "-").
func isUnifiedDiffBodyLine(rest string) bool {
	if rest == "+" || rest == "-" || strings.HasPrefix(rest, `\ No newline`) {
		return true
	}
	if len(rest) >= 2 && (rest[0] == '+' || rest[0] == '-') {
		// Note: "- item" or "+ item" with a single space can be a bullet list item outside a diff hunk,
		// whereas "+code", "+\tcode", "+  code", "-code", "-\tcode", "-  code" are diff lines.
		second := rest[1]
		if second != ' ' {
			return true
		}
		if len(rest) >= 3 && rest[2] == ' ' {
			return true
		}
	}
	return false
}

// isCodeSnippetBoundaryLine detects unindented C function/block header or closing brace lines
// such as "kernel/kprobes.c:__arm_kprobe() {" or "}" in plain-text review emails.
func isCodeSnippetBoundaryLine(trimmed string) bool {
	if trimmed == "{" || trimmed == "}" || trimmed == "};" {
		return true
	}
	if strings.HasSuffix(trimmed, ") {") || strings.HasSuffix(trimmed, "){") {
		return true
	}
	return false
}

func isAttributionEnd(rest string) bool {
	trimmed := strings.TrimSpace(rest)
	return strings.HasSuffix(trimmed, "wrote:") || strings.HasSuffix(trimmed, "writes:") || strings.HasSuffix(trimmed, "said:")
}

// joinUnwrappedLine appends nextPart to current unwrapped prose, handling hyphenation and URLs cleanly.
func joinUnwrappedLine(curr, nextPart string) string {
	nextTrim := strings.TrimSpace(nextPart)
	if nextTrim == "" {
		return curr
	}
	if curr == "" {
		return nextTrim
	}
	return curr + " " + nextTrim
}

// ParseEmailSegments parses an email body into ordered segments, unwrapping 80-column hard-wrapped
// prose paragraphs and quoted replies while keeping code blocks, trailers, signatures, and diffs verbatim.
func ParseEmailSegments(raw string) []Segment {
	cleaned := CleanEmailText(raw)
	if strings.TrimSpace(cleaned) == "" {
		return make([]Segment, 0)
	}

	lines := strings.Split(cleaned, "\n")
	segments := make([]Segment, 0)

	var (
		inFence       bool
		inTrailingSig bool
		inDiffHunk    bool
		diffHunkDepth int
		activeProse   *Segment
		inListItem    bool
		inAttribution bool
	)

	flushProse := func() {
		if activeProse != nil {
			segments = append(segments, *activeProse)
			activeProse = nil
		}
		inListItem = false
		inAttribution = false
	}

	appendVerbatim := func(depth int, prefix string, rawLine string) {
		flushProse()
		if len(segments) > 0 && segments[len(segments)-1].Kind == SegmentVerbatim && segments[len(segments)-1].QuoteDepth == depth {
			segments[len(segments)-1].Text += "\n" + rawLine
			return
		}
		segments = append(segments, Segment{
			Kind:        SegmentVerbatim,
			QuoteDepth:  depth,
			QuotePrefix: prefix,
			Text:        rawLine,
		})
	}

	for idx := 0; idx < len(lines); idx++ {
		line := lines[idx]
		depth, prefix, rest := parseQuotePrefix(line)
		trimmedRest := strings.TrimSpace(rest)

		// Once inside a trailing unquoted signature ("-- ") at the end of the email, keep remaining lines verbatim.
		if inTrailingSig && depth == 0 {
			appendVerbatim(0, "", line)
			continue
		}

		// Detect unquoted signature delimiter "-- " or mid-message "--" divider
		if depth == 0 && !inFence {
			if rest == "-- " || rest == "--" {
				if rest == "-- " && looksLikeTrailingSignature(lines[idx+1:]) {
					inTrailingSig = true
				}
				appendVerbatim(0, "", line)
				continue
			}
		}

		// Check fenced code block toggle
		if isFenceLine(rest) {
			inFence = !inFence
			appendVerbatim(depth, prefix, line)
			continue
		}
		if inFence {
			appendVerbatim(depth, prefix, line)
			continue
		}

		// Blank line
		if trimmedRest == "" {
			flushProse()
			if inDiffHunk && depth < diffHunkDepth {
				inDiffHunk = false
			}
			segments = append(segments, Segment{
				Kind:        SegmentBlank,
				QuoteDepth:  depth,
				QuotePrefix: strings.TrimRight(prefix, " "),
				Text:        "",
			})
			continue
		}

		// Diff headers, diffstat lines, or unified diff body lines (+..., -...) at any quote depth
		if diffHeaderRe.MatchString(trimmedRest) || diffstatLineRe.MatchString(rest) || isUnifiedDiffBodyLine(rest) {
			inDiffHunk = true
			diffHunkDepth = depth
			appendVerbatim(depth, prefix, line)
			continue
		}

		// Inside a diff hunk (quoted or unquoted), context lines starting with space/tab or "[ ... ]" stay verbatim
		if inDiffHunk && depth == diffHunkDepth {
			if strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t") || strings.HasPrefix(rest, "+") || strings.HasPrefix(rest, "-") || trimmedRest == "[ ... ]" || trimmedRest == "[...]" {
				appendVerbatim(depth, prefix, line)
				continue
			}
			inDiffHunk = false
		} else if inDiffHunk && depth != diffHunkDepth {
			inDiffHunk = false
		}

		// Verbatim single lines: trailers, commit SHA headers, code block boundaries, ASCII tables
		if trailerRe.MatchString(trimmedRest) || commitHeaderRe.MatchString(trimmedRest) || isCodeSnippetBoundaryLine(trimmedRest) || isTableOrASCIIArtLine(rest) {
			appendVerbatim(depth, prefix, line)
			continue
		}

		// Check bullet / numbered list item
		if listBulletRe.MatchString(rest) {
			flushProse()
			activeProse = &Segment{
				Kind:        SegmentProse,
				QuoteDepth:  depth,
				QuotePrefix: prefix,
				Text:        strings.TrimRightFunc(rest, unicode.IsSpace),
			}
			inListItem = true
			continue
		}

		// Indented line: if we are inside a list item at the same quote depth, unwrap as continuation;
		// otherwise treat as verbatim indented code/stack trace/log block.
		if isIndentedCodeLine(rest) {
			if inListItem && activeProse != nil && activeProse.QuoteDepth == depth {
				activeProse.Text = joinUnwrappedLine(activeProse.Text, trimmedRest)
				continue
			}
			appendVerbatim(depth, prefix, line)
			continue
		}

		// Quote depth changed -> flush active prose paragraph
		if activeProse != nil && activeProse.QuoteDepth != depth {
			flushProse()
		}

		// Attribution header ("On Mon, ... wrote:")
		if activeProse == nil && attributionStartRe.MatchString(trimmedRest) {
			activeProse = &Segment{
				Kind:        SegmentProse,
				QuoteDepth:  depth,
				QuotePrefix: prefix,
				Text:        trimmedRest,
			}
			if isAttributionEnd(trimmedRest) {
				flushProse()
			} else {
				inAttribution = true
			}
			continue
		}

		if inAttribution && activeProse != nil {
			activeProse.Text = joinUnwrappedLine(activeProse.Text, trimmedRest)
			if isAttributionEnd(trimmedRest) {
				flushProse()
			}
			continue
		}

		// Standard prose line at current quote depth
		if activeProse == nil {
			activeProse = &Segment{
				Kind:        SegmentProse,
				QuoteDepth:  depth,
				QuotePrefix: prefix,
				Text:        trimmedRest,
			}
		} else {
			activeProse.Text = joinUnwrappedLine(activeProse.Text, trimmedRest)
		}

		// If this line ends an attribution header ("... wrote:"), flush it so the next line starts fresh
		if isAttributionEnd(trimmedRest) && strings.HasPrefix(activeProse.Text, "On ") {
			flushProse()
		}
	}

	flushProse()
	return segments
}

// looksLikeTrailingSignature checks whether the lines after a "-- " line are a short
// email footer/signature at the end of the message (rather than a mid-message divider).
func looksLikeTrailingSignature(remaining []string) bool {
	if len(remaining) > 10 {
		return false
	}
	for _, l := range remaining {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "diff --git ") {
			return false
		}
	}
	return true
}

// looksLikePatchSeparator inspects lines following a "---" line to determine whether
// it is a Git patch separator (followed by diffstat, diff --git, or base-commit).
func looksLikePatchSeparator(remaining []string) bool {
	limit := len(remaining)
	if limit > 40 {
		limit = 40
	}
	for i := 0; i < limit; i++ {
		l := strings.TrimSpace(remaining[i])
		if strings.HasPrefix(l, "diff --git ") ||
			strings.HasPrefix(l, "--- a/") ||
			strings.HasPrefix(l, "base-commit:") ||
			strings.Contains(l, " files changed,") ||
			strings.Contains(l, " file changed,") {
			return true
		}
	}
	return false
}

// ApplyPrefixToLines prefixes every line in text with prefix (e.g. "> " or ">> ").
func ApplyPrefixToLines(text, prefix string) string {
	if prefix == "" {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = strings.TrimRight(prefix, " ")
		} else {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// RenderSegments joins a slice of segments back into a complete email text string,
// applying QuotePrefix to prose and blank segments.
func RenderSegments(segments []Segment) string {
	if len(segments) == 0 {
		return ""
	}
	parts := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch seg.Kind {
		case SegmentBlank:
			parts = append(parts, seg.QuotePrefix)
		case SegmentProse:
			parts = append(parts, ApplyPrefixToLines(seg.Text, seg.QuotePrefix))
		case SegmentVerbatim:
			parts = append(parts, seg.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// FormatEmailForReading unwraps 80-column hard-wrapped prose and quoted paragraphs
// into natural English sentences while preserving quote markers, code, trailers, and diffs.
func FormatEmailForReading(raw string) string {
	segments := ParseEmailSegments(raw)
	return RenderSegments(segments)
}

// ExtractPatchPreamble splits a raw patch email body into its commit message / cover preamble
// and the trailing diff section ("---" / "diff --git ...").
func ExtractPatchPreamble(raw string) (preamble string, diffSection string) {
	cleaned := CleanEmailText(raw)
	lines := strings.Split(cleaned, "\n")
	for i, line := range lines {
		depth, _, rest := parseQuotePrefix(line)
		if depth != 0 {
			continue
		}
		trimmed := strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "diff --git ") || (trimmed == "---" && looksLikePatchSeparator(lines[i+1:])) {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i:], "\n")
		}
	}
	return cleaned, ""
}
