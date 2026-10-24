//go:build !windows

package main

import "context"

func signalShow() bool                   { return false }
func listenShow(context.Context, func()) {}
