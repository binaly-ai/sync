// mkicon builds the tray icons from the Binaly icon PNG: a plain one, one with an ochre dot
// (needs attention) and one with a grey dot (paused), each as a Windows .ico holding 16, 24,
// 32 and 48 pixel images (PNG-compressed, which Windows has read since Vista).
//
//	go run ./tools/mkicon <binaly-icon-512.png> cmd/binaly-sync/icons
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

var (
	ochre = color.NRGBA{0xC1, 0x8B, 0x2F, 0xFF} // ochre-500
	grey  = color.NRGBA{0x9D, 0x90, 0x72, 0xFF} // paper-500
	paper = color.NRGBA{0xF4, 0xF2, 0xEB, 0xFF} // paper-50, the dot's outline
)

// scale is an area-average downscale: good enough for an icon, no dependency.
func scale(src image.Image, size int) *image.NRGBA {
	b := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, size, size))
	sx := float64(b.Dx()) / float64(size)
	sy := float64(b.Dy()) / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var r, g, bl, a, n float64
			for yy := int(float64(y) * sy); yy < int(float64(y+1)*sy); yy++ {
				for xx := int(float64(x) * sx); xx < int(float64(x+1)*sx); xx++ {
					c := color.NRGBAModel.Convert(src.At(b.Min.X+xx, b.Min.Y+yy)).(color.NRGBA)
					w := float64(c.A) / 255
					r += float64(c.R) * w
					g += float64(c.G) * w
					bl += float64(c.B) * w
					a += float64(c.A)
					n++
				}
			}
			if n == 0 || a == 0 {
				continue
			}
			wsum := a / 255
			dst.SetNRGBA(x, y, color.NRGBA{uint8(r / wsum), uint8(g / wsum), uint8(bl / wsum), uint8(a / n)})
		}
	}
	return dst
}

// dot draws a status dot in the bottom-right corner, with a paper-coloured ring.
func dot(img *image.NRGBA, c color.NRGBA) {
	s := img.Bounds().Dx()
	rad := float64(s) * 0.22
	cx, cy := float64(s)-rad-0.5, float64(s)-rad-0.5
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			d2 := dx*dx + dy*dy
			switch {
			case d2 <= (rad-1.2)*(rad-1.2):
				img.SetNRGBA(x, y, c)
			case d2 <= rad*rad:
				img.SetNRGBA(x, y, paper)
			}
		}
	}
}

func writeICO(path string, imgs []*image.NRGBA) error {
	var pngs [][]byte
	for _, im := range imgs {
		var buf bytes.Buffer
		if err := png.Encode(&buf, im); err != nil {
			return err
		}
		pngs = append(pngs, buf.Bytes())
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, [3]uint16{0, 1, uint16(len(imgs))})
	offset := 6 + 16*len(imgs)
	for i, im := range imgs {
		s := im.Bounds().Dx()
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		binary.Write(&out, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(pngs[i])), uint32(offset)})
		offset += len(pngs[i])
	}
	for _, p := range pngs {
		out.Write(p)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func main() {
	if len(os.Args) != 3 {
		fmt.Println("usage: mkicon <icon.png> <outdir>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		panic(err)
	}
	sizes := []int{16, 24, 32, 48}
	variants := map[string]*color.NRGBA{"binaly.ico": nil, "attention.ico": &ochre, "paused.ico": &grey}
	os.MkdirAll(os.Args[2], 0o755)
	for name, c := range variants {
		var imgs []*image.NRGBA
		for _, s := range sizes {
			im := scale(src, s)
			if c != nil {
				dot(im, *c)
			}
			imgs = append(imgs, im)
		}
		if err := writeICO(filepath.Join(os.Args[2], name), imgs); err != nil {
			panic(err)
		}
		fmt.Println("wrote", name)
	}
	// A 64px PNG for the local pages' favicon.
	var buf bytes.Buffer
	png.Encode(&buf, scale(src, 64))
	os.WriteFile(filepath.Join(os.Args[2], "binaly-64.png"), buf.Bytes(), 0o644)
}
