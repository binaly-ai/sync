//go:build !windows

package onenote

import "context"

// Exporter is unavailable off Windows.
type Exporter struct{}

// Available reports whether OneNote can export sections here: never, off Windows.
func Available() bool { return false }

// New returns an exporter that always fails.
func New(workDir string) *Exporter { return &Exporter{} }

// Export always fails off Windows.
func (x *Exporter) Export(ctx context.Context, section, pdf string) error { return ErrUnavailable }

// Close does nothing.
func (x *Exporter) Close() {}

func refused(err error) bool { return false }
