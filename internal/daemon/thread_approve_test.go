package daemon

import (
	"testing"
)

func TestApprovedWriteFiles_BasicFlow(t *testing.T) {
	s := &Thread{cwd: t.TempDir()}

	absPath := s.cwd + "/foo.go"

	// Initially not approved.
	if s.isWriteApproved(absPath) {
		t.Fatal("expected file not to be approved initially")
	}

	// Approve it.
	s.addApprovedWriteFile(absPath)

	// Now it should be approved.
	if !s.isWriteApproved(absPath) {
		t.Fatal("expected file to be approved after adding")
	}

	// A different file should not be approved.
	if s.isWriteApproved(s.cwd + "/bar.go") {
		t.Fatal("expected different file not to be approved")
	}
}

func TestApprovedWriteFiles_ThreadIsolation(t *testing.T) {
	s1 := &Thread{cwd: t.TempDir()}
	s2 := &Thread{cwd: t.TempDir()}

	absPath := "/tmp/shared.go"

	s1.addApprovedWriteFile(absPath)

	if !s1.isWriteApproved(absPath) {
		t.Fatal("expected file approved in thread 1")
	}
	if s2.isWriteApproved(absPath) {
		t.Fatal("expected file NOT approved in thread 2")
	}
}
