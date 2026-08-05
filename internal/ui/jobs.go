package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/get-vix/vix/internal/daemon"
	"github.com/get-vix/vix/internal/protocol"
)

// vixThreadsMsg carries the persisted, not-currently-attached thread records
// shown in the Threads tab: vix-initiated ones (job runs, synthetic alerts) in
// their own group, and user-initiated ones from every working directory (userSums),
// which the tab groups by directory alongside the live threads.
type vixThreadsMsg struct {
	sums     []protocol.ThreadSummary
	userSums []protocol.ThreadSummary
}

// fetchVixThreads lists the persisted open threads (across every working
// directory) and keeps the not-currently-attached ones, split into
// vix-initiated (sums) and user-initiated (userSums) groups. Triggered on Init,
// on entering the Threads tab, and on event.threads_changed broadcasts.
func fetchVixThreads(socketPath, cwd, configDir, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		sums, err := client.ListThreads(cwd, configDir)
		if err != nil {
			return vixThreadsMsg{}
		}
		var vixOut, userOut []protocol.ThreadSummary
		for _, s := range sums {
			if s.Attached {
				continue
			}
			if s.Origin == "vix" {
				vixOut = append(vixOut, s)
			} else {
				userOut = append(userOut, s)
			}
		}
		return vixThreadsMsg{sums: vixOut, userSums: userOut}
	}
}

// dismissVixThread archives a vix-initiated record (open/ → closed/) without
// attaching it, then refreshes the list.
func dismissVixThread(socketPath, cwd, configDir, authToken, id string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.DismissThread(cwd, configDir, id)
		return fetchVixThreads(socketPath, cwd, configDir, authToken)()
	}
}

// jobsListMsg carries the scheduled jobs and lifecycle hooks shown in the Jobs
// & Triggers tab.
type jobsListMsg struct {
	jobs  []protocol.JobSummary
	hooks []protocol.HookSummary
}

// fetchJobsAndHooks lists the scheduled jobs and lifecycle hooks. Triggered on
// entering the Jobs & Triggers tab and on event.jobs_changed broadcasts.
func fetchJobsAndHooks(socketPath, authToken string) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		jobs, err := client.ListJobs()
		if err != nil {
			return jobsListMsg{}
		}
		hooks, err := client.ListHooks()
		if err != nil {
			return jobsListMsg{jobs: jobs}
		}
		return jobsListMsg{jobs: jobs, hooks: hooks}
	}
}

// setJobEnabled toggles a job's enabled flag, then refreshes the list.
func setJobEnabled(socketPath, authToken, id string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.SetJobEnabled(id, enabled)
		return fetchJobsAndHooks(socketPath, authToken)()
	}
}

// setHookEnabled toggles a hook's enabled flag, then refreshes the list.
func setHookEnabled(socketPath, authToken, id string, enabled bool) tea.Cmd {
	return func() tea.Msg {
		client := daemon.NewClient(socketPath)
		client.SetAuthToken(authToken)
		client.SetHookEnabled(id, enabled)
		return fetchJobsAndHooks(socketPath, authToken)()
	}
}

// jobDoneStatusText renders the transient status-bar line for a finished job.
func jobDoneStatusText(ev protocol.EventJobDone) (string, StatusMsgKind) {
	name := ev.Name
	if name == "" {
		name = ev.JobID
	}
	switch ev.Status {
	case "ok":
		return fmt.Sprintf("Job %s finished", name), StatusMsgInfo
	default:
		text := fmt.Sprintf("Job %s failed (%s)", name, ev.Status)
		if ev.Error != "" {
			text += ": " + ev.Error
		}
		return text, StatusMsgWarning
	}
}
