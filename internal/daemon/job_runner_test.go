package daemon

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/get-vix/vix/internal/daemon/jobs"
)

func TestJobRunResultFromUnattendedTimeout(t *testing.T) {
	res := jobRunResultFromUnattended(unattendedRunResult{
		SessionID:  "run-1",
		AgentTurns: 2,
		TimedOut:   true,
		Err:        "run cancelled: context deadline exceeded",
	})

	if res.Status != jobs.StatusTimeout {
		t.Fatalf("status = %q, want %q", res.Status, jobs.StatusTimeout)
	}
	if res.Err != "run cancelled: context deadline exceeded" {
		t.Fatalf("err = %q", res.Err)
	}
	if res.SessionID != "run-1" || res.AgentTurns != 2 {
		t.Fatalf("session/turns = %q/%d", res.SessionID, res.AgentTurns)
	}
	if len(res.Errors) != 1 || res.Errors[0].Source != "timeout" || res.Errors[0].Message != res.Err {
		t.Fatalf("errors = %+v", res.Errors)
	}
}

func TestJobRunResultFromUnattendedDenials(t *testing.T) {
	res := jobRunResultFromUnattended(unattendedRunResult{
		SessionID:       "run-2",
		AgentTurns:      1,
		ConfirmRequests: []string{"bash", "edit_file"},
	})

	if res.Status != jobs.StatusOK {
		t.Fatalf("status = %q, want %q", res.Status, jobs.StatusOK)
	}
	wantErr := "needed approval for: bash; edit_file"
	if res.Err != wantErr {
		t.Fatalf("err = %q, want %q", res.Err, wantErr)
	}
	if !reflect.DeepEqual(res.Denials, []string{"bash", "edit_file"}) {
		t.Fatalf("denials = %+v", res.Denials)
	}
	if len(res.Errors) != 1 || res.Errors[0].Source != "denied" || res.Errors[0].Message != wantErr {
		t.Fatalf("errors = %+v", res.Errors)
	}
}

func TestJobRunResultFromUnattendedAgentErrorWithDenials(t *testing.T) {
	res := jobRunResultFromUnattended(unattendedRunResult{
		SessionID:       "run-3",
		ConfirmRequests: []string{"bash"},
		HadError:        true,
		ErrSource:       "agent",
		Err:             "model failed",
	})

	if res.Status != jobs.StatusError {
		t.Fatalf("status = %q, want %q", res.Status, jobs.StatusError)
	}
	if res.Err != "model failed" {
		t.Fatalf("err = %q", res.Err)
	}
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if res.Errors[0].Source != "agent" || res.Errors[0].Message != "model failed" {
		t.Fatalf("first error = %+v", res.Errors[0])
	}
	if res.Errors[1].Source != "denied" || res.Errors[1].Message != "needed approval for: bash" {
		t.Fatalf("second error = %+v", res.Errors[1])
	}
}

func TestJobRunResultFromUnattendedEmptyErrorStillFails(t *testing.T) {
	res := jobRunResultFromUnattended(unattendedRunResult{
		SessionID: "run-4",
		HadError:  true,
	})

	if res.Status != jobs.StatusError {
		t.Fatalf("status = %q, want %q", res.Status, jobs.StatusError)
	}
	if res.Err != "agent run failed" {
		t.Fatalf("err = %q, want fallback", res.Err)
	}
	if len(res.Errors) != 1 || res.Errors[0].Source != "agent" || res.Errors[0].Message != "agent run failed" {
		t.Fatalf("errors = %+v", res.Errors)
	}
}

func TestUnattendedPolicyErrorAfterCancelMapsToTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := unattendedPolicyErrorResult(ctx, unattendedRunResult{SessionID: "run-5"}, "partial output", errors.New("unhandled unattended event: event.confirm_request"))

	if !res.TimedOut {
		t.Fatal("TimedOut = false, want true")
	}
	if res.ErrSource != "timeout" {
		t.Fatalf("ErrSource = %q, want timeout", res.ErrSource)
	}
	if res.Err != "run cancelled: context canceled" {
		t.Fatalf("Err = %q", res.Err)
	}
	if res.FinalText != "partial output" {
		t.Fatalf("FinalText = %q", res.FinalText)
	}
}
