package ui

import (
	"bytes"
	_ "embed"
	"image"
	_ "image/png"

	"gioui.org/op/paint"
)

//go:embed logo.png
var logoPNG []byte

var logoOp paint.ImageOp
var logoSize image.Point
var logoInited bool

func initLogo() {
	if logoInited {
		return
	}
	logoInited = true
	img, _, err := image.Decode(bytes.NewReader(logoPNG))
	if err != nil {
		return
	}
	logoOp = paint.NewImageOp(img)
	logoSize = img.Bounds().Size()
}
