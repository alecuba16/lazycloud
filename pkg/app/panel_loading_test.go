package app

import (
	"strings"
	"testing"
	"time"

	"github.com/elferherrera/lazycloud/cmd/providers"
	"github.com/rivo/tview"
)

// newLoadingTestApp builds the minimal app the loading row needs: an info
// panel wired through the real populateInfoPanel path.
func newLoadingTestApp(t *testing.T) *app {
	t.Helper()
	cache, err := providers.NewCacheAt(t.TempDir() + "/cache.db")
	if err != nil {
		t.Fatalf("NewCacheAt() error = %v", err)
	}
	t.Cleanup(func() { cache.Close() })
	a := &app{
		tapp:   tview.NewApplication(),
		cmd:    tview.NewInputField(),
		info:   newInfoPanel(),
		status: tview.NewTextView().SetDynamicColors(true),
		pages:  tview.NewPages(),
		cache:  cache,
	}
	a.info.SetBorder(true)
	a.viewHeaders = []string{"Name", "Status"}
	return a
}

// TestStatusProgressPaintsLoadingRow pins the on state: a fetch that
// reports progress must paint the animated row under the headers while the
// panel has no data rows.
func TestStatusProgressPaintsLoadingRow(t *testing.T) {
	a := newLoadingTestApp(t)
	a.applyFilter() // empty panel: headers only

	a.statusProgress("Fetching VMs...")

	cell := a.info.GetCell(1, 0)
	if cell == nil {
		t.Fatal("loading row not painted under the headers")
	}
	if runes := []rune(cell.Text); !isSpinnerRune(runes[0]) {
		t.Fatalf("loading row = %q, want a spinner frame first", cell.Text)
	}
	if !strings.Contains(cell.Text, "Fetching VMs") {
		t.Fatalf("loading row = %q, want the progress message in it", cell.Text)
	}
	if !cell.NotSelectable {
		t.Error("loading row must not be selectable, focus belongs to real rows")
	}
}

// TestLoadingRowKeepsHeaders pins that the headers row survives the
// loading repaint: the animation is a body row, not a panel replacement.
func TestLoadingRowKeepsHeaders(t *testing.T) {
	a := newLoadingTestApp(t)
	a.applyFilter()

	a.statusProgress("Fetching VMs...")

	header := a.info.GetCell(0, 0)
	if header == nil || header.Text != "Name" {
		t.Fatalf("headers row clobbered by the loading row: cell(0,0) = %+v", header)
	}
}

// TestStatusTextStopsLoading pins the off state: every terminal status
// (error, success, or the plain reset) must remove the row and stop the
// ticker, so a finished fetch leaves a clean panel.
func TestStatusTextStopsLoading(t *testing.T) {
	a := newLoadingTestApp(t)
	a.applyFilter()

	a.statusProgress("Fetching VMs...")
	if a.loading == nil || a.loading.stop == nil {
		t.Fatal("loading state not running after statusProgress")
	}
	frame := a.info.GetCell(1, 0).Text

	a.statusText("[green]Fetched 12 VMs[-]")

	if a.loading.stop != nil {
		t.Error("loading ticker still running after a terminal status")
	}
	if a.info.GetRowCount() > 1 {
		t.Fatalf("loading row survived the terminal status: %q", a.info.GetCell(1, 0).Text)
	}
	if frame == a.info.GetCell(1, 0).Text {
		t.Error("row read after removal")
	}
}

// TestLoadingRowVanishesWhenRowsArrive pins the repaint path: a populated
// panel is worth more than the animation, so applyFilter with rows must
// drop the loading row and paint the data.
func TestLoadingRowVanishesWhenRowsArrive(t *testing.T) {
	a := newLoadingTestApp(t)
	a.applyFilter()

	a.statusProgress("Fetching projects...")
	a.setFilterBase([][]string{{"p1", "running"}})
	a.applyFilter()

	if a.info.GetRowCount() != 2 {
		t.Fatalf("panel rows = %d, want headers + 1 data row", a.info.GetRowCount())
	}
	if got := a.info.GetCell(1, 0).Text; got != "p1" {
		t.Fatalf("data row clobbered: cell(1,0) = %q, want p1", got)
	}
}

// TestLoadingRowSurvivesEmptyRepaint pins the guard in populateInfoPanel:
// filter edits repaint the panel constantly while a fetch runs, and an
// empty rows slice must not erase the animated row.
func TestLoadingRowSurvivesEmptyRepaint(t *testing.T) {
	a := newLoadingTestApp(t)
	a.applyFilter()

	a.statusProgress("Fetching VMs...")
	a.applyFilter() // the filter changed, panel still empty

	if a.info.GetRowCount() < 2 {
		t.Fatal("loading row erased by an empty repaint")
	}
	if cell := a.info.GetCell(1, 0); cell == nil || !isSpinnerRune([]rune(cell.Text)[0]) {
		t.Fatalf("loading row damaged: %+v", cell)
	}
}

// TestLoadingRowStaysOffPopulatedPanels pins the refresh case: when the
// panel already shows rows, a background refresh keeps them on screen
// instead of trading them for the animation.
func TestLoadingRowStaysOffPopulatedPanels(t *testing.T) {
	a := newLoadingTestApp(t)
	a.setFilterBase([][]string{{"vm-1", "running"}})
	a.applyFilter()

	a.statusProgress("Fetching VMs...")

	if got := a.info.GetCell(1, 0).Text; got != "vm-1" {
		t.Fatalf("data row replaced by the loading row: %q", got)
	}
	if a.info.GetRowCount() != 2 {
		t.Fatalf("panel rows = %d, want headers + 1 data row", a.info.GetRowCount())
	}
}

// TestLoadingTickerAdvancesFrames pins the animation itself: with the
// ticker running, the row's frame changes within a tick interval.
func TestLoadingTickerAdvancesFrames(t *testing.T) {
	a := newLoadingTestApp(t)
	a.applyFilter()

	a.statusProgress("Fetching VMs...")
	first := a.info.GetCell(1, 0).Text

	deadline := time.Now().Add(3 * time.Second)
	moved := false
	for time.Now().Before(deadline) {
		a.uiMu.Lock()
		cur := a.info.GetCell(1, 0).Text
		a.uiMu.Unlock()
		if cur != first {
			moved = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.statusText("")
	if !moved {
		t.Fatal("loading row frame never advanced")
	}
}

// TestLoadingMessageTrimsStatusDecorations pins the message adapter: the
// panel row carries the message alone, without the status line's spinner
// prefix or step counter.
func TestLoadingMessageTrimsStatusDecorations(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Fetching VMs...", "Fetching VMs..."},
		{"⠙ Fetching VMs...", "Fetching VMs..."},
		{"Fetching VMs... (3/12)", "Fetching VMs..."},
		{"⠹ Updating all views (2/10)", "Updating all views"},
		{"Fetching things (v2) ok", "Fetching things (v2) ok"}, // not a step counter
	}
	for _, c := range cases {
		if got := loadingMessage(c.in); got != c.want {
			t.Errorf("loadingMessage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStartLoadingWithoutPanelIsSafe pins the nil guard: an app whose info
// panel never got built must not crash when a fetch reports progress.
func TestStartLoadingWithoutPanelIsSafe(t *testing.T) {
	a := &app{status: tview.NewTextView().SetDynamicColors(true)}
	a.statusProgress("Fetching VMs...")
	a.statusText("")
	if a.loading != nil {
		t.Error("loading state created without a panel to paint it on")
	}
}
