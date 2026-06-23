//go:build !linux && !darwin && !windows

package daemon

func collectVitals() ServerVitals {
	return ServerVitals{}
}
