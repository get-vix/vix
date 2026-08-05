package protocol

import (
	"encoding/base64"
	"testing"
)

func TestValidateAttachment_Image(t *testing.T) {
	ok := Attachment{Type: "image", MediaType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("x"))}
	if err := ValidateAttachment(ok); err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	if err := ValidateAttachment(Attachment{Type: "image", MediaType: "text/plain", Data: "eA=="}); err == nil {
		t.Error("image with non-image media type should be rejected")
	}
	if err := ValidateAttachment(Attachment{Type: "image", MediaType: "image/png", Data: "not base64!!"}); err == nil {
		t.Error("image with invalid base64 should be rejected")
	}
}

func TestValidateAttachment_File(t *testing.T) {
	if err := ValidateAttachment(Attachment{Type: "file", Path: "/tmp/notes.txt"}); err != nil {
		t.Fatalf("valid file attachment rejected: %v", err)
	}
	// A file attachment needs a path; Data is not required.
	if err := ValidateAttachment(Attachment{Type: "file"}); err == nil {
		t.Error("file attachment without path should be rejected")
	}
}

func TestValidateAttachment_UnknownType(t *testing.T) {
	if err := ValidateAttachment(Attachment{Type: "video", Path: "/tmp/x.mp4"}); err == nil {
		t.Error("unknown attachment type should be rejected")
	}
}
