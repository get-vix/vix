package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/get-vix/vix/internal/config"
	"github.com/get-vix/vix/internal/protocol"
	wf "github.com/get-vix/vix/internal/workflow"
)

type unattendedWorkflowRequest struct {
	Name   string
	Inline *wf.Def
}

type unattendedRunRequest struct {
	RunID     string
	Model     string
	CWD       string
	Title     string
	Trigger   *protocol.TriggerInfo
	Prompt    string
	Workflow  unattendedWorkflowRequest
	AutoWrite bool
	AutoDirs  bool
	JobID     string
}

type unattendedRunResult struct {
	SessionID       string
	FinalText       string
	AgentTurns      int
	ConfirmRequests []string
	HadError        bool
	Err             string
	ErrSource       string
	TimedOut        bool
}

type unattendedEventPolicy func(context.Context, *Session, protocol.SessionEvent) (handled bool, err error)

func decodeUnattendedEvent[T any](data any) T {
	var out T
	if data == nil {
		return out
	}
	if typed, ok := data.(T); ok {
		return typed
	}

	var raw []byte
	switch v := data.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		var err error
		raw, err = json.Marshal(v)
		if err != nil {
			return out
		}
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

func (s *Server) runUnattendedSession(ctx context.Context, req unattendedRunRequest, policy unattendedEventPolicy) unattendedRunResult {
	runID := req.RunID
	if runID == "" {
		runID = generateSessionID()
	}
	res := unattendedRunResult{SessionID: runID}

	model := req.Model
	if model == "" {
		model = s.model
	}
	session := NewSession(runID, s, nil, model, req.CWD, "", false, req.AutoWrite, req.AutoDirs, true, ctx)
	session.origin = "vix"
	session.trigger = req.Trigger
	session.title = req.Title
	if req.JobID != "" {
		if jobsRoot := config.NewVixPaths("", s.homeVixDir, "").Jobs(); jobsRoot != "" {
			session.jobDir = filepath.Join(jobsRoot, req.JobID)
			session.addAllowedDir(session.jobDir)
		}
	}

	s.sessionMu.Lock()
	s.sessions[runID] = session
	s.sessionMu.Unlock()
	s.broadcastSessionsChanged()
	defer func() {
		s.sessionMu.Lock()
		delete(s.sessions, runID)
		s.sessionMu.Unlock()
		session.cancel()
		s.broadcastSessionsChanged()
	}()

	go session.Run()

	startCmd, err := unattendedStartCommand(req)
	if err != nil {
		res.HadError = true
		res.Err = err.Error()
		res.ErrSource = "start_command"
		return res
	}
	if !session.pushCommand(ctx, startCmd) {
		res.HadError = true
		res.Err = "session refused start command"
		res.ErrSource = "start_refused"
		return res
	}

	var final strings.Builder
	for {
		select {
		case ev := <-session.eventChan:
			switch ev.Type {
			case "event.stream_chunk":
				final.WriteString(decodeUnattendedEvent[protocol.EventStreamChunk](ev.Data).Text)
				res.FinalText = final.String()
			case "event.stream_done":
				res.AgentTurns++
			case "event.confirm_request":
				cr := decodeUnattendedEvent[protocol.EventConfirmRequest](ev.Data)
				res.ConfirmRequests = append(res.ConfirmRequests, cr.ToolName)
				if err := runUnattendedEventPolicy(ctx, session, ev, policy); err != nil {
					res.HadError = true
					res.Err = err.Error()
					res.ErrSource = "policy"
					res.FinalText = final.String()
					session.persist()
					return res
				}
			case "event.user_question", "event.plan_proposed":
				if err := runUnattendedEventPolicy(ctx, session, ev, policy); err != nil {
					res.HadError = true
					res.Err = err.Error()
					res.ErrSource = "policy"
					res.FinalText = final.String()
					session.persist()
					return res
				}
			case "event.error":
				res.HadError = true
				res.Err = decodeUnattendedEvent[protocol.EventError](ev.Data).Message
				res.ErrSource = "agent"
			case "event.agent_done":
				res.FinalText = final.String()
				session.persist()
				return res
			}
		case <-ctx.Done():
			res = cancelledUnattendedResult(res, final.String(), ctx.Err())
			session.persist()
			return res
		case <-session.ctx.Done():
			res.FinalText = final.String()
			if err := ctx.Err(); err != nil {
				res = cancelledUnattendedResult(res, final.String(), err)
				session.persist()
				return res
			}
			res.HadError = true
			res.ErrSource = "session"
			res.Err = "session ended before agent completion"
			session.persist()
			return res
		}
	}
}

func cancelledUnattendedResult(res unattendedRunResult, final string, err error) unattendedRunResult {
	res.FinalText = final
	res.TimedOut = true
	res.HadError = true
	res.ErrSource = "timeout"
	res.Err = "run cancelled: " + err.Error()
	return res
}

func runUnattendedEventPolicy(ctx context.Context, session *Session, ev protocol.SessionEvent, policy unattendedEventPolicy) error {
	if policy == nil {
		return fmt.Errorf("unhandled unattended event: %s", ev.Type)
	}
	handled, err := policy(ctx, session, ev)
	if err != nil {
		return err
	}
	if !handled {
		return fmt.Errorf("unhandled unattended event: %s", ev.Type)
	}
	return nil
}

func unattendedStartCommand(req unattendedRunRequest) (protocol.SessionCommand, error) {
	switch {
	case req.Workflow.Inline != nil:
		raw, err := json.Marshal(req.Workflow.Inline)
		if err != nil {
			return protocol.SessionCommand{}, err
		}
		data, err := json.Marshal(protocol.SessionWorkflowData{Name: req.Workflow.Inline.Name, Text: req.Prompt, Workflow: raw})
		if err != nil {
			return protocol.SessionCommand{}, err
		}
		return protocol.SessionCommand{Type: "session.workflow", Data: data}, nil
	case req.Workflow.Name != "":
		data, err := json.Marshal(protocol.SessionWorkflowData{Name: req.Workflow.Name, Text: req.Prompt})
		if err != nil {
			return protocol.SessionCommand{}, err
		}
		return protocol.SessionCommand{Type: "session.workflow", Data: data}, nil
	default:
		data, err := json.Marshal(protocol.SessionInputData{Text: req.Prompt})
		if err != nil {
			return protocol.SessionCommand{}, err
		}
		return protocol.SessionCommand{Type: "session.input", Data: data}, nil
	}
}
