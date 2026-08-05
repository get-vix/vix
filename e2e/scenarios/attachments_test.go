package scenarios

import (
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestAttachTextAndPDFFiles proves a user can attach text and PDF files to a
// prompt the same way images are attached: the path is recognised on submit,
// the daemon reads the file (converting PDFs with the built-in reader), and the
// extracted text is embedded into the user message that goes over the wire. It
// also checks the type-aware placeholders render on screen.
func TestAttachTextAndPDFFiles(t *testing.T) {
	const txtMarker = "TODO refactor the scheduler loop"
	const pdfMarker = "Quarterly revenue up twelve percent"

	h := harness.Start(t, harness.Meta{
		Category:    "files",
		Subcategory: "files.attach",
		Description: "user attaches a text file and a PDF; the daemon embeds their extracted text into the prompt over the wire",
		Wire:        harness.WireMessages,
	},
		harness.WithWorkdirFile("notes.txt", txtMarker+"\n"),
		harness.WithWorkdirFile("report.pdf", pdfFixture(pdfMarker)),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(
		harness.Text("Got your notes."),
		harness.Text("Got your PDF."),
	)

	// Turn 1 — attach a text file by typing its absolute path inline. On submit
	// the path becomes a [File #1] placeholder and a path-only "file" attachment;
	// the daemon reads it and embeds the content.
	h.UI.Type("summarize " + h.FS.Path("notes.txt"))
	h.UI.Enter()
	h.UI.WaitFor("Got your notes.")
	h.UI.Shot("after-text")

	if !h.UI.Contains("[File #1]") {
		t.Fatalf("text attachment placeholder not rendered; screen:\n%s", h.UI.Snapshot())
	}
	if !anyRequestBodyContains(h, txtMarker) {
		t.Fatalf("text file content %q did not reach the wire (requests=%d)",
			txtMarker, len(h.Mock.Requests()))
	}

	// Turn 2 — attach a PDF. The daemon converts it with the in-process PDF
	// reader and embeds the extracted text.
	h.UI.Type("and this " + h.FS.Path("report.pdf"))
	h.UI.Enter()
	h.UI.WaitFor("Got your PDF.")
	h.UI.Shot("after-pdf")

	if !h.UI.Contains("[PDF #1]") {
		t.Fatalf("PDF attachment placeholder not rendered; screen:\n%s", h.UI.Snapshot())
	}
	if !anyRequestBodyContains(h, pdfMarker) {
		t.Fatalf("PDF extracted text %q did not reach the wire (requests=%d)",
			pdfMarker, len(h.Mock.Requests()))
	}
}

// TestAttachmentChipShownInTranscript proves that when a user sends an
// attachment, the sent message in the viewport shows a chip line (icon +
// filename) for it — not "nothing". The inline path is replaced by a
// [File #1] placeholder, so the filename reappearing on its own proves the
// chip rendered as a distinct line beneath the message.
func TestAttachmentChipShownInTranscript(t *testing.T) {
	const marker = "chip render check"

	h := harness.Start(t, harness.Meta{
		Category:    "files",
		Subcategory: "files.attach_chip",
		Description: "a sent attachment shows an icon+filename chip line in the user message transcript",
		Wire:        harness.WireMessages,
	},
		harness.WithWorkdirFile("notes.txt", marker+"\n"),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(harness.Text("Got it."))

	h.UI.Type("summarize " + h.FS.Path("notes.txt"))
	h.UI.Enter()
	h.UI.WaitFor("Got it.")
	h.UI.Shot("after-send")

	// The path is replaced by a placeholder in the message body...
	if !h.UI.Contains("[File #1]") {
		t.Fatalf("attachment placeholder not rendered; screen:\n%s", h.UI.Snapshot())
	}
	// ...and the attachment chip line (icon + filename) shows the file.
	if !h.UI.Contains("📎  notes.txt") {
		t.Fatalf("attachment chip line not rendered in transcript; screen:\n%s", h.UI.Snapshot())
	}
}

// OUTSIDE the thread's working directory (here, under HOME — mirroring an
// iCloud path like ~/Library/Mobile Documents/...). A drag-drop is explicit
// user intent, so the daemon reads it and embeds its text even though the same
// path would be refused to the agent's read_file tool. No "outside working
// directory" error must appear.
func TestAttachFileOutsideWorkingDir(t *testing.T) {
	const marker = "Retirement blunder number seven: timing the market"

	h := harness.Start(t, harness.Meta{
		Category:    "files",
		Subcategory: "files.attach_outside",
		Description: "user attaches a file outside the working directory (under HOME); the daemon embeds its text without an outside-working-directory error",
		Wire:        harness.WireMessages,
	},
		harness.WithHomeFile("Books/blunders.txt", marker+"\n"),
	)

	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(harness.Text("Got your book."))

	h.UI.Type("summarize " + h.HomePath("Books/blunders.txt"))
	h.UI.Enter()
	h.UI.WaitFor("Got your book.")
	h.UI.Shot("after-outside-file")

	if !h.UI.Contains("[File #1]") {
		t.Fatalf("outside-cwd attachment placeholder not rendered; screen:\n%s", h.UI.Snapshot())
	}
	if h.UI.Contains("outside working directory") {
		t.Fatalf("attachment was refused for being outside the working directory; screen:\n%s", h.UI.Snapshot())
	}
	if !anyRequestBodyContains(h, marker) {
		t.Fatalf("outside-cwd file content %q did not reach the wire (requests=%d)",
			marker, len(h.Mock.Requests()))
	}
}
