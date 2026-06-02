package auth

import (
	"context"
	"time"
)

// nowMillis returns the current time in Unix milliseconds. It is a package
// variable so tests can install a deterministic clock when exercising token
// expiry and device-code timeouts.
var nowMillis = func() int64 { return time.Now().UnixMilli() }

// sleepCtx sleeps for d or until ctx is cancelled, whichever comes first. It
// is a package variable so device-code polling tests can run without real
// delays.
var sleepCtx = func(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
