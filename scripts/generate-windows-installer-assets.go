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
	canvas = color.RGBA{R: 244, G: 247, B: 249, A: 255}
	ink    = color.RGBA{R: 29, G: 42, B: 51, A: 255}
	blue   = color.RGBA{R: 36, G: 116, B: 181, A: 255}
	teal   = color.RGBA{R: 42, G: 157, B: 143, A: 255}
	white  = color.RGBA{R: 255, G: 255, B: 255, A: 255}
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: generate-windows-installer-assets OUTPUT_DIR")
		os.Exit(2)
	}
	outDir := os.Args[1]
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fatal(err)
	}
	if err := writeICO(filepath.Join(outDir, "ace-agent.ico")); err != nil {
		fatal(err)
	}
	if err := writeBMP(filepath.Join(outDir, "wizard-small.bmp"), 55, 58); err != nil {
		fatal(err)
	}
	if err := writeBMP(filepath.Join(outDir, "wizard-large.bmp"), 164, 314); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "generate installer assets: %v\n", err)
	os.Exit(1)
}

func writeICO(path string) error {
	sizes := []int{16, 32, 48, 64, 256}
	images := make([][]byte, len(sizes))
	for index, size := range sizes {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, renderMark(size, size, true)); err != nil {
			return err
		}
		images[index] = encoded.Bytes()
	}

	var output bytes.Buffer
	_ = binary.Write(&output, binary.LittleEndian, uint16(0))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint16(len(images)))
	offset := 6 + len(images)*16
	for index, size := range sizes {
		width := byte(size)
		if size == 256 {
			width = 0
		}
		output.WriteByte(width)
		output.WriteByte(width)
		output.WriteByte(0)
		output.WriteByte(0)
		_ = binary.Write(&output, binary.LittleEndian, uint16(1))
		_ = binary.Write(&output, binary.LittleEndian, uint16(32))
		_ = binary.Write(&output, binary.LittleEndian, uint32(len(images[index])))
		_ = binary.Write(&output, binary.LittleEndian, uint32(offset))
		offset += len(images[index])
	}
	for _, encoded := range images {
		output.Write(encoded)
	}
	return os.WriteFile(path, output.Bytes(), 0o644)
}

func writeBMP(path string, width, height int) error {
	rowSize := (width*3 + 3) &^ 3
	pixelSize := rowSize * height
	fileSize := 14 + 40 + pixelSize
	data := make([]byte, fileSize)
	copy(data[0:2], "BM")
	binary.LittleEndian.PutUint32(data[2:6], uint32(fileSize))
	binary.LittleEndian.PutUint32(data[10:14], 54)
	binary.LittleEndian.PutUint32(data[14:18], 40)
	binary.LittleEndian.PutUint32(data[18:22], uint32(width))
	binary.LittleEndian.PutUint32(data[22:26], uint32(height))
	binary.LittleEndian.PutUint16(data[26:28], 1)
	binary.LittleEndian.PutUint16(data[28:30], 24)
	binary.LittleEndian.PutUint32(data[34:38], uint32(pixelSize))
	binary.LittleEndian.PutUint32(data[38:42], 3780)
	binary.LittleEndian.PutUint32(data[42:46], 3780)

	mark := renderMark(width, height, false)
	for y := 0; y < height; y++ {
		row := 54 + (height-1-y)*rowSize
		for x := 0; x < width; x++ {
			pixel := color.RGBAModel.Convert(mark.At(x, y)).(color.RGBA)
			offset := row + x*3
			data[offset] = pixel.B
			data[offset+1] = pixel.G
			data[offset+2] = pixel.R
		}
	}
	return os.WriteFile(path, data, 0o644)
}

func renderMark(width, height int, transparent bool) *image.RGBA {
	background := canvas
	if transparent {
		background = color.RGBA{}
	}
	result := image.NewRGBA(image.Rect(0, 0, width, height))
	fill(result, result.Bounds(), background)

	side := min(width*4/5, height*4/5)
	if !transparent && height > width*2 {
		side = width * 3 / 5
	}
	left := (width - side) / 2
	top := (height - side) / 2
	if !transparent && height > width*2 {
		top = height / 7
	}
	fill(result, image.Rect(left, top, left+side, top+side), ink)

	unit := max(1, side/8)
	fill(result, image.Rect(left+unit, top+unit, left+3*unit, top+side-unit), blue)
	fill(result, image.Rect(left+3*unit, top+unit, left+side-unit, top+3*unit), blue)
	fill(result, image.Rect(left+3*unit, top+3*unit, left+side-unit, top+5*unit), white)
	fill(result, image.Rect(left+3*unit, top+5*unit, left+5*unit, top+side-unit), teal)
	fill(result, image.Rect(left+5*unit, top+5*unit, left+side-unit, top+side-unit), blue)
	return result
}

func fill(target *image.RGBA, rectangle image.Rectangle, value color.RGBA) {
	rectangle = rectangle.Intersect(target.Bounds())
	for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
		for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
			target.SetRGBA(x, y, value)
		}
	}
}
