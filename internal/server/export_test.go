package server

// SetPerformanceEventCap lowers the event cap for a test and returns the restore.
func SetPerformanceEventCap(n int) func() {
	old := performanceEventCap
	performanceEventCap = n
	return func() { performanceEventCap = old }
}
