package app

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Dialog runes follow the classic text-mode window: dialogs draw the
// double-line set, every other window the single-line set, like Turbo
// Vision separated modal dialogs from background panels.
var (
	dialogCorners      = [4]rune{'╔', '╗', '╚', '╝'} // top-left, top-right, bottom-left, bottom-right
	windowCorners     = [4]rune{'┌', '┐', '└', '┘'}
	dialogHorizontal  = '═'
	dialogVertical    = '║'
	windowHorizontal = '─'
	windowVertical   = '│'
)

// framedWindow wraps the body of an overlay and paints it as a classic
// text-mode window: a rectangular border with the title sitting inside the
// top border line, a one-cell drop shadow offset down and right, and the
// frame drawn over whatever the page below already painted, so the window
// reads as a layer floating above the main view instead of a flat region
// of it. Focus and input stay the wrapped body's, exactly like the shadow
// wrapper it replaces for dialogs.
type framedWindow struct {
	*tview.Box
	body      tview.Primitive
	title     string
	titleFg   tcell.Color
	dialog    bool // double-line frame, for modal dialogs
}

// newDialogWindow wraps an overlay body as a modal dialog: double-line
// border, title in the top border, drop shadow.
func newDialogWindow(title string, body tview.Primitive) *framedWindow {
	return &framedWindow{Box: tview.NewBox(), body: body, title: title, titleFg: tcell.ColorDefault, dialog: true}
}

// newPanelWindow wraps an overlay body as a background-style window with
// the single-line frame. Same shadow, same layering.
func newPanelWindow(title string, body tview.Primitive) *framedWindow {
	return &framedWindow{Box: tview.NewBox(), body: body, title: title, titleFg: tcell.ColorDefault}
}

// SetTitleColor sets the color the title paints with, so a pack switch can
// restyle open windows the same way it restyles every other chrome.
func (w *framedWindow) SetTitleColor(color tcell.Color) *framedWindow {
	w.titleFg = color
	return w
}

// SetBorderColor and SetBackgroundColor shadow the Box setters so method
// chains keep the framedWindow type: Box's own setters return *Box, and a
// chained call would leave the app holding a plain Box, whose Draw paints
// nothing and whose input delegates to itself instead of the body.
func (w *framedWindow) SetBorderColor(color tcell.Color) *framedWindow {
	w.Box.SetBorderColor(color)
	return w
}

func (w *framedWindow) SetBackgroundColor(color tcell.Color) *framedWindow {
	w.Box.SetBackgroundColor(color)
	return w
}

// SetTitleText replaces the painted title text without rebuilding the
// window, so callers that update a title live (hit counts) keep one window.
func (w *framedWindow) SetTitleText(title string) *framedWindow {
	w.title = title
	return w
}

// frameRect is the body area inside the wrapper: one cell of shadow margin
// on the right and bottom edges keeps the strip on screen, and one cell of
// breathing space on the top and left keeps the window from touching the
// screen edge, which is where the depth illusion lives.
func (w *framedWindow) frameRect() (int, int, int, int) {
	x, y, width, height := w.GetInnerRect()
	// The wrapper's inner rect is the full window box; the frame occupies
	// its border ring, the body the rest.
	return x + 1, y + 1, width - 2, height - 2
}

// Draw paints the frame, then the body on top of the frame's inner area,
// then the shadow strip around the window's right and bottom edges. Order
// matters: the body must cover the frame's interior, and the shadow must
// darken the page underneath, not the window.
func (w *framedWindow) Draw(screen tcell.Screen) {
	w.Box.DrawForSubclass(screen, w)
	x, y, width, height := w.GetRect()
	if width < 3 || height < 3 {
		// Degenerate rect: no room for a frame ring, just draw the body.
		w.body.SetRect(x, y, width, height)
		w.body.Draw(screen)
		return
	}

	horizontal, vertical := windowHorizontal, windowVertical
	corners := windowCorners
	if w.dialog {
		horizontal, vertical = dialogHorizontal, dialogVertical
		corners = dialogCorners
	}

	borderStyle := tcell.StyleDefault.Foreground(w.GetBorderColor()).Background(w.GetBackgroundColor())
	// Top and bottom border rows.
	for col := x + 1; col < x+width-1; col++ {
		screen.SetContent(col, y, horizontal, nil, borderStyle)
		screen.SetContent(col, y+height-1, horizontal, nil, borderStyle)
	}
	// Left and right border columns.
	for row := y + 1; row < y+height-1; row++ {
		screen.SetContent(x, row, vertical, nil, borderStyle)
		screen.SetContent(x+width-1, row, vertical, nil, borderStyle)
	}
	// Corners.
	screen.SetContent(x, y, corners[0], nil, borderStyle)
	screen.SetContent(x+width-1, y, corners[1], nil, borderStyle)
	screen.SetContent(x, y+height-1, corners[2], nil, borderStyle)
	screen.SetContent(x+width-1, y+height-1, corners[3], nil, borderStyle)

	// Title inside the top border, aligned left after the corner. The title
	// reads from the theme when no explicit color was set, so a live theme
	// switch repaints open windows like every other chrome.
	titleFg := w.titleFg
	if titleFg == tcell.ColorDefault {
		titleFg = theme.titleColor()
	}
	titleStyle := tcell.StyleDefault.Foreground(titleFg).Background(w.GetBackgroundColor())
	if w.title != "" {
		tcol := x + 1
		maxWidth := width - 2
		for _, r := range w.title {
			if maxWidth <= 0 {
				break
			}
			screen.SetContent(tcol, y, r, nil, titleStyle)
			tcol++
			maxWidth--
		}
	}

	// Body fills the inside of the frame.
	bx, by, bw, bh := w.frameRect()
	if bw > 0 && bh > 0 {
		w.body.SetRect(bx, by, bw, bh)
		w.body.Draw(screen)
	}

	// Drop shadow: one cell right of the right edge, one row below the
	// bottom edge, drawn after the body so only the page underneath dims.
	screenWidth, screenHeight := screen.Size()
	for row := y + 1; row < y+height; row++ {
		if row < screenHeight {
			shadowCell(screen, x+width, row)
		}
	}
	for col := x + 1; col < x+width; col++ {
		if col < screenWidth {
			shadowCell(screen, col, y+height)
		}
	}
	if x+width < screenWidth && y+height < screenHeight {
		shadowCell(screen, x+width, y+height)
	}
}

func (w *framedWindow) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return w.body.InputHandler()
}

func (w *framedWindow) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return w.body.MouseHandler()
}

func (w *framedWindow) PasteHandler() func(text string, setFocus func(p tview.Primitive)) {
	return w.body.PasteHandler()
}

func (w *framedWindow) Focus(delegate func(p tview.Primitive)) {
	w.body.Focus(delegate)
}

func (w *framedWindow) HasFocus() bool {
	return w.body.HasFocus()
}

func (w *framedWindow) Blur() {
	w.body.Blur()
}
