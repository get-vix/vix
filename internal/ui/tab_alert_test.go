package ui

import "testing"

// TestHasBackgroundThreadAlert verifies that a thread waiting for input only
// drives the Threads-tab blink when the user isn't already viewing it.
func TestHasBackgroundThreadAlert(t *testing.T) {
	waiting := func() *ThreadState { return &ThreadState{agentState: StateUserQuestion} }
	idle := func() *ThreadState { return &ThreadState{agentState: StateWaitingForInput} }

	tests := []struct {
		name      string
		threads   []*ThreadState
		activeTab TabKind
		selected  int
		wantBg    bool
		wantAny   bool
	}{
		{
			name:      "viewed asker: blink suppressed",
			threads:   []*ThreadState{waiting()},
			activeTab: TabKindChat,
			selected:  0,
			wantBg:    false,
			wantAny:   true,
		},
		{
			name:      "asker viewed but on another tab: still blinks",
			threads:   []*ThreadState{waiting()},
			activeTab: TabKindSettings,
			selected:  0,
			wantBg:    true,
			wantAny:   true,
		},
		{
			name:      "background asker while viewing an idle thread: blinks",
			threads:   []*ThreadState{idle(), waiting()},
			activeTab: TabKindChat,
			selected:  0,
			wantBg:    true,
			wantAny:   true,
		},
		{
			name:      "viewed asker plus background asker: blinks",
			threads:   []*ThreadState{waiting(), waiting()},
			activeTab: TabKindChat,
			selected:  0,
			wantBg:    true,
			wantAny:   true,
		},
		{
			name:      "no waiting threads: no blink",
			threads:   []*ThreadState{idle()},
			activeTab: TabKindChat,
			selected:  0,
			wantBg:    false,
			wantAny:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{threads: tt.threads, activeTab: tt.activeTab, selectedThread: tt.selected}
			if got := m.hasBackgroundThreadAlert(); got != tt.wantBg {
				t.Errorf("hasBackgroundThreadAlert() = %v, want %v", got, tt.wantBg)
			}
			if got := m.hasAlertThreads(); got != tt.wantAny {
				t.Errorf("hasAlertThreads() = %v, want %v", got, tt.wantAny)
			}
		})
	}
}
