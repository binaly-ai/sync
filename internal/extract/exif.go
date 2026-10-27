package extract

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// exifCaptured reads when a photo was taken (EXIF DateTimeOriginal) from a JPEG or TIFF: for a
// microscope or camera image, when the measurement happened. It reads only the file's first
// 128 KB, where the EXIF block lives.
func exifCaptured(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 128<<10)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", err
	}
	head = head[:n]
	tiff := head
	if len(head) > 2 && head[0] == 0xFF && head[1] == 0xD8 { // JPEG: find the APP1 "Exif" segment
		tiff = nil
		for i := 2; i+4 <= len(head); {
			if head[i] != 0xFF {
				break
			}
			marker := head[i+1]
			size := int(binary.BigEndian.Uint16(head[i+2:]))
			if marker == 0xE1 && i+4+6 <= len(head) && string(head[i+4:i+10]) == "Exif\x00\x00" {
				end := i + 2 + size
				if end > len(head) {
					end = len(head)
				}
				tiff = head[i+10 : end]
				break
			}
			if marker == 0xDA { // start of image data: no EXIF before it
				break
			}
			i += 2 + size
		}
	}
	if tiff == nil {
		return "", nil
	}
	return tiffDateTimeOriginal(tiff)
}

var errBadTIFF = errors.New("not a readable TIFF header")

// tiffDateTimeOriginal walks IFD0 to the EXIF IFD and reads tag 0x9003, falling back to IFD0's
// DateTime (0x0132).
func tiffDateTimeOriginal(b []byte) (string, error) {
	if len(b) < 8 {
		return "", errBadTIFF
	}
	var bo binary.ByteOrder
	switch string(b[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return "", errBadTIFF
	}
	ascii := func(count, off uint32) string {
		if count == 0 || int(off)+int(count) > len(b) {
			return ""
		}
		return strings.TrimRight(string(b[off:off+count]), "\x00 ")
	}
	// ifd returns, per tag, the entry's count and value-or-offset.
	ifd := func(off uint32) map[uint16][2]uint32 {
		out := map[uint16][2]uint32{}
		if int(off)+2 > len(b) {
			return out
		}
		n := int(bo.Uint16(b[off:]))
		for i := 0; i < n; i++ {
			e := int(off) + 2 + 12*i
			if e+12 > len(b) {
				break
			}
			out[bo.Uint16(b[e:])] = [2]uint32{bo.Uint32(b[e+4:]), bo.Uint32(b[e+8:])}
		}
		return out
	}
	ifd0 := ifd(bo.Uint32(b[4:]))
	var raw string
	if p, ok := ifd0[0x8769]; ok {
		if d, ok := ifd(p[1])[0x9003]; ok {
			raw = ascii(d[0], d[1])
		}
	}
	if raw == "" {
		if d, ok := ifd0[0x0132]; ok {
			raw = ascii(d[0], d[1])
		}
	}
	if raw == "" {
		return "", nil
	}
	// EXIF times have no zone; they are the camera's local time, reported as written.
	t, err := time.Parse("2006:01:02 15:04:05", raw)
	if err != nil {
		return "", nil
	}
	return t.Format("2006-01-02T15:04:05"), nil
}
