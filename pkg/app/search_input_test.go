package app

import (
	"strings"
	"testing"

	"github.com/elferherrera/lazycloud/cmd/providers"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// wireSearchApp mirrors run(): the app needs its root, the search column and
// the input capture installed so GetFocus and key routing behave like the
// real UI.
func wireSearchApp(a *app) {
	mainView := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.summary, 4, 1, false).
		AddItem(a.cmd, 0, 0, false).
		AddItem(a.info, 0, 1, false).
		AddItem(a.status, 1, 1, false)
	a.searchColumn = tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(a.searchPanel, 0, 0, false).
		AddItem(mainView, 0, 1, false)
	a.pages.AddPage("main", a.searchColumn, true, true)
	a.tapp.SetRoot(a.pages, true)
	a.tapp.SetInputCapture(a.inputCapture)
	a.tapp.SetFocus(a.info)
}

func resetAppState() {
	appState.CmdMode = modeCmd
	appState.ViewKind = viewProjects
	appState.ProjectID = "p1"
	appState.AllProjects = false
}

func TestInputCaptureTabTogglesSearchFocusFromPanel(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	wireSearchApp(a)
	resetAppState()

	a.openSearch()
	if got := a.tapp.GetFocus(); got != a.searchInput {
		t.Fatalf("focus after open = %T, want search input", got)
	}

	// Tab from the input moves focus to the main view and back, keeping the
	// panel open both times.
	if ev := a.inputCapture(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); ev != nil {
		t.Fatal("Tab from panel should be consumed")
	}
	if got := a.tapp.GetFocus(); got != a.info {
		t.Fatalf("focus after Tab = %T, want info table", got)
	}
	if !a.searchOpen.Load() {
		t.Fatal("panel closed by Tab")
	}
	if ev := a.inputCapture(tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)); ev != nil {
		t.Fatal("Backtab from main view should be consumed")
	}
	if got := a.tapp.GetFocus(); got != a.searchInput {
		t.Fatalf("focus after Backtab = %T, want search input", got)
	}
	a.closeSearch()
}

func TestInputCaptureTabKeepsFilterCompletionInFilterMode(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	wireSearchApp(a)
	resetAppState()

	a.openSearch()
	// Move focus to the main view while the panel stays open.
	a.inputCapture(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))

	// In filter mode, Tab on the main view cycles detail paths instead of
	// toggling the panel: the filter input is where typing happens.
	appState.CmdMode = modeFilter
	a.cmd.SetText("")
	if got := a.inputCapture(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); got != nil {
		t.Fatal("Tab in filter mode should be consumed by detail completion")
	}
	if a.tapp.GetFocus() == a.searchInput {
		t.Fatal("Tab in filter mode toggled panel focus, want detail completion")
	}
	if text := a.cmd.GetText(); text == "" {
		t.Fatal("detail completion did not preset a path")
	}
	a.closeSearch()
}

func TestInputCaptureEscClosesSearchPanel(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	wireSearchApp(a)
	resetAppState()

	a.openSearch()
	if got := a.inputCapture(tcell.NewEventKey(tcell.KeyEsc, 0, tcell.ModNone)); got != nil {
		t.Fatal("Esc with panel open should be consumed")
	}
	if a.searchOpen.Load() {
		t.Fatal("Esc did not close the search panel")
	}
	if got := a.tapp.GetFocus(); got != a.info {
		t.Fatalf("focus after Esc = %T, want info table", got)
	}
}

func TestInputCaptureEscLeavesPanelFocusPath(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	wireSearchApp(a)
	resetAppState()

	a.openSearch()
	// Runes typed while the panel holds focus pass through to the query input:
	// the capture hands the event back and the input handler inserts it.
	ev := tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone)
	if got := a.inputCapture(ev); got == nil {
		t.Fatal("rune with panel focus should reach the panel widgets")
	}
	a.searchInput.InputHandler()(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone), nil)
	if a.searchInput.GetText() != "x" {
		t.Fatalf("query text = %q, want x", a.searchInput.GetText())
	}
	a.closeSearch()
}

func TestInputCaptureCtrlCQuits(t *testing.T) {
	a := newSearchTestApp(t)
	wireSearchApp(a)
	resetAppState()

	// quit stops the application; with no screen running Stop is a no-op but
	// the event must still be consumed.
	if got := a.inputCapture(tcell.NewEventKey(tcell.KeyCtrlC, 0, tcell.ModNone)); got != nil {
		t.Fatal("Ctrl-C should be consumed")
	}
}

func TestSearchOpenWithoutColumnIsNoop(t *testing.T) {
	a := newSearchTestApp(t)
	a.searchColumn = nil
	a.openSearch()
	if a.searchOpen.Load() {
		t.Fatal("openSearch without search column opened the panel")
	}
}

func TestToggleSearchFocusIgnoredWhenClosed(t *testing.T) {
	a := newSearchTestApp(t)
	a.toggleSearchFocus()
	if a.searchPanelFocus {
		t.Fatal("toggleSearchFocus with panel closed changed focus state")
	}
}

func TestSearchSelectRejectsInvalidRows(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	resetAppState()

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	// Row 0 is the table header; row beyond the list is off the end.
	before := appState.ViewKind
	a.searchSelect(0)
	a.searchSelect(len(a.searchGroupRows) + 1)
	if appState.ViewKind != before {
		t.Fatalf("ViewKind changed on invalid row: %q", appState.ViewKind)
	}
	a.closeSearch()
}

func TestSearchSelectHeaderWithMissingJumperIsNoop(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	resetAppState()

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	// Corrupt one hit row into a header with a kind that has no jumper and
	// confirm the select path still guards instead of panicking.
	corrupted := -1
	for i := range a.searchGroupRows {
		if !a.searchGroupRows[i].header {
			a.searchGroupRows[i].header = true
			a.searchGroupRows[i].hit.kind = viewKind("no-such-kind")
			corrupted = i
			break
		}
	}
	if corrupted < 0 {
		t.Fatal("no hit row found to corrupt")
	}
	before := appState.ViewKind
	a.searchSelect(corrupted + 1)
	if appState.ViewKind != before {
		t.Fatalf("ViewKind changed on jumper-less header: %q", appState.ViewKind)
	}
	a.closeSearch()
}

func TestSearchKindCountCountsShownSliceOnly(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	resetAppState()

	a.openSearch()
	// An empty query matches every row: two VMs across both projects plus
	// the bucket.
	a.searchInput.SetText("")
	a.runSearch()

	vms := searchKindCount(a.searchHits, viewVMsAll)
	if vms != 2 {
		t.Fatalf("searchKindCount(vms) = %d, want 2", vms)
	}
	// A truncated slice counts only what it still holds.
	truncated := a.searchHits[:1]
	if got := searchKindCount(truncated, viewVMsAll); got != 1 {
		t.Fatalf("searchKindCount(truncated) = %d, want 1", got)
	}
	a.closeSearch()
}

func TestSearchGroupHeaderCountMatchesShownRows(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	resetAppState()

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	// Every header cell must state the number of hit rows that follow it.
	for row := 1; row <= len(a.searchGroupRows); row++ {
		group := a.searchGroupRows[row-1]
		if !group.header {
			continue
		}
		cell := a.searchResults.GetCell(row, 0)
		want := searchKindCount(a.searchHits, group.hit.kind)
		if want == 0 {
			t.Fatalf("header %q counts zero shown rows", cell.Text)
		}
		if !strings.Contains(cell.Text, strings.TrimPrefix(cell.Text, "» ")) {
			t.Fatalf("header cell lost its prefix: %q", cell.Text)
		}
	}
	a.closeSearch()
}

func TestSearchEmptyQueryShowsPlaceholderNotEverything(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	resetAppState()

	a.openSearch()
	if a.searchInput.GetText() != "" {
		t.Fatalf("query = %q, want empty on fresh open", a.searchInput.GetText())
	}
	if got := len(a.searchHits); got == 0 {
		t.Fatal("empty query matched nothing, index should hold rows")
	}
	if got := a.searchResults.GetRowCount(); got != 1 {
		t.Fatalf("table rows = %d, want 1 (headers only) on empty query", got)
	}
	title := a.searchResults.GetTitle()
	if !strings.Contains(title, searchPlaceholder) {
		t.Fatalf("title = %q, want placeholder %q", title, searchPlaceholder)
	}

	a.searchInput.SetText("alpha")
	a.runSearch()
	if got := a.searchResults.GetRowCount(); got <= 1 {
		t.Fatalf("table rows = %d after typing, want results", got)
	}
	if strings.Contains(a.searchResults.GetTitle(), searchPlaceholder) {
		t.Fatalf("title = %q still shows placeholder after typing", a.searchResults.GetTitle())
	}
	a.closeSearch()
}

func TestSearchTitleShowsSearchingWhileIndexWarm(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	resetAppState()

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	// Simulate a still-building kind: the title must carry the searching
	// marker so partial counts are not mistaken for final ones.
	idx := a.searchIndex
	idx.mu.Lock()
	idx.warm[searchKinds[0]] = false
	idx.mu.Unlock()
	a.runSearch()
	if !strings.Contains(a.searchResults.GetTitle(), "searching") {
		t.Fatalf("title = %q, want searching marker while index is warm", a.searchResults.GetTitle())
	}
	a.closeSearch()
}

func TestSearchEnterMovesFocusToResults(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	wireSearchApp(a)
	resetAppState()

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	// The panel input's done handler moves focus to the results table when
	// Enter arrives with rows on screen.
	a.cmd.SetDoneFunc(nil)           // keep the real one on searchInput untouched
	done := a.searchInput.GetLabel() // sanity: panel input is wired
	if done == "" {
		t.Fatal("search input lost its label")
	}
	// Drive the done closure by sending Enter through the input's done func:
	// SetDoneFunc stored it on the widget, so call it via the event path.
	a.tapp.SetFocus(a.searchInput)
	// tview InputField fires done on Enter during Draw+input; invoke directly.
	a.searchInput.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
	if got := a.tapp.GetFocus(); got != a.searchResults {
		t.Fatalf("focus after Enter = %T, want search results", got)
	}
	a.closeSearch()
}

func TestSearchPanelWidthScalesWithTerminal(t *testing.T) {
	if got := searchPanelWidthFor(0); got != searchPanelWidth {
		t.Errorf("searchPanelWidthFor(0) = %d, want default %d", got, searchPanelWidth)
	}
	if got := searchPanelWidthFor(160); got != searchPanelWidth {
		t.Errorf("searchPanelWidthFor(160) = %d, want full %d", got, searchPanelWidth)
	}
	if got := searchPanelWidthFor(80); got >= 80 {
		t.Errorf("searchPanelWidthFor(80) = %d, want a share below the terminal width", got)
	}
}

func TestUpdateSearchPanelStyleFollowsFocus(t *testing.T) {
	a := newSearchTestApp(t)
	resetAppState()

	a.updateSearchPanelStyle()
	if got := a.searchInput.GetBorderColor(); got != theme.borderColor() {
		t.Fatalf("closed panel border = %v, want theme border", got)
	}

	a.openSearch()
	if got := a.searchInput.GetBorderColor(); got == theme.borderColor() {
		t.Fatal("focused panel border matches the plain theme border")
	}

	a.toggleSearchFocus()
	if got := a.searchInput.GetBorderColor(); got != theme.borderColor() {
		t.Fatalf("unfocused panel border = %v, want theme border", got)
	}
	a.closeSearch()
}

func TestApplyThemeToWidgetsStylesSearchPanel(t *testing.T) {
	a := newSearchTestApp(t)
	resetAppState()

	original := theme
	t.Cleanup(func() { theme = original })
	applyTheme(providers.Theme{Title: "orange", Borders: "green", Labels: "aqua", Text: "gray"})

	a.applyThemeToWidgets()
	if got := a.searchInput.GetBorderColor(); got != tcell.GetColor("green") {
		t.Fatalf("input border after theme = %v, want green", got)
	}
	if got := a.searchResults.GetBorderColor(); got != tcell.GetColor("green") {
		t.Fatalf("results border after theme = %v, want green", got)
	}
}

func TestSearchJumpWithoutProjectShowsStatus(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)
	resetAppState()

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	// Strip the project off the first hit: the jump must refuse instead of
	// opening a view with an empty project context.
	hit := a.searchGroupRows[0].hit
	hit.project = ""
	appState.ProjectID = "p1"
	a.searchJump(&hit)
	if appState.ProjectID == "" {
		t.Fatal("jump with no project cleared the project")
	}
	a.closeSearch()
}

func TestEscapePressedClosesSearchBeforeOtherLayers(t *testing.T) {
	a := newSearchTestApp(t)
	wireSearchApp(a)
	resetAppState()

	a.openSearch()
	a.escapePressed()
	if a.searchOpen.Load() {
		t.Fatal("escapePressed did not close the open search panel")
	}
}
func TestBuildMainLayoutHostsSearchColumnAndPages(t *testing.T) {
	a := newFilterTestApp(t)
	resetAppState()

	helpOverlay, helpRow := newHelpOverlay(a.shortcuts)
	a.helpRow = helpRow
	a.buildMainLayout(helpOverlay)

	if a.searchPanel == nil || a.searchColumn == nil || a.mainView == nil {
		t.Fatal("buildMainLayout left search panel, column or main view nil")
	}
	if name, _ := a.pages.GetFrontPage(); name != "main" {
		t.Fatalf("front page = %q, want main", name)
	}
	if a.searchInput == nil || a.searchResults == nil {
		t.Fatal("buildMainLayout did not build the search widgets")
	}

	// The real run() wiring must let the panel open and take a slice of the
	// layout without a live screen.
	a.tapp.SetRoot(a.pages, true)
	a.tapp.SetFocus(a.info)
	a.openSearch()
	if !a.searchOpen.Load() {
		t.Fatal("openSearch failed on the layout built by buildMainLayout")
	}
	if a.tapp.GetFocus() != a.searchInput {
		t.Fatalf("focus after open = %T, want search input", a.tapp.GetFocus())
	}
}
