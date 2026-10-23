//go:build !darwin && !linux

package browser

// Without a portable signal-0 check, retain the profile rather than risk
// deleting one that an active browser is still using.
func processAlive(pid int) bool {
	return pid > 0
}
