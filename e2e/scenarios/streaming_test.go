package scenarios

import (
	"strings"
	"testing"
	"time"

	"github.com/get-vix/vix/e2e/harness"
)

// TestThinkingThenAnswer drives a turn whose model reply carries a thinking
// block before the visible answer. It proves vix parses the thinking channel
// without breaking the turn and still renders the final answer. Exercises the
// harness Thinking primitive.
func TestThinkingThenAnswer(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "stream",
		Subcategory: "stream.thinking",
		Description: "model emits a thinking block, then the visible answer renders",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)
	h.UI.Shot("initial")

	h.Mock.Enqueue(
		harness.Thinking("weighing the options carefully", "The answer is 42."),
	)
	h.UI.Type("think it through, then answer")
	h.UI.Enter()

	h.UI.WaitFor("The answer is 42.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-answer")

	if !h.UI.Contains("The answer is 42.") {
		t.Fatalf("final answer not rendered; screen:\n%s", h.UI.Snapshot())
	}
}

// TestStreamingMultiChunk delivers the assistant turn as several text deltas
// rather than one, mirroring real provider streaming. It asserts the chunks
// reassemble into the full message on screen. Exercises the harness TextChunks
// primitive (also the OpenAI multi-chunk regression surface).
func TestStreamingMultiChunk(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "stream",
		Subcategory: "stream.chunks",
		Description: "a multi-delta assistant turn reassembles into the full message",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.TextChunks("Streaming ", "works ", "across ", "chunks."),
	)
	h.UI.Type("stream a multi-part reply")
	h.UI.Enter()

	h.UI.WaitFor("Streaming works across chunks.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-stream")

	if !h.UI.Contains("Streaming works across chunks.") {
		t.Fatalf("reassembled message not rendered; screen:\n%s", h.UI.Snapshot())
	}
}

// TestRetryAfterOverload returns a retryable 503 on the first attempt and a
// normal reply on the retry. It proves vix's retry loop recovers transparently:
// the recovered text renders and the model saw two requests. Exercises the
// harness HTTPError primitive.
func TestRetryAfterOverload(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "stream",
		Subcategory: "stream.retry",
		Description: "a retryable API error is retried and the turn recovers",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	// First turn fails (retryable), the retry succeeds.
	h.Mock.Enqueue(
		harness.HTTPError(503, "service temporarily unavailable"),
		harness.Text("Recovered after the retry."),
	)
	h.UI.Type("do the thing")
	h.UI.Enter()

	h.UI.WaitFor("Recovered after the retry.")
	h.UI.WaitStable(300 * time.Millisecond)
	h.UI.Shot("after-retry")

	if got := len(h.Mock.Requests()); got < 2 {
		t.Fatalf("expected at least 2 requests (error + retry), got %d", got)
	}
	if !h.UI.Contains("Recovered after the retry.") {
		t.Fatalf("recovered text not rendered; screen:\n%s", h.UI.Snapshot())
	}
}

// TestNonRetryableErrorSurfaces returns a non-retryable 400. It proves the
// failure is surfaced (not retried into oblivion and not a crash): the daemon
// log records the bad-request classification. Exercises the harness HTTPError
// primitive on the fail-fast path.
func TestNonRetryableErrorSurfaces(t *testing.T) {
	h := harness.Start(t, harness.Meta{
		Category:    "stream",
		Subcategory: "stream.error",
		Description: "a non-retryable API error fails fast and is surfaced",
		Wire:        harness.WireMessages,
	})

	h.UI.WaitStable(400 * time.Millisecond)

	h.Mock.Enqueue(
		harness.HTTPError(400, "malformed-request-marker"),
	)
	h.UI.Type("trigger a bad request")
	h.UI.Enter()
	h.WaitForLLMRequests(1)
	h.UI.WaitStable(500 * time.Millisecond)
	h.UI.Shot("after-error")

	// The turn must not have been retried (single request) and the error must be
	// recorded in the daemon log.
	if got := len(h.Mock.Requests()); got != 1 {
		t.Fatalf("non-retryable error should not retry; requests=%d", got)
	}
	if log := h.Daemon.LogTail(200); !strings.Contains(log, "400") && !strings.Contains(strings.ToLower(log), "bad request") {
		t.Fatalf("daemon log does not record the bad-request error:\n%s", log)
	}
}
