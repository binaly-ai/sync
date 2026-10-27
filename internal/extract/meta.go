package extract

import (
	"path/filepath"
	"strings"

	"github.com/binaly-ai/sync/internal/api"
)

// HasMeta reports whether ReadMeta reads anything from files with this name.
func HasMeta(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".docx", ".docm", ".pptx", ".pptm", ".xlsx", ".xlsm", ".jpg", ".jpeg", ".tif", ".tiff":
		return true
	}
	return false
}

// ReadMeta reads a file's embedded metadata, recovering from a parser that panics on a damaged
// file. A nil result with no error means the file says nothing about itself.
func ReadMeta(path string) (m *api.FileMeta, err error) {
	defer func() {
		if p := recover(); p != nil {
			m, err = nil, nil
		}
	}()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		m, err = pdfMeta(path)
	case ".docx", ".docm", ".pptx", ".pptm", ".xlsx", ".xlsm":
		m, err = officeMeta(path)
	case ".jpg", ".jpeg", ".tif", ".tiff":
		var captured string
		if captured, err = exifCaptured(path); captured != "" {
			m = &api.FileMeta{Captured: captured}
		}
	}
	if m.Empty() {
		m = nil
	}
	return m, err
}
