package extract

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/binaly-ai/sync/internal/api"
)

// maxTextBytes: plain-text files larger than this are data (instrument logs, exports), stored
// by the server but not sent as text.
const maxTextBytes = 4 << 20

// textPages reads a plain-text file, split into pages of at most MaxPageChars. Text that isn't
// UTF-8 is read as Windows-1252, the usual encoding of older lab software's output.
func textPages(p string) ([]api.ExtractPage, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.Size() > maxTextBytes {
		return nil, fmt.Errorf("over %d MB of text, not sent", maxTextBytes>>20)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
	if err != nil {
		return nil, err
	}
	s := string(raw)
	if !utf8.ValidString(s) {
		s = latin1(raw)
	}
	s = strings.TrimPrefix(s, string(rune(0xFEFF)))
	var pages []api.ExtractPage
	for s != "" {
		chunk := cut(s, MaxPageChars)
		if len(chunk) < len(s) {
			if i := strings.LastIndexByte(chunk, '\n'); i > MaxPageChars/2 {
				chunk = chunk[:i+1]
			}
		}
		pages = append(pages, api.ExtractPage{Text: chunk})
		s = s[len(chunk):]
	}
	if len(pages) == 0 {
		pages = []api.ExtractPage{{}}
	}
	return pages, nil
}

// cp1252 maps the bytes 0x80-0x9F, where Windows-1252 differs from Latin-1.
var cp1252 = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

func latin1(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c >= 0x80 && c < 0xA0 {
			sb.WriteRune(cp1252[c-0x80])
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}
