package daemon

import (
	"log"
	"os"
	"path/filepath"
)

// appendThinking writes a thinking delta to this thread's dedicated thinking
// log file. The file is opened lazily on the first delta and named after the
// thread ID so concurrent threads never interleave into the same file.
func (s *Thread) appendThinking(delta string) {
	if delta == "" {
		return
	}
	s.thinkingLogMu.Lock()
	defer s.thinkingLogMu.Unlock()
	if s.thinkingLogFile == nil {
		path := filepath.Join(TmpLogDir(), "vix-thinking-"+s.id+".log")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Printf("[thinking] failed to open log for thread %s: %v", s.id, err)
			return
		}
		s.thinkingLogFile = f
	}
	_, _ = s.thinkingLogFile.WriteString(delta)
	s.thinkingInTurn = true
}

// thinkingBoundary writes a turn-separator line so successive turns are
// distinguishable when grepping the log. No-op for turns that wrote no
// thinking content.
func (s *Thread) thinkingBoundary() {
	s.thinkingLogMu.Lock()
	defer s.thinkingLogMu.Unlock()
	if s.thinkingLogFile != nil && s.thinkingInTurn {
		_, _ = s.thinkingLogFile.WriteString("\n--- end of turn ---\n")
		s.thinkingInTurn = false
	}
}

// closeThinkingLog closes the thread's thinking log file if it was opened.
// Called from Thread.Run()'s defer so it runs exactly once when the thread
// shuts down.
func (s *Thread) closeThinkingLog() {
	s.thinkingLogMu.Lock()
	defer s.thinkingLogMu.Unlock()
	if s.thinkingLogFile != nil {
		s.thinkingLogFile.Close()
		s.thinkingLogFile = nil
	}
}
