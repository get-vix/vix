package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/get-vix/vix/internal/protocol"
)

const maxImageSize = 20 * 1024 * 1024 // 20MB

// imageExtensions maps supported file extensions to MIME media types.
var imageExtensions = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
}

// imagePathPattern matches drag-and-drop image paths in three formats:
// 1. Single-quoted: '/path/to/image.png'
// 2. Double-quoted: "/path/to/image.png"
// 3. Unquoted with optional backslash-escaped spaces: /path/to/image.png or /path/to/my\ image.png
var imagePathPattern = regexp.MustCompile(
	`'(/[^']+\.(?i:png|jpe?g|gif|webp|bmp))'` + `|` +
		`"(/[^"]+\.(?i:png|jpe?g|gif|webp|bmp))"` + `|` +
		`(/(?:[^\s'"\\]|\\.)+\.(?i:png|jpe?g|gif|webp|bmp))`,
)

// extractImageAttachments scans text for drag-and-dropped image file paths,
// reads and base64-encodes them, and returns the cleaned text with [Image #N]
// placeholders, the attachments, and any error messages for files that exist
// but couldn't be read.
func extractImageAttachments(text string) (cleanText string, attachments []protocol.Attachment, errs []string) {
	matches := imagePathPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil, nil
	}

	imageNum := 0
	var result strings.Builder
	lastIdx := 0

	for _, loc := range matches {
		// Determine which capture group matched and extract the path.
		var path string
		var fullStart, fullEnd int

		fullStart, fullEnd = loc[0], loc[1]

		switch {
		case loc[2] >= 0: // single-quoted group
			path = text[loc[2]:loc[3]]
		case loc[4] >= 0: // double-quoted group
			path = text[loc[4]:loc[5]]
		case loc[6] >= 0: // unquoted group
			path = text[loc[6]:loc[7]]
			// Unescape backslash-escaped shell-special characters.
			path = unescapeDropPath(path)
		default:
			continue
		}

		// Check extension is supported (case-insensitive).
		ext := strings.ToLower(extensionOf(path))
		mediaType, ok := imageExtensions[ext]
		if !ok {
			continue
		}

		// Check if file exists.
		info, err := os.Stat(path)
		if err != nil {
			// File doesn't exist — not a drag-drop, leave text as-is.
			continue
		}

		// Check file size.
		if info.Size() > maxImageSize {
			errs = append(errs, fmt.Sprintf("Image too large (%.1fMB > 20MB): %s", float64(info.Size())/(1024*1024), path))
			continue
		}

		// Read the file.
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("Failed to read image: %s", err))
			continue
		}

		imageNum++
		attachments = append(attachments, protocol.Attachment{
			Type:      "image",
			MediaType: mediaType,
			Data:      base64.StdEncoding.EncodeToString(data),
			Path:      path,
		})

		// Replace the matched path (including quotes) with [Image #N].
		result.WriteString(text[lastIdx:fullStart])
		result.WriteString(fmt.Sprintf("[Image #%d]", imageNum))
		lastIdx = fullEnd
	}

	result.WriteString(text[lastIdx:])
	cleanText = result.String()
	return cleanText, attachments, errs
}

// backslashEscape matches a backslash followed by any single character, as
// produced by terminals when a dropped path contains shell-special characters
// (spaces, `~`, `(`, `&`, …).
var backslashEscape = regexp.MustCompile(`\\(.)`)

// unescapeDropPath removes backslash escaping from an unquoted drag-and-dropped
// path, turning `\X` into `X` for every escaped character — not just spaces.
// macOS escapes `~` (and others) in iCloud paths like `com\~apple\~CloudDocs`,
// so unescaping only `\ ` would leave literal backslashes that break os.Stat.
func unescapeDropPath(path string) string {
	return backslashEscape.ReplaceAllString(path, "$1")
}

// extensionOf returns the file extension including the dot, e.g. ".png".
func extensionOf(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return path[idx:]
}

// fileExtensions maps supported non-image attachment extensions to a display
// kind: "pdf" (converted to text by the daemon's PDF reader) or "file" (embedded
// as UTF-8 text). Covers common docs/data formats plus source code.
var fileExtensions = map[string]string{
	".pdf": "pdf",
	// docs / data
	".txt": "file", ".md": "file", ".markdown": "file", ".rst": "file",
	".csv": "file", ".tsv": "file", ".json": "file", ".jsonl": "file",
	".yaml": "file", ".yml": "file", ".toml": "file", ".ini": "file",
	".xml": "file", ".html": "file", ".htm": "file", ".log": "file",
	// source code
	".go": "file", ".py": "file", ".js": "file", ".ts": "file", ".tsx": "file",
	".jsx": "file", ".rs": "file", ".c": "file", ".h": "file", ".cpp": "file",
	".hpp": "file", ".cc": "file", ".java": "file", ".rb": "file", ".sh": "file",
	".bash": "file", ".zsh": "file", ".sql": "file", ".css": "file", ".scss": "file",
	".php": "file", ".swift": "file", ".kt": "file", ".lua": "file", ".pl": "file",
	".r": "file", ".scala": "file", ".dart": "file", ".ex": "file", ".exs": "file",
	".vue": "file", ".svelte": "file", ".proto": "file", ".gradle": "file",
}

// filePathPattern matches drag-and-drop text/PDF paths in the same three quoting
// formats as imagePathPattern, over the fileExtensions alternation.
var filePathPattern = buildFilePathPattern()

func buildFilePathPattern() *regexp.Regexp {
	exts := make([]string, 0, len(fileExtensions))
	for ext := range fileExtensions {
		exts = append(exts, regexp.QuoteMeta(strings.TrimPrefix(ext, ".")))
	}
	// Longest-first so e.g. "cpp" is preferred over "c" in the alternation.
	sort.Slice(exts, func(i, j int) bool {
		if len(exts[i]) != len(exts[j]) {
			return len(exts[i]) > len(exts[j])
		}
		return exts[i] < exts[j]
	})
	alt := strings.Join(exts, "|")
	return regexp.MustCompile(
		`'(/[^']+\.(?i:` + alt + `))'` + `|` +
			`"(/[^"]+\.(?i:` + alt + `))"` + `|` +
			`(/(?:[^\s'"\\]|\\.)+\.(?i:` + alt + `))`,
	)
}

// fileCandidate is a detected text/PDF file path referenced in input text.
type fileCandidate struct {
	Path string // filesystem path
	Kind string // "pdf" or "file"
	Raw  string // exact matched substring (including quotes) for stripping
}

// detectFileCandidates returns existing text/PDF file paths referenced in text.
// It performs no I/O beyond os.Stat — content validation happens on the daemon
// via attachment.validate. Used at drop time to decide what to validate.
func detectFileCandidates(text string) []fileCandidate {
	matches := filePathPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []fileCandidate
	seen := make(map[string]bool)
	for _, loc := range matches {
		path, ok := matchedPath(text, loc)
		if !ok || seen[path] {
			continue
		}
		kind, ok := fileExtensions[strings.ToLower(extensionOf(path))]
		if !ok {
			continue
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		seen[path] = true
		out = append(out, fileCandidate{Path: path, Kind: kind, Raw: text[loc[0]:loc[1]]})
	}
	return out
}

// extractFileAttachments replaces text/PDF file paths in text with numbered
// [PDF #N]/[File #N] placeholders and returns them as path-only "file"
// attachments (the daemon reads and converts them at send time). Used on submit
// to catch typed paths that never went through drop-time validation.
func extractFileAttachments(text string) (cleanText string, attachments []protocol.Attachment) {
	matches := filePathPattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	pdfNum, fileNum := 0, 0
	var result strings.Builder
	lastIdx := 0
	for _, loc := range matches {
		path, ok := matchedPath(text, loc)
		if !ok {
			continue
		}
		kind, ok := fileExtensions[strings.ToLower(extensionOf(path))]
		if !ok {
			continue
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		var placeholder string
		if kind == "pdf" {
			pdfNum++
			placeholder = fmt.Sprintf("[PDF #%d]", pdfNum)
		} else {
			fileNum++
			placeholder = fmt.Sprintf("[File #%d]", fileNum)
		}
		attachments = append(attachments, protocol.Attachment{Type: "file", Path: path})
		result.WriteString(text[lastIdx:loc[0]])
		result.WriteString(placeholder)
		lastIdx = loc[1]
	}
	result.WriteString(text[lastIdx:])
	return result.String(), attachments
}

// matchedPath extracts the path from a filePathPattern submatch index tuple,
// unescaping backslash-escaped spaces for the unquoted form.
func matchedPath(text string, loc []int) (string, bool) {
	switch {
	case loc[2] >= 0:
		return text[loc[2]:loc[3]], true
	case loc[4] >= 0:
		return text[loc[4]:loc[5]], true
	case loc[6] >= 0:
		return unescapeDropPath(text[loc[6]:loc[7]]), true
	}
	return "", false
}

// attachmentRefPattern matches the daemon's persisted attachment reference
// lines ("[Image: /path]" / "[File: /path]") that prefix a stored user message
// (see Thread.AddUserMessage in the daemon).
var attachmentRefPattern = regexp.MustCompile(`^\[(Image|File): (.+)\]$`)

// parseAttachmentRefs splits a replayed user message into its body text and the
// attachments encoded as leading "[Image: path]" / "[File: path]" reference
// lines. The daemon writes those refs, then a blank separator line, then the
// body (an attachment-only message stores just the refs and no body). Returns
// the text unchanged with no attachments when it carries no reference lines.
func parseAttachmentRefs(text string) (body string, attachments []protocol.Attachment) {
	lines := strings.Split(text, "\n")
	i := 0
	for i < len(lines) {
		m := attachmentRefPattern.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		att := protocol.Attachment{Type: "file", Path: m[2]}
		if m[1] == "Image" {
			att.Type = "image"
		}
		attachments = append(attachments, att)
		i++
	}
	if len(attachments) == 0 {
		return text, nil
	}
	// The daemon separates the refs from the body with a single blank line.
	if i < len(lines) && lines[i] == "" {
		i++
	}
	return strings.Join(lines[i:], "\n"), attachments
}
