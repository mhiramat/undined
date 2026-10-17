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
		out = append(out, strings.TrimSpace(generalComment))
		out = append(out, "")
	}

	// Filter valid notes
	var validNotes []*InlineNote
	for i := range notes {
		n := &notes[i]
		f := strings.TrimSpace(n.File)
		t := strings.TrimSpace(n.Text)
		if f != "" && n.LineNo > 0 && t != "" {
			side := strings.ToLower(strings.TrimSpace(n.Side))
			validNotes = append(validNotes, &InlineNote{
				File:   f,
				LineNo: n.LineNo,
				Side:   side,
				Text:   t,
			})
		}
	}

	// If no patch or no valid notes: quote full original body
	if parsedDiff == nil || !parsedDiff.IsPatch || len(validNotes) == 0 {
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

	// 3. Process files and hunks containing notes
	hunksPrinted := 0

	for _, diffFile := range parsedDiff.Files {
		var fileNotes []*InlineNote
		for _, fn := range validNotes {
			if !fn.matched && (patch.FileMatches(fn.File, diffFile.NewPath) || patch.FileMatches(fn.File, diffFile.OldPath)) {
				fileNotes = append(fileNotes, fn)
			}
		}

		if len(fileNotes) == 0 {
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
				for _, fn := range fileNotes {
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
						out = append(out, c)
					}
					out = append(out, "")
				}

				prevIdx = idx
			}
		}
	}

	// Safety fallback if no notes matched any hunk
	if hunksPrinted == 0 {
		for _, line := range strings.Split(bodyPlain, "\n") {
			if line != "" {
				out = append(out, "> "+line)
			} else {
				out = append(out, ">")
			}
		}
		out = append(out, "")
		for _, vn := range validNotes {
			out = append(out, fmt.Sprintf("[%s:%d] %s", vn.File, vn.LineNo, vn.Text))
			out = append(out, "")
		}
	}

	// 4. Signature
	if strings.TrimSpace(signature) != "" {
		out = append(out, "", "-- ", strings.TrimSpace(signature))
	}

	return strings.Join(out, "\n")
}
