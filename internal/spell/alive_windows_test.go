//go:build windows

package spell

// processAlive is unused on Windows: the test that needs it is skipped there.
func processAlive(int) bool { return false }
