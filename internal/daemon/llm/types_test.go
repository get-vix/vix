package llm

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMessageParamTimestampRoundTrip(t *testing.T) {
	ts := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	in := NewUserMessage(NewTextBlock("hi"))
	in.Timestamp = ts

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out MessageParam
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Timestamp.Equal(ts) {
		t.Errorf("round-trip Timestamp = %v, want %v", out.Timestamp, ts)
	}
}

// A message persisted without a timestamp (the zero value) or from a record
// predating this field must round-trip such that IsZero() stays true — that is
// the gate the replay path uses to omit the "Sent at" line for legacy threads.
func TestMessageParamTimestampZeroRoundTripsAsLegacy(t *testing.T) {
	data, err := json.Marshal(NewUserMessage(NewTextBlock("hi")))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MessageParam
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Timestamp.IsZero() {
		t.Errorf("zero Timestamp should round-trip as IsZero(); got %v", out.Timestamp)
	}

	var legacy MessageParam
	if err := json.Unmarshal([]byte(`{"role":"user","content":[]}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if !legacy.Timestamp.IsZero() {
		t.Errorf("absent timestamp should be zero; got %v", legacy.Timestamp)
	}
}
