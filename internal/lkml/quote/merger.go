// Package quote implements LKML email quote merging and review bubble aggregation.
package quote

import (
	"fmt"
	"strings"

	"undined-go/internal/lkml/patch"
)

// InlineNote represents a review note taken at a specific file and line.
type InlineNote struct {
	File    string `json:"file"`
	LineNo  int    `json:"line_no"`
	Side    string `json:"side"` // "new" or "old"
	Text    string `json:"text"`
	matched bool
}

// DefaultWrapColumns is the standard LKML column limit for wrapping review comments.
const DefaultWrapColumns = 75

func isWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) ||
		(r >= 0x2E80 && r <= 0xA4CF && r != 0x303F) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1F9FF) ||
		(r >= 0x20000 && r <= 0x3FFFD)
}

func runeWidth(r rune, col int) int {
	if r == '\t' {
		return 8 - (col % 8)
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

func visualWidth(s string) int {
	col := 0
	for _, r := range s {
		col += runeWidth(r, col)
	}
	return col
}

func isURLToken(w string) bool {
	return strings.HasPrefix(w, "http://") ||
		strings.HasPrefix(w, "https://") ||
		strings.HasPrefix(w, "<http://") ||
		strings.HasPrefix(w, "<https://")
}

// WrapCommentText wraps lines in a review comment around maxCols columns (default 75).
// Quoted lines (">"), fenced code blocks ("```"), and tab-indented lines are preserved as-is.
func WrapCommentText(text string, maxCols int) string {
	if maxCols <= 0 {
		maxCols = DefaultWrapColumns
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	rawLines := strings.Split(normalized, "\n")
	var outLines []string
	inCodeFence := false

	for _, line := range rawLines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeFence = !inCodeFence
			outLines = append(outLines, line)
			continue
		}
		if inCodeFence || trimmed == "" || visualWidth(line) <= maxCols {
			outLines = append(outLines, line)
			continue
		}
		if strings.HasPrefix(trimmed, ">") || strings.HasPrefix(line, "\t") {
			outLines = append(outLines, line)
			continue
		}

		// Extract leading spaces (preserve up to 8 spaces of indentation)
		indentLen := 0
		for indentLen < len(line) && line[indentLen] == ' ' {
			indentLen++
		}
		indent := line[:indentLen]
		if len(indent) > 8 {
			indent = ""
		}
		indentWidth := visualWidth(indent)

		words := strings.Fields(line[indentLen:])
		if len(words) == 0 {
			outLines = append(outLines, line)
			continue
		}

		curLine := ""
		curWidth := 0

		for _, word := range words {
			wordWidth := visualWidth(word)
			if indentWidth+wordWidth <= maxCols {
				if curLine == "" {
					curLine = indent + word
					curWidth = indentWidth + wordWidth
				} else if curWidth+1+wordWidth <= maxCols {
					curLine += " " + word
					curWidth += 1 + wordWidth
				} else {
					outLines = append(outLines, curLine)
					curLine = indent + word
					curWidth = indentWidth + wordWidth
				}
			} else if isURLToken(word) {
				if curLine != "" {
					outLines = append(outLines, curLine)
					curLine = ""
					curWidth = 0
				}
				outLines = append(outLines, indent+word)
			} else {
				// Overlong non-URL token (e.g., long string without spaces or CJK text)
				if curLine != "" {
					outLines = append(outLines, curLine)
					curLine = ""
					curWidth = 0
				}
				chunk := indent
				chunkWidth := indentWidth
				for _, r := range word {
					rw := runeWidth(r, chunkWidth)
					if chunkWidth+rw > maxCols && chunk != indent {
						outLines = append(outLines, chunk)
						chunk = indent
						chunkWidth = indentWidth
						rw = runeWidth(r, chunkWidth)
					}
					chunk += string(r)
					chunkWidth += rw
				}
				if chunk != indent {
					curLine = chunk
					curWidth = chunkWidth
				}
			}
		}
		if curLine != "" {
			outLines = append(outLines, curLine)
		}
	}

	return strings.Join(outLines, "\n")
}

// FormatAttribution builds the standard LKML email attribution line.
func FormatAttribution(dateStr, fromName, fromAddr string) string {
	namePart := strings.TrimSpace(fromName)
	if namePart == "" {
		namePart = fromAddr
	}
	addrPart := ""
	if fromAddr != "" {
		addrPart = fmt.Sprintf("<%s>", fromAddr)
	}
	dtPart := strings.TrimSpace(dateStr)
	if dtPart == "" {
		dtPart = "recently"
	}

	if namePart != "" && addrPart != "" {
		return fmt.Sprintf("On %s, %s %s wrote:", dtPart, namePart, addrPart)
	}
	if namePart != "" {
		return fmt.Sprintf("On %s, %s wrote:", dtPart, namePart)
	}
	return fmt.Sprintf("On %s, wrote:", dtPart)
}

// MergePatchReviewQuote merges inline review comments into an LKML quoted email body.
func MergePatchReviewQuote(
	dateStr, fromName, fromAddr, bodyPlain string,
	parsedDiff *patch.ParsedEmailDiff,
	notes []InlineNote,
	generalComment string,
	signature string,
	contextLines ...int,
) string {
	ctxLines := 3
	if len(contextLines) > 0 && contextLines[0] >= 0 {
		ctxLines = contextLines[0]
	}

	var out []string

	// 1. Attribution header
	out = append(out, FormatAttribution(dateStr, fromName, fromAddr))

	// 2. General comment
	if strings.TrimSpace(generalComment) != "" {
		out = append(out, "")
		out = append(out, WrapCommentText(strings.TrimSpace(generalComment), DefaultWrapColumns))
		out = append(out, "")
	}

	// Filter valid notes
	var validNotes []*InlineNote
	var descNotes []*InlineNote
	var fileNotes []*InlineNote

	for i := range notes {
		n := &notes[i]
		f := strings.TrimSpace(n.File)
		t := strings.TrimSpace(n.Text)
		if f != "" && n.LineNo > 0 && t != "" {
			side := strings.ToLower(strings.TrimSpace(n.Side))
			if strings.EqualFold(f, "DESCRIPTION") {
				f = "DESCRIPTION"
			}
			note := &InlineNote{
				File:   f,
				LineNo: n.LineNo,
				Side:   side,
				Text:   t,
			}
			validNotes = append(validNotes, note)
			if f == "DESCRIPTION" {
				descNotes = append(descNotes, note)
			} else {
				fileNotes = append(fileNotes, note)
			}
		}
	}

	// If no valid notes: quote full original body
	if len(validNotes) == 0 {
		for _, line := range strings.Split(bodyPlain, "\n") {
			if line != "" {
				out = append(out, "> "+line)
			} else {
				out = append(out, ">")
			}
		}
		if strings.TrimSpace(signature) != "" {
			out = append(out, "", "-- ", strings.TrimSpace(signature))
		}
		return strings.Join(out, "\n")
	}

	// 3. Process message body / patch description notes (DESCRIPTION)
	descLinesPrinted := 0
	if len(descNotes) > 0 {
		descText := ""
		if parsedDiff != nil && parsedDiff.IsPatch && parsedDiff.Preamble != "" {
			descText = parsedDiff.Preamble
		} else {
			descText = bodyPlain
		}

		descLines := strings.Split(descText, "\n")
		descNoteMap := make(map[int][]string)
		for _, dn := range descNotes {
			if dn.LineNo >= 1 && dn.LineNo <= len(descLines) {
				descNoteMap[dn.LineNo] = append(descNoteMap[dn.LineNo], dn.Text)
				dn.matched = true
			}
		}

		if len(descNoteMap) > 0 {
			includedIndices := make(map[int]struct{})
			if len(descLines) <= 25 {
				for idx := 1; idx <= len(descLines); idx++ {
					includedIndices[idx] = struct{}{}
				}
			} else {
				for lNo := range descNoteMap {
					for offset := -ctxLines; offset <= ctxLines; offset++ {
						cand := lNo + offset
						if cand >= 1 && cand <= len(descLines) {
							includedIndices[cand] = struct{}{}
						}
					}
				}
			}

			prevIdx := -1
			for idx := 1; idx <= len(descLines); idx++ {
				if _, ok := includedIndices[idx]; !ok {
					continue
				}

				if prevIdx == -1 && idx > 1 {
					out = append(out, "> [...]")
				} else if prevIdx != -1 && idx > prevIdx+1 {
					out = append(out, "> [...]")
				}

				line := descLines[idx-1]
				if line != "" {
					out = append(out, "> "+line)
				} else {
					out = append(out, ">")
				}

				if comments, exists := descNoteMap[idx]; exists {
					out = append(out, "")
					for _, c := range comments {
						out = append(out, WrapCommentText(c, DefaultWrapColumns))
					}
					out = append(out, "")
				}

				prevIdx = idx
				descLinesPrinted++
			}

			if prevIdx != -1 && prevIdx < len(descLines) {
				out = append(out, "> [...]")
			}
		}
	}

	// 4. Process files and hunks containing notes
	hunksPrinted := 0

	if parsedDiff != nil && parsedDiff.IsPatch {
		for _, diffFile := range parsedDiff.Files {
			var fNotes []*InlineNote
			for _, fn := range fileNotes {
				if !fn.matched && (patch.FileMatches(fn.File, diffFile.NewPath) || patch.FileMatches(fn.File, diffFile.OldPath)) {
					fNotes = append(fNotes, fn)
				}
			}

			if len(fNotes) == 0 {
				continue
			}

			fileHeaderPrinted := false

			for _, hunk := range diffFile.Hunks {
				hunkNotes := make(map[int][]string)

				for _, row := range hunk.PairedRows {
					var rNo, lNo int
					if row.Right.LineNo != nil {
						rNo = *row.Right.LineNo
					}
					if row.Left.LineNo != nil {
						lNo = *row.Left.LineNo
					}

					var rowComments []string
					for _, fn := range fNotes {
						if fn.matched {
							continue
						}
						tl := fn.LineNo
						ts := fn.Side
						matched := false

						if ts == "new" || ts == "right" {
							matched = (rNo == tl)
						} else if ts == "old" || ts == "left" {
							matched = (lNo == tl)
						} else {
							if rNo == tl {
								matched = true
							} else if row.Left.Type == "del" && lNo == tl {
								matched = true
							} else if lNo == tl && (rNo == 0 || rNo != tl) {
								matched = true
							}
						}

						if matched {
							rowComments = append(rowComments, fn.Text)
							fn.matched = true
						}
					}

					if len(rowComments) > 0 {
						hunkNotes[row.RowIdx] = rowComments
					}
				}

				if len(hunkNotes) == 0 {
					continue
				}

				// Print file header
				if !fileHeaderPrinted {
					if len(out) > 0 && out[len(out)-1] != "" {
						out = append(out, "")
					}
					out = append(out, fmt.Sprintf("> diff --git a/%s b/%s", diffFile.OldPath, diffFile.NewPath))
					out = append(out, fmt.Sprintf("> --- a/%s", diffFile.OldPath))
					out = append(out, fmt.Sprintf("> +++ b/%s", diffFile.NewPath))
					fileHeaderPrinted = true
				}

				out = append(out, "> "+hunk.Header)
				hunksPrinted++

				// Calculate row indices to include
				includedIndices := make(map[int]struct{})
				for aIdx := range hunkNotes {
					for offset := -ctxLines; offset <= ctxLines; offset++ {
						cand := aIdx + offset
						if cand >= 0 && cand < len(hunk.PairedRows) {
							includedIndices[cand] = struct{}{}
						}
					}
				}

				prevIdx := -1
				for _, row := range hunk.PairedRows {
					idx := row.RowIdx
					if _, ok := includedIndices[idx]; !ok {
						continue
					}

					if prevIdx != -1 && idx > prevIdx+1 {
						out = append(out, "> [...]")
					}

					var prefixChar, content string
					if row.Right.Type == "add" {
						prefixChar = "+"
						content = row.Right.Content
					} else if row.Left.Type == "del" {
						prefixChar = "-"
						content = row.Left.Content
					} else {
						prefixChar = " "
						content = row.Right.Content
					}

					out = append(out, fmt.Sprintf("> %s%s", prefixChar, content))

					if comments, exists := hunkNotes[idx]; exists {
						out = append(out, "")
						for _, c := range comments {
							out = append(out, WrapCommentText(c, DefaultWrapColumns))
						}
						out = append(out, "")
					}

					prevIdx = idx
				}
			}
		}
	}

	// Safety fallback if no notes matched any hunk or description
	if hunksPrinted == 0 && descLinesPrinted == 0 {
		for _, line := range strings.Split(bodyPlain, "\n") {
			if line != "" {
				out = append(out, "> "+line)
			} else {
				out = append(out, ">")
			}
		}
		out = append(out, "")
		for _, vn := range validNotes {
			out = append(out, WrapCommentText(fmt.Sprintf("[%s:%d] %s", vn.File, vn.LineNo, vn.Text), DefaultWrapColumns))
			out = append(out, "")
		}
	} else {
		// Append any remaining unmatched notes (e.g. invalid line number)
		var unmatched []*InlineNote
		for _, vn := range validNotes {
			if !vn.matched {
				unmatched = append(unmatched, vn)
			}
		}
		if len(unmatched) > 0 {
			out = append(out, "")
			for _, un := range unmatched {
				out = append(out, WrapCommentText(fmt.Sprintf("[%s:%d] %s", un.File, un.LineNo, un.Text), DefaultWrapColumns))
				out = append(out, "")
			}
		}
	}

	// 5. Signature
	if strings.TrimSpace(signature) != "" {
		out = append(out, "", "-- ", strings.TrimSpace(signature))
	}

	return strings.Join(out, "\n")
}
