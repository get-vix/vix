package daemon

import (
	"github.com/get-vix/vix/internal/daemon/hooks"
	"github.com/get-vix/vix/internal/daemon/jobs"
)

// ServerVitals holds a snapshot of host and daemon resource usage.
type ServerVitals struct {
	CPUPercent   float64 `json:"cpu_percent"`
	CPUAvailable bool    `json:"cpu_available"`
	RAMUsed      uint64  `json:"ram_used"`
	RAMTotal     uint64  `json:"ram_total"`
	DiskUsed     uint64  `json:"disk_used"`
	DiskTotal    uint64  `json:"disk_total"`
}

// wsMessage is the envelope sent over the WebSocket connection.
type wsMessage struct {
	Threads    []ThreadInfo         `json:"threads"`
	Vitals     ServerVitals         `json:"vitals"`
	Jobs       []jobs.JobSnapshot   `json:"jobs"`
	Hooks      []hooks.HookSnapshot `json:"hooks"`
	DefaultCWD string               `json:"default_cwd"`
	Version    string               `json:"version"`
}
