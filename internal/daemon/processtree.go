package daemon

// ProcessTree abstracts platform-specific process-tree teardown.
//
// Today the daemon isolates every spawned shell/hook/MCP child in its own
// process group (Unix: Setpgid) and tears the whole tree down by signalling
// the group (Unix: kill -pgid). That mechanism is POSIX-only. This interface
// formalizes the seam so a later wave can slot in a Windows Job-Object backend
// without touching call sites.
//
// Wave 0 ships only the Unix implementation plus a Windows compile stub; it
// adds no Windows backend and changes no Unix behaviour.
type ProcessTree interface {
	// KillTree kills the process identified by pid together with any
	// descendants it has spawned. On Unix, pid is interpreted as a process-group
	// id (negative semantics live inside the Unix impl).
	KillTree(pid int) error
}

// processTree is the ProcessTree used by all call sites. It is bound to the
// platform-appropriate implementation by the per-OS processtree_*.go file.
var processTree ProcessTree

// killProcessTree kills the process tree rooted at pid via the active
// ProcessTree implementation.
func killProcessTree(pid int) error {
	if processTree == nil {
		return nil
	}
	return processTree.KillTree(pid)
}
