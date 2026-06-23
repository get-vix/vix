//go:build windows

package daemon

// collectVitals is a Wave 0 stub on Windows. A later wave will implement it via
// GetSystemTimes / pdh.dll so the web UI's vitals panel works natively. The
// daemon does not yet run on Windows, so returning the zero value keeps the
// package compiling without adding a backend.
func collectVitals() ServerVitals {
	return ServerVitals{}
}
