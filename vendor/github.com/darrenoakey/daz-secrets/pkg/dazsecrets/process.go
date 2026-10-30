package dazsecrets

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"
)

func (c *Client) invoke(parent context.Context, path, providerID string, req request) (response, error) {
	if err := validateProvider(path); err != nil {
		return response{}, err
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if ctx.Err() != nil {
		return response{}, &Error{Code: CodeDeadline}
	}
	cmd := exec.Command(path, "--stdio")
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = io.Discard
	stdin, stdout, err := processPipes(cmd)
	if err != nil {
		return response{}, &Error{Code: CodeUnavailable}
	}
	if err = cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return response{}, &Error{Code: CodeUnavailable}
	}
	result := make(chan exchangeResult, 1)
	go exchange(ctx, cmd, stdin, stdout, req, result)
	select {
	case completed := <-result:
		return validateResponse(completed.response, completed.err, req.ID, providerID, req.Operation)
	case <-ctx.Done():
		killGroup(cmd)
		<-result
		return response{}, &Error{Code: CodeDeadline}
	}
}

type exchangeResult struct {
	response response
	err      error
}

func processPipes(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	return stdin, stdout, nil
}

func exchange(ctx context.Context, cmd *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser, req request, result chan<- exchangeResult) {
	req.DeadlineMS = deadlineMilliseconds(ctx)
	err := writeFrame(stdin, req)
	closeErr := stdin.Close()
	var resp response
	if err == nil && closeErr == nil {
		err = readFrame(stdout, &resp)
	}
	if err == nil {
		err = ensureEOF(stdout)
	}
	waitErr := cmd.Wait()
	if err == nil {
		err = waitErr
	}
	result <- exchangeResult{response: resp, err: err}
}

func ensureEOF(reader io.Reader) error {
	var extra [1]byte
	n, err := reader.Read(extra[:])
	if n != 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return errors.New("extra provider output")
	}
	return nil
}

func validateResponse(resp response, err error, id []byte, providerID, operation string) (response, error) {
	if err != nil {
		return response{}, &Error{Code: CodeCorrupt}
	}
	if !bytes.Equal(resp.ID, id) || resp.ProviderID != providerID || resp.Major != protocolMajor || resp.Minor != protocolMinor {
		return response{}, &Error{Code: CodeCorrupt}
	}
	if !resp.OK {
		if resp.Value != nil || resp.Revision != "" || resp.Deleted || resp.Metadata != nil {
			return response{}, &Error{Code: CodeCorrupt}
		}
		return response{}, typedError(resp.Code)
	}
	if resp.Code != "" {
		return response{}, &Error{Code: CodeCorrupt}
	}
	if !validSuccess(resp, operation) {
		return response{}, &Error{Code: CodeCorrupt}
	}
	return resp, nil
}

func validSuccess(resp response, operation string) bool {
	switch operation {
	case "get":
		return resp.Value != nil && len(*resp.Value) <= maxValue && validateRevision(resp.Revision) && !resp.Deleted && resp.Metadata == nil
	case "set":
		return resp.Value == nil && validateRevision(resp.Revision) && !resp.Deleted && resp.Metadata == nil
	case "delete":
		return resp.Value == nil && resp.Revision == "" && resp.Deleted && resp.Metadata == nil
	case "list_metadata":
		if resp.Value != nil || resp.Revision != "" || resp.Deleted || resp.Metadata == nil {
			return false
		}
		for _, item := range *resp.Metadata {
			if !validateName(item.Service) || !validateName(item.Account) || !validateRevision(item.Revision) {
				return false
			}
		}
		return true
	case "info":
		return resp.Value == nil && resp.Revision == "" && !resp.Deleted && resp.Metadata == nil
	default:
		return false
	}
}

func deadlineMilliseconds(ctx context.Context) uint64 {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0
	}
	remainingMS := time.Until(deadline).Milliseconds()
	if remainingMS < 1 {
		return 1
	}
	return uint64(remainingMS)
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
