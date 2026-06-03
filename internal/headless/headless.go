package headless

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/protocol"
)

// OutputFormat controls how the headless result is printed.
type OutputFormat string

const (
	FormatText       OutputFormat = "text"
	FormatJSON       OutputFormat = "json"
	FormatStreamJSON OutputFormat = "stream-json"
)

// Valid returns true if the format is recognized.
func (f OutputFormat) Valid() bool {
	return f == FormatText || f == FormatJSON || f == FormatStreamJSON
}

// result is the final JSON output structure (for json and stream-json modes).
type result struct {
	Type       string         `json:"type"`
	Result     string         `json:"result"`
	SessionID  string         `json:"session_id"`
	IsError    bool           `json:"is_error"`
	NumTurns   int            `json:"num_turns"`
	DurationMs int64          `json:"duration_ms"`
	Usage      usageStats     `json:"usage"`
	Steps      []stepDuration `json:"steps,omitempty"`
}

// stepDuration captures how long each workflow step took.
type stepDuration struct {
	StepID     string `json:"step_id"`
	StepIdx    int    `json:"step_idx"`
	Success    bool   `json:"success"`
	DurationMs int64  `json:"duration_ms"`
}

// timestampedEvent wraps a SessionEvent with a wall-clock timestamp so
// stream-json consumers can compute per-event durations after the fact
// (e.g. find which workflow step or tool call took the longest).
type timestampedEvent struct {
	Timestamp string `json:"ts"`
	Type      string `json:"type"`
	Data      any    `json:"data"`
}

type usageStats struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
}

// Run executes a single prompt in headless mode, consuming session events
// and producing output in the requested format.
func Run(session *daemon.SessionClient, prompt string, format OutputFormat, workflow string) error {
	start := time.Now()

	// Handle signals for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		session.SendCancel()
		session.SendClose()
		os.Exit(130)
	}()
	defer signal.Stop(sigCh)

	// Send the prompt (or trigger a workflow)
	if workflow != "" {
		if err := session.SendWorkflow(workflow, prompt); err != nil {
			return fmt.Errorf("send workflow: %w", err)
		}
	} else {
		if err := session.SendInput(prompt, nil); err != nil {
			return fmt.Errorf("send input: %w", err)
		}
	}

	var (
		textBuf  strings.Builder
		usage    usageStats
		numTurns int
		hadError bool
		errMsg   string
		steps    []stepDuration
	)

	stdoutW := bufio.NewWriter(os.Stdout)
	jsonEnc := json.NewEncoder(stdoutW)

	for {
		event, err := session.ReadEvent()
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read event: %w", err)
		}

		// Forward raw event in stream-json mode, prefixed with a wall-clock
		// timestamp so consumers can attribute time to individual events.
		if format == FormatStreamJSON {
			jsonEnc.Encode(timestampedEvent{
				Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
				Type:      event.Type,
				Data:      event.Data,
			})
			stdoutW.Flush()
		}

		switch event.Type {
		case "event.stream_chunk":
			chunk := decodeEvent[protocol.EventStreamChunk](event.Data)
			textBuf.WriteString(chunk.Text)

		case "event.stream_done":
			done := decodeEvent[protocol.EventStreamDone](event.Data)
			usage.InputTokens += done.InputTokens
			usage.OutputTokens += done.OutputTokens
			usage.CacheCreationTokens += done.CacheCreationTokens
			usage.CacheReadTokens += done.CacheReadTokens
			numTurns++

		case "event.confirm_request":
			// Auto-approve in headless mode (never persist dirs)
			session.SendConfirm(true, false)

		case "event.user_question":
			// Auto-select first option in headless mode
			uq := decodeEvent[protocol.EventUserQuestion](event.Data)
			if len(uq.RichOptions) > 0 {
				session.SendUserAnswer(uq.RichOptions[0].Title, "")
			} else if len(uq.Options) > 0 {
				session.SendUserAnswer(uq.Options[0], "")
			} else {
				session.SendUserAnswer("", "")
			}

		case "event.plan_proposed":
			// Auto-approve plans in headless mode
			session.SendPlanAction("approve", "")

		case "event.tool_call":
			if format == FormatText {
				tc := decodeEvent[protocol.EventToolCall](event.Data)
				fmt.Fprintf(os.Stderr, "[tool] %s: %s\n", tc.Name, tc.Summary)
			}

		case "event.tool_result":
			if format == FormatText {
				tr := decodeEvent[protocol.EventToolResult](event.Data)
				if tr.IsError {
					fmt.Fprintf(os.Stderr, "[tool error] %s: %s\n", tr.Name, tr.Output)
				}
			}

		case "event.workflow_step_start":
			if format == FormatText {
				ss := decodeEvent[protocol.EventWorkflowStepStart](event.Data)
				fmt.Fprintf(os.Stderr, "[step %d/%d] %s starting\n", ss.StepIdx, ss.Total, ss.StepID)
			}

		case "event.workflow_step_done":
			sd := decodeEvent[protocol.EventWorkflowStepDone](event.Data)
			steps = append(steps, stepDuration{
				StepID:     sd.StepID,
				StepIdx:    sd.StepIdx,
				Success:    sd.Success,
				DurationMs: sd.DurationMs,
			})
			if format == FormatText {
				status := "ok"
				if !sd.Success {
					status = "failed"
				}
				fmt.Fprintf(os.Stderr, "[step %d/%d] %s %s in %s\n",
					sd.StepIdx, sd.Total, sd.StepID, status, formatDuration(sd.DurationMs))
			}

		case "event.error":
			e := decodeEvent[protocol.EventError](event.Data)
			hadError = true
			errMsg = e.Message
			fmt.Fprintf(os.Stderr, "Error: %s\n", e.Message)

		case "event.agent_done":
			durationMs := time.Since(start).Milliseconds()

			switch format {
			case FormatText:
				text := textBuf.String()
				if text != "" {
					if !strings.HasSuffix(text, "\n") {
						text += "\n"
					}
					fmt.Print(text)
				}

			case FormatJSON, FormatStreamJSON:
				resultText := textBuf.String()
				if hadError && resultText == "" {
					resultText = errMsg
				}
				r := result{
					Type:       "result",
					Result:     resultText,
					SessionID:  session.SessionID(),
					IsError:    hadError,
					NumTurns:   numTurns,
					DurationMs: durationMs,
					Usage:      usage,
					Steps:      steps,
				}
				jsonEnc.Encode(r)
				stdoutW.Flush()
			}
			return nil

		case "event.quit":
			return nil

		case "event.init_state":
			// Ignore init state updates silently
		}
	}

	return nil
}

// formatDuration renders milliseconds as a short, human-readable string.
func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Second {
		return fmt.Sprintf("%dms", ms)
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d / time.Minute)
	s := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%ds", m, s)
}

// decodeEvent unmarshals event.Data into the given type.
func decodeEvent[T any](data any) T {
	var out T
	raw, err := json.Marshal(data)
	if err != nil {
		return out
	}
	json.Unmarshal(raw, &out)
	return out
}
