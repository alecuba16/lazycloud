package app

import (
	"strings"
	"sync"
	"time"

	"github.com/rivo/tview"
)

// panelLoadingFrames cycles in the view panel's body while a fetch runs.
// Braille dots read as motion at one frame per tick and cost a single cell,
// the same language the status-line spinner speaks.
var panelLoadingFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// panelLoadingTick is how often the loading row advances a frame. Slow
// enough to feel calm, fast enough to read as alive.
const panelLoadingTick = 200 * time.Millisecond

// panelLoading drives the loading row inside the view panel. The status
// line already reports fetches, but the panel itself goes blank for the
// whole fetch: this paints an animated row under the headers so the panel
// reads as busy instead of empty.
//
// The state has two halves. The counter half (frame) only moves on the
// ticker goroutine while loading is on, so it needs no lock of its own:
// every read and write happens either on that goroutine or after the
// stop channel closed. The message half is written from fetch goroutines
// through queueUpdateDraw, which the ui mutex already serializes.
type panelLoading struct {
	frame int
	msg   string
	stop  chan struct{}
	once  sync.Once
}

// loadingRow renders the animated cell + message text.
func (l *panelLoading) loadingRow() string {
	if l.frame >= len(panelLoadingFrames) {
		l.frame = 0
	}
	return panelLoadingFrames[l.frame] + " " + l.msg
}

// startLoading turns the loading row on. The panel repaints with the row
// right away and a ticker goroutine advances the animation until stop.
func (a *app) startLoading(msg string) {
	if a.info == nil {
		return
	}
	if a.loading == nil {
		a.loading = &panelLoading{}
	}
	l := a.loading
	l.msg = msg
	if l.stop != nil {
		// Already loading: the running ticker picks the new message up.
		a.paintLoadingRow()
		return
	}
	l.stop = make(chan struct{})
	l.once = sync.Once{}
	a.paintLoadingRow()
	// Deliberately untracked: the goroutine exits through stop and only
	// touches the panel through queueUpdateDraw, the same lifetime the
	// progress ticker already has. Tracked fetch goroutines are the ones
	// tests must drain.
	go func() {
		ticker := time.NewTicker(panelLoadingTick)
		defer ticker.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-ticker.C:
				a.queueUpdateDraw(func() {
					l.frame = (l.frame + 1) % len(panelLoadingFrames)
					a.paintLoadingRow()
				})
			}
		}
	}()
}

// stopLoading turns the loading row off and repaints the panel with the
// rows the fetch produced. Safe to call when no load is running, and it
// must leave populated panels untouched: the row it removes has to be the
// animated one, never a data row.
func (a *app) stopLoading() {
	if a.info == nil || a.loading == nil {
		return
	}
	l := a.loading
	if l.stop != nil {
		l.once.Do(func() { close(l.stop) })
		l.stop = nil
	}
	if loadingRowPainted(a.info) {
		a.info.RemoveRow(1)
	}
}

// loadingRowPainted reports whether row 1 holds the panel's animated
// loading cell, so nothing mistakes a data row for the animation.
func loadingRowPainted(table *tview.Table) bool {
	if table.GetRowCount() < 2 {
		return false
	}
	cell := table.GetCell(1, 0)
	if cell == nil {
		return false
	}
	runes := []rune(cell.Text)
	return len(runes) > 0 && isSpinnerRune(runes[0])
}

// paintLoadingRow puts the loading row under the headers, replacing the
// previous frame's cells. The row is not selectable and not expanded, so
// focus stays on the headers' fixed row and the animation never paints
// over real rows. When the panel carries data rows already, the load is
// a refresh of visible content and the rows stay on screen instead.
func (a *app) paintLoadingRow() {
	l := a.loading
	if l == nil || len(a.visibleRows) > 0 {
		return
	}
	row := l.loadingRow()
	cell := a.info.GetCell(1, 0)
	if cell != nil && cell.Text == row {
		return // ticker fired twice inside one frame
	}
	a.info.SetCell(1, 0, tview.NewTableCell(row).
		SetTextColor(theme.statusTextColor()).
		SetSelectable(false))
}

// loadingMessage keeps the panel row's message in step with the status
// line: strip the leading spinner and step counter so the panel row stays
// short, and drop color tags the table cannot render.
func loadingMessage(status string) string {
	msg := status
	// Drop a leading spinner frame followed by its space.
	if runes := []rune(msg); len(runes) > 1 && isSpinnerRune(runes[0]) && runes[1] == ' ' {
		msg = string(runes[2:])
	}
	// Drop a trailing step counter like (3/12).
	if i := strings.LastIndex(msg, " ("); i != -1 && strings.HasSuffix(msg, ")") {
		if strings.Trim(msg[i+2:len(msg)-1], "0123456789/") == "" {
			msg = msg[:i]
		}
	}
	return msg
}

// isSpinnerRune reports whether r is one of the animation frames. The
// frames are multi-byte braille runes, so callers must decode runes, not
// index bytes.
func isSpinnerRune(r rune) bool {
	for _, f := range panelLoadingFrames {
		if f == string(r) {
			return true
		}
	}
	return false
}
