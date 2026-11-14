package app

import (
	"github.com/gdamore/tcell/v2"
)

// shadowCell repaints one strip cell so it reads as cast shadow: the
// background scales toward black and the glyph keeps its rune, dimmed,
// so content under the strip recedes instead of vanishing. A near-black
// background would stay black under scaling, so it gets a dark gray
// floor instead; that floor is what makes the strip visible on the
// default dark packs. Cells painted with the terminal's own default
// background (follow-terminal mode) carry no color to darken, so those
// keep only the dim attribute.
func shadowCell(screen tcell.Screen, x, y int) {
	sw, sh := screen.Size()
	if x < 0 || y < 0 || x >= sw || y >= sh {
		return
	}
	mainRune, combining, style, _ := screen.GetContent(x, y)
	style = style.Dim(true)
	_, bg, _ := style.Decompose()
	if bg != tcell.ColorDefault {
		style = style.Background(shadowBackgroundColor(bg))
	}
	screen.SetContent(x, y, mainRune, combining, style)
}

// shadowBackgroundColor scales a background color to 45% brightness, the
// level a cast shadow sits at relative to its surface. Colors whose scaled
// value would still read as black get a fixed dark gray floor instead:
// scaling pure black yields pure black, which reads as no shadow at all,
// and no darker color than black exists, so near-black surfaces get the
// floor as the deepest step the terminal can show. Every mid-dark surface
// keeps its own scaled color, which is always a visible darker step.
func shadowBackgroundColor(bg tcell.Color) tcell.Color {
	r, g, b := bg.TrueColor().RGB()
	r, g, b = r*45/100, g*45/100, b*45/100
	if r+g+b < shadowFloorThreshold*3 {
		return shadowFloorColor
	}
	return tcell.NewRGBColor(r, g, b)
}

// shadowFloorColor is the fixed dark gray near-black surfaces fall back to
// when their scaled value would read as no shadow. It sits above pure
// black by enough to read as a band, and only ever applies to surfaces
// whose own scaled color would be indistinguishable from black.
var shadowFloorColor = tcell.NewRGBColor(48, 48, 48)

// shadowFloorThreshold is the per-channel scaled brightness under which a
// surface takes the floor: below it, the scaled color is close enough to
// black that the strip would not read as a step.
const shadowFloorThreshold = 12
