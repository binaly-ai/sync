//go:build !windows

package scan

// CloudDrive is Windows-only for now.
func CloudDrive(string) string { return "" }
