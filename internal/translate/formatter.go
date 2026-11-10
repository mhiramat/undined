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

	// trailerRe matches standard Linux kernel commit/email trailers.
	trailerRe = regexp.MustCompile(`^(?i)(Signed-off-by|Reviewed-by|Acked-by|Tested-by|Reported-by|Suggested-by|Co-developed-by|Fixes|Link|Closes|Cc|Message-ID|Change-Id):\s+\S+`)

	// attributionStartRe matches the start of an RFC quote attribution line ("On ... wrote:").
	attributionStartRe = regexp.MustCompile(`^(?:On\s+.+|.+<\S+@\S+>.+wrote:\s*)$`)

	// diffHeaderRe matches unified diff or git diff headers.
	diffHeaderRe = regexp.MustCompile(`^(?:diff --git\s+|index [0-9a-f]{7,}\.\.[0-9a-f]{7,}|---\s+\S+|\+\+\+\s+\S+|@@\s+-\d+)`)
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
		inDiffOrSig   bool
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

		// Once inside an unquoted signature ("-- ") or raw patch diff section, keep remaining lines verbatim.
		if inDiffOrSig && depth == 0 {
			appendVerbatim(0, "", line)
			continue
		}

		// Detect unquoted signature delimiter "-- " or patch separator "---" followed by diffstat/diff
		if depth == 0 && !inFence {
			if rest == "-- " || rest == "--" {
				inDiffOrSig = true
				appendVerbatim(0, "", line)
				continue
			}
			if strings.HasPrefix(rest, "diff --git ") {
				inDiffOrSig = true
				appendVerbatim(0, "", line)
				continue
			}
			if trimmedRest == "---" && looksLikePatchSeparator(lines[idx+1:]) {
				inDiffOrSig = true
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
			segments = append(segments, Segment{
				Kind:        SegmentBlank,
				QuoteDepth:  depth,
				QuotePrefix: strings.TrimRight(prefix, " "),
				Text:        "",
			})
			continue
		}

		// Verbatim single lines: trailers, diff lines, ASCII tables
		if trailerRe.MatchString(trimmedRest) || diffHeaderRe.MatchString(trimmedRest) || isTableOrASCIIArtLine(rest) {
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
