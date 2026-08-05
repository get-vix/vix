package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/daemon/pdf"
)

// Attachment validation statuses shared by the attachment.validate RPC and the
// send-time transform. "ok" means the file can be embedded as prompt text;
// "invalid" means the file can't be used (the TUI alerts + drops).
const (
	attachmentOK      = "ok"
	attachmentInvalid = "invalid"
)

// readAttachmentFile authorizes a user-attached path against the thread's
// deny list (identical to read_file), then reads its bytes. Unlike read_file it
// does NOT require the path to sit under the working directory: a drag-and-drop
// is explicit user intent, so any file the user attaches is allowed unless it is
// on the deny list. It returns an error describing why access was refused or why
// the read failed.
func (s *Thread) readAttachmentFile(path string) ([]byte, error) {
	if blocked := checkDenyList("read_file", map[string]any{"path": path}, s.cwd, s.denyListSnapshot(), s.denyURLsSnapshot()); blocked != nil {
		return nil, fmt.Errorf("access to this path is denied")
	}
	abs, err := resolveUserAttachmentPath(s.cwd, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(abs)
}

// resolveUserAttachmentPath resolves a user-dragged attachment path to an
// absolute, symlink-resolved path. Relative paths are joined against cwd, but —
// unlike resolvePathInAllowed — the result is NOT constrained to the working
// directory: attaching a file is an explicit user action, so paths anywhere on
// disk are accepted (the deny list, enforced by the caller, remains the hard
// boundary). Symlinks are resolved so the deny-list check operates on the real
// target.
func resolveUserAttachmentPath(cwd, path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Clean(filepath.Join(cwd, path))
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", path)
		}
		return "", err
	}
	return resolved, nil
}

// resolveFileAttachment converts an attached file's raw bytes into embeddable
// prompt text. It returns a status (attachmentOK/Invalid), a short
// human-readable reason when the status is not OK, and the extracted text when
// it is OK. PDFs are converted with the built-in reader — password-protected,
// scanned, oversized, or corrupt documents are invalid; every other file must be
// valid UTF-8 text within the configured size cap.
func resolveFileAttachment(path string, raw []byte) (status, reason, text string) {
	if looksLikePDF(raw) {
		if len(raw) > maxPDFBytes {
			return attachmentInvalid, fmt.Sprintf("PDF is too large (limit %d bytes)", maxPDFBytes), ""
		}
		res, err := pdf.ToMarkdown(raw)
		if err != nil {
			if err == pdf.ErrEncrypted {
				return attachmentInvalid, "PDF is password-protected", ""
			}
			return attachmentInvalid, "could not parse PDF", ""
		}
		if res.Scanned {
			return attachmentInvalid, "PDF has no extractable text (scanned or image-only)", ""
		}
		return attachmentOK, "", res.Markdown
	}

	if limit := config.MaxAttachmentTextBytes(); len(raw) > limit {
		return attachmentInvalid, fmt.Sprintf("file is too large (limit %d bytes)", limit), ""
	}
	if !utf8.Valid(raw) {
		return attachmentInvalid, "file is not valid UTF-8 text", ""
	}
	return attachmentOK, "", string(raw)
}
