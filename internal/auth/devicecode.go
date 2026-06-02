package auth

import (
	"context"
	"errors"
	"time"
)

// Device-code polling messages and constants, ported from pi's device-code.ts.
const (
	deviceCancelMessage          = "Login cancelled"
	deviceTimeoutMessage         = "Device flow timed out"
	deviceSlowDownTimeoutMessage = "Device flow timed out after one or more slow_down responses. " +
		"This is often caused by clock drift in WSL or VM environments. " +
		"Please sync or restart the VM clock and try again."

	deviceMinIntervalMS = 1000
	// RFC 8628 §3.2: when the server omits `interval`, use 5 seconds.
	deviceDefaultPollIntervalSeconds = 5
	// RFC 8628 §3.5: `slow_down` means increase the interval by 5 seconds.
	deviceSlowDownIncrementMS = 5000
)

// pollStatus is the outcome of a single device-code poll.
type pollStatus int

const (
	pollPending pollStatus = iota
	pollSlowDown
	pollFailed
	pollComplete
)

// pollResult is one poll's result. For pollComplete, Value holds the payload.
// For pollFailed, Message holds the error text.
type pollResult[T any] struct {
	Status  pollStatus
	Value   T
	Message string
}

// devicePollOptions configures pollDeviceCode.
type devicePollOptions[T any] struct {
	// Label identifies the flow in log output (e.g. the provider id).
	Label            string
	IntervalSeconds  int
	ExpiresInSeconds int
	// Poll performs one poll against the token endpoint.
	Poll func(ctx context.Context) (pollResult[T], error)
}

// pollDeviceCode drives an RFC 8628 device-authorization poll loop until the
// flow completes, fails, times out, or ctx is cancelled. It mirrors pi's
// pollOAuthDeviceCodeFlow, including slow_down handling and the WSL clock-drift
// hint on timeout.
func pollDeviceCode[T any](ctx context.Context, opts devicePollOptions[T]) (T, error) {
	var zero T

	deadline := int64(-1) // sentinel: no deadline
	if opts.ExpiresInSeconds > 0 {
		deadline = nowMillis() + int64(opts.ExpiresInSeconds)*1000
	}

	intervalSeconds := opts.IntervalSeconds
	if intervalSeconds <= 0 {
		intervalSeconds = deviceDefaultPollIntervalSeconds
	}
	intervalMS := intervalSeconds * 1000
	if intervalMS < deviceMinIntervalMS {
		intervalMS = deviceMinIntervalMS
	}

	lg().Debug("device poll: starting", "flow", opts.Label, "interval_ms", intervalMS, "expires_in_s", opts.ExpiresInSeconds)

	slowDownResponses := 0
	attempt := 0
	for deadline < 0 || nowMillis() < deadline {
		if err := ctx.Err(); err != nil {
			lg().Warn("device poll: cancelled", "flow", opts.Label, "attempts", attempt, "err", err)
			return zero, errors.New(deviceCancelMessage)
		}

		attempt++
		result, err := opts.Poll(ctx)
		if err != nil {
			lg().Error("device poll: poll request errored", "flow", opts.Label, "attempt", attempt, "err", err)
			return zero, err
		}

		switch result.Status {
		case pollComplete:
			lg().Info("device poll: authorized", "flow", opts.Label, "attempts", attempt)
			return result.Value, nil
		case pollFailed:
			lg().Error("device poll: server reported failure", "flow", opts.Label, "attempt", attempt, "reason", result.Message)
			return zero, errors.New(result.Message)
		case pollSlowDown:
			slowDownResponses++
			// RFC 8628 §3.5: apply the increase to this and all later requests.
			intervalMS += deviceSlowDownIncrementMS
			if intervalMS < deviceMinIntervalMS {
				intervalMS = deviceMinIntervalMS
			}
			lg().Warn("device poll: slow_down", "flow", opts.Label, "attempt", attempt, "new_interval_ms", intervalMS, "slow_downs", slowDownResponses)
		case pollPending:
			lg().Debug("device poll: pending", "flow", opts.Label, "attempt", attempt)
		}

		waitMS := intervalMS
		if deadline >= 0 {
			remaining := deadline - nowMillis()
			if remaining <= 0 {
				break
			}
			if remaining < int64(waitMS) {
				waitMS = int(remaining)
			}
		}

		if err := sleepCtx(ctx, time.Duration(waitMS)*time.Millisecond); err != nil {
			lg().Warn("device poll: cancelled during wait", "flow", opts.Label, "attempts", attempt, "err", err)
			return zero, errors.New(deviceCancelMessage)
		}
	}

	if slowDownResponses > 0 {
		lg().Error("device poll: timed out after slow_down", "flow", opts.Label, "attempts", attempt, "slow_downs", slowDownResponses)
		return zero, errors.New(deviceSlowDownTimeoutMessage)
	}
	lg().Error("device poll: timed out", "flow", opts.Label, "attempts", attempt)
	return zero, errors.New(deviceTimeoutMessage)
}
