package ui

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"
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
			// Unescape backslash-escaped spaces.
			path = strings.ReplaceAll(path, `\ `, " ")
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

// extensionOf returns the file extension including the dot, e.g. ".png".
func extensionOf(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	return path[idx:]
}
