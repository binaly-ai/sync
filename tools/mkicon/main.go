// mkicon builds the icons from the Binaly icon PNGs (G:/My Drive/Projects/Lab/Assets):
//
//   - tray icons: a plain one, one with an ochre dot (needs attention) and one with a grey dot
//     (paused), each as a Windows .ico holding 16, 24, 32 and 48 pixel images. The light-theme
//     artwork (navy) is for a light taskbar; the dark-theme artwork for a dark one, the
//     Windows 11 default, where navy would all but disappear.
//   - app.ico, the program's own icon (Explorer, Start menu, Installed apps), up to 256 pixels.
//
// Images are PNG-compressed inside the .ico, which Windows has read since Vista.
//
//	go run ./tools/mkicon <binaly-icon-512.png> <binaly-icon-512-dark-theme.png> cmd/binaly-sync/icons
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
	paper = color.NRGBA{0xF4, 0xF2, 0xEB, 0xFF} // paper-50, the dot's outline on a light taskbar
	night = color.NRGBA{0x20, 0x20, 0x20, 0xFF} // the dark taskbar, the dot's outline there
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
func dot(img *image.NRGBA, c, ring color.NRGBA) {
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
				img.SetNRGBA(x, y, ring)
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

func load(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		panic(err)
	}
	return img
}

func main() {
	if len(os.Args) != 4 {
		fmt.Println("usage: mkicon <icon.png> <icon-dark-theme.png> <outdir>")
		os.Exit(2)
	}
	light, dark, out := load(os.Args[1]), load(os.Args[2]), os.Args[3]
	os.MkdirAll(out, 0o755)
	sizes := []int{16, 24, 32, 48}
	themes := []struct {
		suffix string
		src    image.Image
		ring   color.NRGBA
	}{{"", light, paper}, {"-dark", dark, night}}
	for _, th := range themes {
		for _, v := range []struct {
			name string
			c    *color.NRGBA
		}{{"binaly", nil}, {"attention", &ochre}, {"paused", &grey}} {
			var imgs []*image.NRGBA
			for _, s := range sizes {
				im := scale(th.src, s)
				if v.c != nil {
					dot(im, *v.c, th.ring)
				}
				imgs = append(imgs, im)
			}
			name := v.name + th.suffix + ".ico"
			if err := writeICO(filepath.Join(out, name), imgs); err != nil {
				panic(err)
			}
			fmt.Println("wrote", name)
		}
	}
	// The program's icon: the brand (light-theme) artwork, like the website and the app.
	var app []*image.NRGBA
	for _, s := range []int{16, 20, 24, 32, 40, 48, 64, 256} {
		app = append(app, scale(light, s))
	}
	if err := writeICO(filepath.Join(out, "app.ico"), app); err != nil {
		panic(err)
	}
	fmt.Println("wrote app.ico")
	// A 64px PNG for the local pages' favicon.
	var buf bytes.Buffer
	png.Encode(&buf, scale(light, 64))
	os.WriteFile(filepath.Join(out, "binaly-64.png"), buf.Bytes(), 0o644)
}
