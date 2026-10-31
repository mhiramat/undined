// Package patch implements unified diff parsing, hunk extraction, and paired line alignment for SideDiff.
package patch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	hunkHeaderRegex = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(?: ?(.*))?$`)
	diffGitRegex    = regexp.MustCompile(`^diff --git (?:a/)?(\S+) (?:b/)?(\S+)`)
	gitVersionRegex = regexp.MustCompile(`^(?:git version\s+)?v?\d+\.\d+(?:\.\d+)*(?:[\.-][a-zA-Z0-9]+)*$`)
)

func isSignatureSeparator(line string) bool {
	trimmed := strings.TrimRight(line, " \t\r")
	return trimmed == "--" || trimmed == "---"
}

func isGitVersionLine(line string) bool {
	return gitVersionRegex.MatchString(strings.TrimSpace(line))
}

// DiffCell represents a single cell in a side-by-side diff table.
type DiffCell struct {
	LineNo  *int   `json:"line_no"`
	Content string `json:"content"`
	Type    string `json:"type"` // "ctx", "del", "add", "empty"
}

// PairedRow represents a row containing paired left (pre-image) and right (post-image) cells.
type PairedRow struct {
	RowIdx int      `json:"row_idx"`
	Left   DiffCell `json:"left"`
	Right  DiffCell `json:"right"`
}

// UnifiedLine represents a single unified diff line.
type UnifiedLine struct {
	Type    string `json:"type"` // "ctx", "add", "del", "header"
	OldNo   *int   `json:"old_no"`
	NewNo   *int   `json:"new_no"`
	Content string `json:"content"`
}

// Hunk represents a unified diff hunk with its headers, line counts, and paired rows.
type Hunk struct {
	Header       string        `json:"header"`
	OldStart     int           `json:"old_start"`
	OldCount     int           `json:"old_count"`
	NewStart     int           `json:"new_start"`
	NewCount     int           `json:"new_count"`
	Section      string        `json:"section,omitempty"`
	PairedRows   []PairedRow   `json:"paired_rows"`
	UnifiedLines []UnifiedLine `json:"unified_lines"`
}

// DiffFile represents a file modified in a patch.
type DiffFile struct {
	OldPath string  `json:"old_path"`
	NewPath string  `json:"new_path"`
	Hunks   []*Hunk `json:"hunks"`
}

// ParsedEmailDiff represents the complete parsed diff structure of an email.
type ParsedEmailDiff struct {
	IsPatch            bool        `json:"is_patch"`
	IsCorrupted        bool        `json:"is_corrupted"`
	CorruptedReason    string      `json:"corrupted_reason,omitempty"`
	Preamble           string      `json:"preamble"`
	Files              []*DiffFile `json:"files"`
	Postscript         string      `json:"postscript"`
	BodyContinuesAfter bool        `json:"body_continues_after"`
}

// HasBodyAfterDiff checks if lines after a diff contain substantive email body text
// (such as review discussion, quoted lines, or paragraphs) rather than standard git signatures.
func HasBodyAfterDiff(lines []string) bool {
	firstNonEmpty := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Quoted reply lines (e.g. "> ...") anywhere after the diff strictly indicate email discussion
		if strings.HasPrefix(trimmed, ">") {
			return true
		}
		if firstNonEmpty == "" {
			firstNonEmpty = line
		}
	}

	if firstNonEmpty == "" {
		return false
	}

	trimmedFirst := strings.TrimSpace(firstNonEmpty)
	// Standard email / git signature separators: "-- ", "--", "---", "-- <name>"
	if isSignatureSeparator(firstNonEmpty) || isSignatureSeparator(trimmedFirst) || (strings.HasPrefix(trimmedFirst, "--") && len(trimmedFirst) <= 30) {
		return false
	}

	// Git version number signatures (e.g. "2.43.0", "git version 2.43.0")
	if isGitVersionLine(trimmedFirst) {
		return false
	}

	return true
}

// NormalizePath strips leading a/ and b/ from file paths.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		p = p[2:]
	}
	return p
}

// FileMatches checks if two file paths refer to the same file.
func FileMatches(noteFile, diffFilePath string) bool {
	if noteFile == "" || diffFilePath == "" {
		return false
	}
	nf := NormalizePath(noteFile)
	df := NormalizePath(diffFilePath)
	return nf == df || strings.HasSuffix(nf, "/"+df) || strings.HasSuffix(df, "/"+nf)
}

func intPtr(i int) *int {
	return &i
}

// AlignHunkLines pairs additions and deletions horizontally for Side-by-Side rendering.
func AlignHunkLines(rawLines []string, oldStart, newStart int) ([]PairedRow, []UnifiedLine) {
	paired := make([]PairedRow, 0)
	unified := make([]UnifiedLine, 0)

	curOld := oldStart
	curNew := newStart
	rowIdx := 0

	i := 0
	n := len(rawLines)

	for i < n {
		line := rawLines[i]
		var prefix, content string
		if len(line) == 0 {
			prefix = " "
			content = ""
		} else {
			prefix = string(line[0])
			content = line[1:]
		}

		if prefix == " " {
			// Context line on both sides
			paired = append(paired, PairedRow{
				RowIdx: rowIdx,
				Left:   DiffCell{LineNo: intPtr(curOld), Content: content, Type: "ctx"},
				Right:  DiffCell{LineNo: intPtr(curNew), Content: content, Type: "ctx"},
			})
			unified = append(unified, UnifiedLine{
				Type:    "ctx",
				OldNo:   intPtr(curOld),
				NewNo:   intPtr(curNew),
				Content: content,
			})
			curOld++
			curNew++
			rowIdx++
			i++
		} else if prefix == "-" || prefix == "+" {
			var deletions []string
			var additions []string

			for i < n && len(rawLines[i]) > 0 && rawLines[i][0] == '-' {
				deletions = append(deletions, rawLines[i][1:])
				i++
			}
			for i < n && len(rawLines[i]) > 0 && rawLines[i][0] == '+' {
				additions = append(additions, rawLines[i][1:])
				i++
			}

			// Unified view records deletions then additions
			for _, d := range deletions {
				unified = append(unified, UnifiedLine{
					Type:    "del",
					OldNo:   intPtr(curOld),
					NewNo:   nil,
					Content: d,
				})
				curOld++
			}
			curOld -= len(deletions)

			for _, a := range additions {
				unified = append(unified, UnifiedLine{
					Type:    "add",
					OldNo:   nil,
					NewNo:   intPtr(curNew),
					Content: a,
				})
				curNew++
			}
			curNew -= len(additions)

			// Pair deletions and additions horizontally
			maxLen := len(deletions)
			if len(additions) > maxLen {
				maxLen = len(additions)
			}

			for k := 0; k < maxLen; k++ {
				var leftCell, rightCell DiffCell
				if k < len(deletions) {
					leftCell = DiffCell{LineNo: intPtr(curOld), Content: deletions[k], Type: "del"}
					curOld++
				} else {
					leftCell = DiffCell{LineNo: nil, Content: "", Type: "empty"}
				}

				if k < len(additions) {
					rightCell = DiffCell{LineNo: intPtr(curNew), Content: additions[k], Type: "add"}
					curNew++
				} else {
					rightCell = DiffCell{LineNo: nil, Content: "", Type: "empty"}
				}

				paired = append(paired, PairedRow{
					RowIdx: rowIdx,
					Left:   leftCell,
					Right:  rightCell,
				})
				rowIdx++
			}
		} else if prefix == "\\" {
			// "\ No newline at end of file"
			i++
		} else {
			// Fallback for unexpected line
			paired = append(paired, PairedRow{
				RowIdx: rowIdx,
				Left:   DiffCell{LineNo: nil, Content: line, Type: "ctx"},
				Right:  DiffCell{LineNo: nil, Content: line, Type: "ctx"},
			})
			unified = append(unified, UnifiedLine{
				Type:    "ctx",
				OldNo:   nil,
				NewNo:   nil,
				Content: line,
			})
			rowIdx++
			i++
		}
	}

	return paired, unified
}

// Parse parses email text into preamble, structured diff files/hunks, and postscript.
func Parse(text string) *ParsedEmailDiff {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	var preambleLines []string
	files := make([]*DiffFile, 0)
	var postscriptLines []string

	state := "PREAMBLE"
	var currentFile *DiffFile
	var currentHunk *Hunk
	var hunkRawLines []string

	isCorrupted := false
	corruptedReason := ""

	checkLineForNonASCII := func(line string) {
		if isCorrupted {
			return
		}
		for _, r := range line {
			if r > 127 {
				isCorrupted = true
				corruptedReason = fmt.Sprintf("non-ASCII character detected in patch diff (U+%04X)", r)
				return
			}
		}
	}

	commitHunk := func() {
		if currentHunk != nil {
			paired, unif := AlignHunkLines(hunkRawLines, currentHunk.OldStart, currentHunk.NewStart)
			if paired == nil {
				paired = make([]PairedRow, 0)
			}
			if unif == nil {
				unif = make([]UnifiedLine, 0)
			}
			currentHunk.PairedRows = paired
			currentHunk.UnifiedLines = unif
			if currentFile != nil {
				currentFile.Hunks = append(currentFile.Hunks, currentHunk)
			}
			currentHunk = nil
			hunkRawLines = nil
		}
	}

	commitFile := func() {
		commitHunk()
		if currentFile != nil {
			if currentFile.Hunks == nil {
				currentFile.Hunks = make([]*Hunk, 0)
			}
			files = append(files, currentFile)
			currentFile = nil
		}
	}

	total := len(lines)
	idx := 0

	for idx < total {
		line := lines[idx]

		if state == "PREAMBLE" {
			if m := diffGitRegex.FindStringSubmatch(line); len(m) > 2 {
				state = "DIFF"
				commitFile()
				checkLineForNonASCII(line)
				currentFile = &DiffFile{OldPath: NormalizePath(m[1]), NewPath: NormalizePath(m[2]), Hunks: make([]*Hunk, 0)}
				idx++
				continue
			} else if strings.HasPrefix(line, "--- a/") && idx+1 < total && strings.HasPrefix(lines[idx+1], "+++ b/") {
				state = "DIFF"
				commitFile()
				checkLineForNonASCII(line)
				checkLineForNonASCII(lines[idx+1])
				oldP := NormalizePath(strings.TrimPrefix(line, "--- a/"))
				newP := NormalizePath(strings.TrimPrefix(lines[idx+1], "+++ b/"))
				currentFile = &DiffFile{OldPath: oldP, NewPath: newP, Hunks: make([]*Hunk, 0)}
				idx += 2
				continue
			} else {
				preambleLines = append(preambleLines, line)
				idx++
				continue
			}
		} else if state == "DIFF" {
			if isSignatureSeparator(line) || isGitVersionLine(line) {
				state = "POSTSCRIPT"
				commitFile()
				postscriptLines = append(postscriptLines, line)
				idx++
				continue
			}

			checkLineForNonASCII(line)

			if m := diffGitRegex.FindStringSubmatch(line); len(m) > 2 {
				commitFile()
				currentFile = &DiffFile{OldPath: NormalizePath(m[1]), NewPath: NormalizePath(m[2]), Hunks: make([]*Hunk, 0)}
				idx++
				continue
			}

			if strings.HasPrefix(line, "--- a/") && idx+1 < total && strings.HasPrefix(lines[idx+1], "+++ b/") {
				checkLineForNonASCII(lines[idx+1])
				oldP := NormalizePath(strings.TrimPrefix(line, "--- a/"))
				newP := NormalizePath(strings.TrimPrefix(lines[idx+1], "+++ b/"))
				if currentFile != nil && len(currentFile.Hunks) == 0 {
					currentFile.OldPath = oldP
					currentFile.NewPath = newP
				} else {
					commitFile()
					currentFile = &DiffFile{OldPath: oldP, NewPath: newP, Hunks: make([]*Hunk, 0)}
				}
				idx += 2
				continue
			}

			if m := hunkHeaderRegex.FindStringSubmatch(line); len(m) > 3 {
				commitHunk()
				oldS, _ := strconv.Atoi(m[1])
				oldC := 1
				if m[2] != "" {
					oldC, _ = strconv.Atoi(m[2])
				}
				newS, _ := strconv.Atoi(m[3])
				newC := 1
				if m[4] != "" {
					newC, _ = strconv.Atoi(m[4])
				}
				sec := ""
				if len(m) > 5 {
					sec = strings.TrimSpace(m[5])
				}

				currentHunk = &Hunk{
					Header:       line,
					OldStart:     oldS,
					OldCount:     oldC,
					NewStart:     newS,
					NewCount:     newC,
					Section:      sec,
					PairedRows:   make([]PairedRow, 0),
					UnifiedLines: make([]UnifiedLine, 0),
				}
				idx++
				continue
			}

			if currentHunk != nil {
				r, _ := utf8.DecodeRuneInString(line)
				if len(line) > 0 && (line[0] == '+' || line[0] == '-' || line[0] == ' ' || line[0] == '\\' || unicode.IsSpace(r) || (isCorrupted && !isSignatureSeparator(line) && !isGitVersionLine(line) && !strings.HasPrefix(line, ">"))) {
					hunkRawLines = append(hunkRawLines, line)
					idx++
					continue
				} else {
					commitHunk()
				}
			}

			// Mode/index lines can only occur before any hunk has been processed for the current file
			if currentFile != nil && len(currentFile.Hunks) == 0 {
				idx++
				continue
			}

			// If we reached here with hunks already parsed, the diff for the current file has ended.
			// Check if another hunk or file follows after blank lines.
			if strings.TrimSpace(line) == "" {
				hasMoreDiff := false
				for lookahead := idx + 1; lookahead < total; lookahead++ {
					trimmedNext := strings.TrimSpace(lines[lookahead])
					if trimmedNext == "" {
						continue
					}
					if hunkHeaderRegex.MatchString(lines[lookahead]) ||
						diffGitRegex.MatchString(lines[lookahead]) ||
						(strings.HasPrefix(lines[lookahead], "--- a/") && lookahead+1 < total && strings.HasPrefix(lines[lookahead+1], "+++ b/")) {
						hasMoreDiff = true
					}
					break
				}
				if hasMoreDiff {
					idx++
					continue
				}
			}

			// No more diff hunks or files. Transition to POSTSCRIPT and do not discard lines.
			commitFile()
			state = "POSTSCRIPT"
			postscriptLines = append(postscriptLines, line)
			idx++
		} else if state == "POSTSCRIPT" {
			postscriptLines = append(postscriptLines, line)
			idx++
		}
	}

	commitFile()

	hasHunks := false
	for _, f := range files {
		if len(f.Hunks) > 0 {
			hasHunks = true
			break
		}
	}

	bodyContinuesAfter := HasBodyAfterDiff(postscriptLines)
	isPatch := len(files) > 0 && (hasHunks || isCorrupted) && !bodyContinuesAfter

	return &ParsedEmailDiff{
		IsPatch:            isPatch,
		IsCorrupted:        isCorrupted,
		CorruptedReason:    corruptedReason,
		Preamble:           strings.TrimSpace(strings.Join(preambleLines, "\n")),
		Files:              files,
		Postscript:         strings.TrimSpace(strings.Join(postscriptLines, "\n")),
		BodyContinuesAfter: bodyContinuesAfter,
	}
}

// ParsePatch is an alias for Parse retained for backward compatibility.
func ParsePatch(text string) *ParsedEmailDiff {
	return Parse(text)
}
