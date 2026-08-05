package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// Tier 3 — file & search tool breadth. Each proves a tool's execute→feedback
// loop across disk and wire. These are daemon-internal handlers (no bash exec,
// no fragile keystrokes), so they're deterministic. Messages wire.

// TestReadFile covers a full read and an offset/limit slice. files.read
func TestReadFile(t *testing.T) {
	t.Run("full", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category: "files", Subcategory: "files.read",
			Description: "read_file returns the file contents to the model",
			Wire:        harness.WireMessages, Variant: "full",
		})
		mustSeed(t, h, "data.txt", "alpha\nbeta\ngamma\n")
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("read_file", `{"path":"data.txt"}`),
			harness.Text("Read the whole file."),
		)
		h.UI.Type("read data.txt")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("Read the whole file.")

		if !anyToolResultContains(h, "alpha") || !anyToolResultContains(h, "gamma") {
			t.Fatalf("read_file did not return the contents; requests=%d", len(h.Mock.Requests()))
		}
	})

	t.Run("offset-limit", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category: "files", Subcategory: "files.read",
			Description: "read_file honours offset/limit (returns only the requested slice)",
			Wire:        harness.WireMessages, Variant: "offset-limit",
		})
		mustSeed(t, h, "data.txt", "alpha\nbeta\ngamma\n")
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("read_file", `{"path":"data.txt","offset":2,"limit":1}`),
			harness.Text("Read one line."),
		)
		h.UI.Type("read line 2 of data.txt")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("Read one line.")

		if !anyToolResultContains(h, "beta") {
			t.Fatal("offset/limit read did not return the target line")
		}
		if anyToolResultContains(h, "alpha") || anyToolResultContains(h, "gamma") {
			t.Fatal("offset/limit read leaked lines outside the slice")
		}
	})
}

// TestReadMinified proves the minified read strips comments while keeping code.
// read_minified only minifies when the VFS is enabled for the language in
// languages.json (off by default → it falls back to plain read_file), so the
// test seeds a config enabling it for Go. files.read_minified
func TestReadMinified(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "files", Subcategory: "files.read_minified",
		Description: "read_minified_file strips comments but keeps code symbols",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/config/languages.json",
		`{"languages":[{"name":"go","extensions":[".go"],"vfs":{"enable":true,"keep_comments":false}}]}`))
	mustSeed(t, h, "code.go", "package main\n\n// SECRET_COMMENT_MARKER stays out\nfunc Hello() {}\n")
	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("read_minified_file", `{"path":"code.go"}`),
		harness.Text("Read the minified file."),
	)
	h.UI.Type("read code.go minified")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Read the minified file.")

	if !anyToolResultContains(h, "Hello") {
		t.Fatal("minified read dropped the code symbol")
	}
	if anyToolResultContains(h, "SECRET_COMMENT_MARKER") {
		t.Fatal("minified read did not strip the comment")
	}
}

// TestEditFile covers an exact-match edit and the non-unique-match error.
// files.edit
func TestEditFile(t *testing.T) {
	t.Run("exact", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category: "files", Subcategory: "files.edit",
			Description: "edit_file applies a unique exact-match replacement on disk",
			Wire:        harness.WireMessages, Variant: "exact",
		})
		mustSeed(t, h, "greet.txt", "hello world\n")
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("read_file", `{"path":"greet.txt"}`),
			harness.ToolUse("edit_file", `{"path":"greet.txt","old_string":"world","new_string":"there"}`),
			harness.Text("Edited the file."),
		)
		h.UI.Type("read greet.txt then change world to there")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("Edited the file.")

		if got := string(h.FS.Read("greet.txt")); got != "hello there\n" {
			t.Fatalf("greet.txt = %q, want %q", got, "hello there\n")
		}
	})

	t.Run("non-unique", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category: "files", Subcategory: "files.edit",
			Description: "edit_file rejects a non-unique old_string and leaves the file unchanged",
			Wire:        harness.WireMessages, Variant: "non-unique",
		})
		mustSeed(t, h, "dup.txt", "x\nx\n")
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("read_file", `{"path":"dup.txt"}`),
			harness.ToolUse("edit_file", `{"path":"dup.txt","old_string":"x","new_string":"y"}`),
			harness.Text("The edit was rejected."),
		)
		h.UI.Type("read dup.txt then replace x")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("The edit was rejected.")

		if !anyToolResultContains(h, "must be unique") {
			t.Fatal("non-unique edit did not return the uniqueness error")
		}
		if got := string(h.FS.Read("dup.txt")); got != "x\nx\n" {
			t.Fatalf("dup.txt was modified by a rejected edit: %q", got)
		}
	})
}

// TestEditMinified proves the projected minified-edit path: the model matches a
// token in the minified view and the rename is spliced into the unminified
// source in place, preserving the original formatting byte-for-byte (no
// whole-file reformat). files.edit_minified
func TestEditMinified(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "files", Subcategory: "files.edit_minified",
		Description: "edit_minified_file splices a minified-matched rename into the unminified source",
		Wire:        harness.WireMessages,
	}, harness.WithHomeFile(".vix/config/languages.json",
		`{"languages":[{"name":"go","extensions":[".go"],"vfs":{"enable":true,"keep_comments":true}}]}`))
	mustSeed(t, h, "mod.go", "package main\n\n// OldName does nothing.\nfunc OldName() {}\n")
	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("read_minified_file", `{"path":"mod.go"}`),
		harness.ToolUse("edit_minified_file", `{"path":"mod.go","old_string":"func OldName(","new_string":"func NewName("}`),
		harness.Text("Renamed the function."),
	)
	h.UI.Type("read mod.go minified then rename OldName to NewName")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Renamed the function.")

	// The splice changes only the matched span; the blank line and the comment
	// are preserved exactly because the file is never reformatted.
	want := "package main\n\n// OldName does nothing.\nfunc NewName() {}\n"
	if got := string(h.FS.Read("mod.go")); got != want {
		t.Fatalf("mod.go = %q, want %q", got, want)
	}
}

// TestWriteFileModes covers parent-dir creation and the executable mode.
// files.write_modes
func TestWriteFileModes(t *testing.T) {
	t.Run("parent-dirs", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category: "files", Subcategory: "files.write_modes",
			Description: "write_file creates missing parent directories",
			Wire:        harness.WireMessages, Variant: "parent-dirs",
		})
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("write_file", `{"path":"a/b/c.txt","content":"nested"}`),
			harness.Text("Wrote the nested file."),
		)
		h.UI.Type("write a/b/c.txt")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("Wrote the nested file.")

		if got := string(h.FS.Read("a/b/c.txt")); got != "nested" {
			t.Fatalf("a/b/c.txt = %q, want %q", got, "nested")
		}
	})

	t.Run("executable", func(t *testing.T) {
		h := harness.Start(t, harness.Meta{
			Category: "files", Subcategory: "files.write_modes",
			Description: "write_file with mode 0755 produces an executable file",
			Wire:        harness.WireMessages, Variant: "executable",
		})
		h.UI.WaitStable(400 * time.Millisecond)

		h.Mock.Enqueue(
			harness.ToolUse("write_file", `{"path":"run.sh","content":"#!/bin/sh\necho hi\n","mode":"0755"}`),
			harness.Text("Wrote the script."),
		)
		h.UI.Type("write an executable script")
		h.UI.Enter()
		h.UI.ResolveToolPrompts("Wrote the script.")

		info, err := h.FS.Stat("run.sh")
		if err != nil {
			t.Fatalf("run.sh missing: %v", err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("run.sh is not executable: mode=%v", info.Mode())
		}
	})
}

// TestDeleteFile proves delete_file removes the file from disk. files.delete
func TestDeleteFile(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "files", Subcategory: "files.delete",
		Description: "delete_file removes the file from disk",
		Wire:        harness.WireMessages,
	})
	mustSeed(t, h, "trash.txt", "remove me\n")
	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("delete_file", `{"path":"trash.txt"}`),
		harness.Text("Deleted the file."),
	)
	h.UI.Type("delete trash.txt")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Deleted the file.")

	if h.FS.Exists("trash.txt") {
		t.Fatal("trash.txt still exists after delete_file")
	}
}

// TestGrepTool proves grep returns matches in path:line:content form. search.grep
func TestGrepTool(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "search", Subcategory: "search.grep",
		Description: "grep returns matching lines with file + line number",
		Wire:        harness.WireMessages,
	})
	mustSeed(t, h, "code.go", "package main\n\nfunc Target() {}\n")
	mustSeed(t, h, "other.txt", "nothing here\n")
	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("grep", `{"pattern":"Target","path":".","reason":"e2e"}`),
		harness.Text("Found the match."),
	)
	h.UI.Type("grep for Target")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Found the match.")

	if !anyToolResultContains(h, "code.go") || !anyToolResultContains(h, "Target") {
		t.Fatalf("grep result missing the match; requests=%d", len(h.Mock.Requests()))
	}
}

// TestGlobTool proves glob_files returns paths matching the pattern union.
// search.glob
func TestGlobTool(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category: "search", Subcategory: "search.glob",
		Description: "glob_files lists files matching the pattern, excluding non-matches",
		Wire:        harness.WireMessages,
	})
	mustSeed(t, h, "a.go", "package a\n")
	mustSeed(t, h, "sub/b.go", "package b\n")
	mustSeed(t, h, "c.txt", "text\n")
	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.ToolUse("glob_files", `{"pattern":["**/*.go"],"reason":"e2e"}`),
		harness.Text("Globbed the go files."),
	)
	h.UI.Type("list go files")
	h.UI.Enter()
	h.UI.ResolveToolPrompts("Globbed the go files.")

	if !anyToolResultContains(h, "a.go") || !anyToolResultContains(h, "b.go") {
		t.Fatalf("glob result missing the go files; requests=%d", len(h.Mock.Requests()))
	}
	if anyToolResultContains(h, "c.txt") {
		t.Fatal("glob returned a non-matching file")
	}
}
