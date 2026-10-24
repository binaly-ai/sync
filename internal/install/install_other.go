//go:build !windows

// Package install is Windows-only for now; elsewhere the binary runs from wherever it is.
package install

import "os"

func Dir() string                        { return "" }
func Target() string                     { exe, _ := os.Executable(); return exe }
func IsInstalled() bool                  { return true }
func Install() (string, error)           { return Target(), nil }
func Swap(string) error                  { return nil }
func Cleanup()                           {}
func Register(string) error              { return nil }
func Unregister() error                  { return nil }
func SetStartAtLogon(bool, string) error { return nil }
