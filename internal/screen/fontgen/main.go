// Generator for internal/screen/glyphs.png. Stdlib only. Run with
//   go run ./internal/screen/fontgen
// from the repo root. The output PNG is checked in; CI does not run this.
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"os"
)

// 7×12 source glyph cell. Each byte is one row, LSB = leftmost pixel.
// Cells are centred within the cell (col 0 and col 6 blank, rows 0/10/11
// blank). Order matches atlasOrder below.
var glyphs = map[rune][12]byte{
	'0': {0x00, 0x1C, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x22, 0x1C, 0x00, 0x00},
	'1': {0x00, 0x08, 0x0C, 0x08, 0x08, 0x08, 0x08, 0x08, 0x08, 0x1C, 0x00, 0x00},
	'2': {0x00, 0x1C, 0x22, 0x20, 0x10, 0x08, 0x04, 0x02, 0x22, 0x3E, 0x00, 0x00},
	'3': {0x00, 0x1C, 0x22, 0x20, 0x10, 0x18, 0x10, 0x20, 0x22, 0x1C, 0x00, 0x00},
	'4': {0x00, 0x10, 0x18, 0x14, 0x12, 0x3E, 0x10, 0x10, 0x10, 0x10, 0x00, 0x00},
	'5': {0x00, 0x3E, 0x02, 0x02, 0x1E, 0x20, 0x20, 0x20, 0x22, 0x1C, 0x00, 0x00},
	'6': {0x00, 0x1C, 0x22, 0x02, 0x1E, 0x22, 0x22, 0x22, 0x22, 0x1C, 0x00, 0x00},
	'7': {0x00, 0x3E, 0x20, 0x10, 0x08, 0x08, 0x04, 0x04, 0x04, 0x04, 0x00, 0x00},
	'8': {0x00, 0x1C, 0x22, 0x22, 0x1C, 0x22, 0x22, 0x22, 0x22, 0x1C, 0x00, 0x00},
	'9': {0x00, 0x1C, 0x22, 0x22, 0x22, 0x3C, 0x20, 0x20, 0x22, 0x1C, 0x00, 0x00},
	':': {0x00, 0x00, 0x00, 0x08, 0x08, 0x00, 0x00, 0x08, 0x08, 0x00, 0x00, 0x00},
}

// Atlas layout: 11 glyphs, each 14×24 (source 7×12 scaled 2×), white on
// transparent. Matches the order screen/badge.go expects.
var atlasOrder = []rune{'0', '1', '2', '3', '4', '5', '6', '7', '8', '9', ':'}

const (
	cellW   = 14 // 7 source cols × 2
	cellH   = 24 // 12 source rows × 2
	atlasW  = cellW * 11
	atlasH  = cellH
	srcRows = 12
	srcCols = 7
)

func main() {
	img := image.NewNRGBA(image.Rect(0, 0, atlasW, atlasH))
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for i, r := range atlasOrder {
		bm, ok := glyphs[r]
		if !ok {
			log.Fatalf("missing glyph for %q", r)
		}
		baseX := i * cellW
		for row := 0; row < srcRows; row++ {
			bits := bm[row]
			for col := 0; col < srcCols; col++ {
				if bits&(1<<col) == 0 {
					continue
				}
				// 2× scale: write a 2×2 block.
				px := baseX + col*2
				py := row * 2
				img.SetNRGBA(px, py, white)
				img.SetNRGBA(px+1, py, white)
				img.SetNRGBA(px, py+1, white)
				img.SetNRGBA(px+1, py+1, white)
			}
		}
	}
	f, err := os.Create("internal/screen/glyphs.png")
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		log.Fatalf("encode: %v", err)
	}
	log.Printf("wrote internal/screen/glyphs.png (%dx%d, %d glyphs)", atlasW, atlasH, len(atlasOrder))
}
