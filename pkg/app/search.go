package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// searchEntry is one indexed resource row: the All view it came from, the row
// as rendered by that view, the project the row belongs to, and the row text
// pre-lowered for matching.
type searchEntry struct {
	kind    viewKind
	row     []string
	project string
	text    string
}

// searchIndex is a lazily built, in-memory index over the cached data of the
// All views (the cache-only variants of every resource list). It exists for
// the lifetime of the app. Entries are built in the background because view
// loads are cache-only, and re-marked dirty whenever a cache save lands. The
// state is guarded by one mutex because reads (matching, from the UI thread)
// and writes (building, from a background goroutine) race.
type searchIndex struct {
	mu             sync.Mutex
	entries        map[viewKind][]searchEntry
	warm           map[viewKind]bool
	dirty          map[viewKind]bool
	pulled         map[viewKind]bool
	projectsPulled bool
	building       bool
}

func newSearchIndex() *searchIndex {
	return &searchIndex{
		entries: map[viewKind][]searchEntry{},
		warm:    map[viewKind]bool{},
		dirty:   map[viewKind]bool{},
		pulled:  map[viewKind]bool{},
	}
}

// searchKinds lists the All views the index covers, in build order. The most
// commonly searched resources come first so progressive results are useful
// early.
var searchKinds = []viewKind{
	viewVMsAll,
	viewIPsAll,
	viewNetworksAll,
	viewBucketsAll,
	viewLBsAll,
	viewClustersAll,
	viewInstanceGroupsAll,
	viewInstanceTemplatesAll,
	viewDisksAll,
	viewSnapshotsAll,
	viewImagesAll,
	viewRoutesAll,
	viewFirewallsAll,
	viewPeeringsAll,
	viewAccountsAll,
	viewDNSZonesAll,
	viewCloudRoutersAll,
	viewVPNsAll,
	viewPSCAll,
	viewReservationsAll,
	viewIAMAll,
}

// searchIndexedKind maps any view kind to the All view whose cache it feeds.
// Per-project kinds share the cache tables of their All twin, so indexing
// the All views covers both. Kinds with no All variant are not indexed.
func searchIndexedKind(kind viewKind) viewKind {
	switch kind {
	case viewProjects, viewPublicImages, viewWorkloads, viewNodePools,
		viewDNSRecords, viewBucketIAM:
		return ""
	}
	if strings.HasSuffix(string(kind), "-all") {
		return kind
	}
	return viewKind(string(kind) + "-all")
}

// invalidate marks the entries for kind stale. The next ensureSearchIndex
// rebuilds them.
func (idx *searchIndex) invalidate(kind viewKind) {
	if kind == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.warm[kind] = false
	idx.dirty[kind] = true
}

// ensureSearchIndex queues a background build of every cold or invalidated
// kind. With force it rebuilds all kinds. Safe to call repeatedly: while a
// build runs, new dirty marks are picked up by the running loop.
func (a *app) ensureSearchIndex(force bool) {
	idx := a.searchIndex
	if idx == nil {
		return
	}
	idx.mu.Lock()
	for _, kind := range searchKinds {
		if force || !idx.warm[kind] {
			idx.dirty[kind] = true
		}
	}
	if idx.building {
		idx.mu.Unlock()
		return
	}
	idx.building = true
	idx.mu.Unlock()
	a.goBackground(a.buildSearchIndex)
}

// buildSearchIndex builds every dirty kind one at a time. After each kind it
// re-runs the live search so results appear progressively while the rest of
// the index is still building. Load errors leave the kind cold and are not
// retried in this pass (a persistent error must not spin the loop); the next
// open or invalidation retries it.
//
// A kind with no cached entries at all is assumed to be missing data, not
// empty infrastructure: the builder pulls it in the background (one pass per
// kind per app run, so a project without VMs does not refetch forever). The
// pull saves through the normal cache path, which invalidates the kind and
// queues this loop again, so results keep arriving while search is open.
func (a *app) buildSearchIndex() {
	idx := a.searchIndex
	for {
		idx.mu.Lock()
		var kind viewKind
		for _, k := range searchKinds {
			if idx.dirty[k] {
				kind = k
				break
			}
		}
		if kind == "" {
			idx.building = false
			idx.mu.Unlock()
			return
		}
		delete(idx.dirty, kind)
		idx.mu.Unlock()

		entries, err := a.loadSearchEntries(kind)
		idx.mu.Lock()
		if err != nil {
			idx.entries[kind] = nil
			idx.warm[kind] = false
		} else {
			idx.entries[kind] = entries
			idx.warm[kind] = true
			if len(entries) == 0 && !idx.pulled[kind] {
				idx.pulled[kind] = true
				a.goBackground(func() { a.pullSearchKind(kind) })
			}
		}
		idx.mu.Unlock()

		if a.searchOpen.Load() {
			a.queueUpdateDraw(a.runSearch)
		}
	}
}

// pullSearchKind fetches the kind's resources for every cached project in the
// background and saves them through the view's normal fetch path. It runs
// outside idx.mu because network calls are slow; markViewUpdated re-arms the
// builder when the saves land.
func (a *app) pullSearchKind(kind viewKind) {
	projects, err := a.cache.ListProjects()
	if err != nil || len(projects) == 0 {
		return
	}
	perKind := viewKind(strings.TrimSuffix(string(kind), "-all"))
	def, ok := views[perKind]
	if !ok || def.fetch == nil {
		return
	}
	label := views[kind].title
	for _, proj := range projects {
		if _, _, err := def.fetch(a, proj.ProjectID); err != nil {
			a.reportError(string(perKind), proj.ProjectID, "Search pull failed for "+label, err)
		}
	}
}

// ensureSearchProjects bootstraps an empty project cache: without a project
// list there is nothing to index and nothing to pull per project. One attempt
// per app run, in the background; the save lands through markViewUpdated,
// which re-arms the index builder. It must not deadlock the UI: cache saves
// from a background goroutine are the same pattern allProjectsRefresh uses.
func (a *app) ensureSearchProjects() {
	idx := a.searchIndex
	if idx == nil {
		return
	}
	idx.mu.Lock()
	if idx.projectsPulled {
		idx.mu.Unlock()
		return
	}
	idx.projectsPulled = true
	idx.mu.Unlock()
	a.goBackground(func() {
		projects, err := listGcloudProjects()
		if err != nil {
			a.reportError("projects", "all", "Search failed to fetch projects", err)
			return
		}
		if err := a.cache.SaveProjects(projects); err != nil {
			a.reportError("projects", "all", "Search failed to update cache", err)
			return
		}
		a.ensureSearchIndex(true)
	})
}

// loadSearchEntries reads one All view through its cache-only load. The last
// column of every All view is the project the row belongs to.
func (a *app) loadSearchEntries(kind viewKind) ([]searchEntry, error) {
	def := views[kind]
	rows, err := def.load(a)
	if err != nil {
		return nil, err
	}
	perProject := len(def.headers) > 0 && def.headers[len(def.headers)-1] == "Project"
	entries := make([]searchEntry, 0, len(rows))
	for _, row := range rows {
		project := ""
		if perProject && len(row) > 0 {
			project = row[len(row)-1]
		}
		entries = append(entries, searchEntry{
			kind:    kind,
			row:     row,
			project: project,
			text:    strings.ToLower(strings.Join(row, " ")),
		})
	}
	return entries, nil
}

const maxSearchHits = 100

// parseSearchTerms splits a query into lowered include terms and exclude
// terms ("-x" must not match). Every include must match, no exclude may.
func parseSearchTerms(query string) (includes, excludes []string) {
	for _, term := range strings.Fields(query) {
		if strings.HasPrefix(term, "-") && len(term) > 1 {
			excludes = append(excludes, strings.ToLower(term[1:]))
		} else {
			includes = append(includes, strings.ToLower(term))
		}
	}
	return includes, excludes
}

func searchEntryMatches(text string, includes, excludes []string) bool {
	for _, term := range includes {
		if !strings.Contains(text, term) {
			return false
		}
	}
	for _, term := range excludes {
		if strings.Contains(text, term) {
			return false
		}
	}
	return true
}

// match returns the hits for query. It reads the index under lock; a kind
// still building simply has no entries yet. Results are capped just above the
// display limit so the title can say "more than N".
func (idx *searchIndex) match(query string) []searchEntry {
	includes, excludes := parseSearchTerms(query)
	idx.mu.Lock()
	defer idx.mu.Unlock()
	var hits []searchEntry
	for _, kind := range searchKinds {
		for _, e := range idx.entries[kind] {
			if !searchEntryMatches(e.text, includes, excludes) {
				continue
			}
			hits = append(hits, e)
			if len(hits) > maxSearchHits {
				return hits
			}
		}
	}
	return hits
}

func (idx *searchIndex) warmCount() int {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	count := 0
	for _, kind := range searchKinds {
		if idx.warm[kind] {
			count++
		}
	}
	return count
}

// searchPanelWidth is the fixed width the left panel takes from the main
// view while search is open.
const searchPanelWidth = 46

// buildSearchPanel creates the left search panel: a query input on top and a
// results table below, stacked so they share the column width. The panel
// lives in searchColumn next to the main view; opening search resizes that
// column so the main view shrinks.
func (a *app) buildSearchPanel() {
	input := tview.NewInputField().SetLabel(" search: ").SetFieldWidth(0)
	input.SetBorder(true)
	results := tview.NewTable().SetBorders(false).SetSelectable(true, false)
	results.SetBorder(true)
	a.searchInput = input
	a.searchResults = results

	input.SetChangedFunc(func(text string) { a.runSearch() })
	input.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter && results.GetRowCount() > 1 {
			a.tapp.SetFocus(results)
		}
	})
	results.SetSelectedFunc(func(row, _ int) { a.searchSelect(row) })

	panel := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(input, 3, 1, true).
		AddItem(results, 0, 1, true)
	a.searchPanel = panel
}

// buildMainLayout wires the shared widget tree run() mounts: the summary
// row, command input, info table and status line stacked as the main view,
// the search side panel next to it in a zero-width column, and both pages.
// It exists apart from run() so tests can exercise the real wiring without
// starting the terminal loop.
func (a *app) buildMainLayout(helpOverlay *tview.Flex) {
	a.buildSearchPanel()

	summary := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(a.summary, 0, 1, false).
		AddItem(a.shortcutsSummary, 0, 1, false)

	mainView := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(summary, 4, 1, false).
		AddItem(a.cmd, 0, 0, false).
		AddItem(a.info, 0, 1, false).
		AddItem(a.infobar, infobarHeight(infobarEnabled), infobarProportion(infobarEnabled), false).
		AddItem(a.status, 1, 1, false)
	a.mainView = mainView

	// The search column stays zero-width until search opens, then takes a
	// fixed slice so the main view shrinks instead of being covered.
	searchColumn := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(a.searchPanel, 0, 0, false).
		AddItem(mainView, 0, 1, false)
	a.searchColumn = searchColumn

	a.pages.AddPage("main", searchColumn, true, true)
	a.pages.AddPage("help", helpOverlay, true, false)
}

// searchPlaceholder is shown instead of results while the query is empty, so
// opening the panel does not read as "every resource matches nothing".
const searchPlaceholder = "type to search"

// searchPanelWidthFor scales the side panel to the terminal: the full 46
// columns on wide terminals, a smaller share below 100 so the main view
// keeps usable room.
func searchPanelWidthFor(width int) int {
	if width <= 0 {
		return searchPanelWidth
	}
	if width < 100 {
		return width * 2 / 5
	}
	return searchPanelWidth
}

func (a *app) openSearch() {
	if a.searchIndex == nil {
		a.searchIndex = newSearchIndex()
	}
	if a.searchPanel == nil || a.searchColumn == nil {
		return
	}
	a.searchInput.SetText(a.searchQuery)
	a.searchPanelFocus = true
	a.searchOpen.Store(true)
	width := 0
	if a.screen != nil {
		width, _ = a.screen.Size()
	}
	a.searchColumn.ResizeItem(a.searchPanel, searchPanelWidthFor(width), 1)
	a.tapp.SetFocus(a.searchInput)
	a.runSearch()
	a.updateSearchPanelStyle()
	a.ensureSearchIndex(false)
	a.ensureSearchProjects()
}

func (a *app) closeSearch() {
	if !a.searchOpen.Load() {
		return
	}
	a.searchOpen.Store(false)
	a.searchPanelFocus = false
	a.searchQuery = strings.TrimSpace(a.searchInput.GetText())
	if a.searchColumn != nil {
		a.searchColumn.ResizeItem(a.searchPanel, 0, 0)
	}
	a.updateSearchPanelStyle()
	a.tapp.SetFocus(a.info)
}

// toggleSearchFocus moves focus between the search panel and the main view,
// keeping the panel open either way.
func (a *app) toggleSearchFocus() {
	if !a.searchOpen.Load() {
		return
	}
	a.searchPanelFocus = !a.searchPanelFocus
	if a.searchPanelFocus {
		a.tapp.SetFocus(a.searchInput)
	} else {
		a.tapp.SetFocus(a.info)
	}
	a.updateSearchPanelStyle()
}

// updateSearchPanelStyle colors the search panel border while the panel holds
// focus and restores the theme border while focus sits on the main view, so
// Tab's effect is visible before any typing.
func (a *app) updateSearchPanelStyle() {
	if a.searchInput == nil || a.searchResults == nil {
		return
	}
	focused := a.searchOpen.Load() && a.searchPanelFocus
	border := theme.borderColor()
	if focused {
		border = theme.searchFocusBorderColor()
	}
	a.searchInput.SetBorderColor(border)
	a.searchResults.SetBorderColor(border)
}

// runSearch re-runs the query against the index and refreshes the results
// table. It runs on the UI thread (keystrokes) and from the index builder
// via queueUpdateDraw.
func (a *app) runSearch() {
	if !a.searchOpen.Load() {
		return
	}
	query := a.searchInput.GetText()
	a.searchHits = a.searchIndex.match(query)
	a.refreshSearchResults()
}

// searchGroupRow maps a grouped results row to the resource group it belongs
// to. Header rows open the whole resource list. The entry is stored by value
// so a later refresh reassigning the hits slice cannot leave a row pointing
// at stale memory.
type searchGroupRow struct {
	header bool
	hit    searchEntry
}

// searchGroupJumpers maps each indexed kind to the same view-opening trigger
// the numeric shortcuts use. Selecting a group header in the panel opens that
// resource list in the main view without closing the panel.
var searchGroupJumpers = map[viewKind]func(a *app){
	viewVMsAll:               showVMs,
	viewIPsAll:               (*app).showIPs,
	viewNetworksAll:          (*app).showNetworks,
	viewBucketsAll:           showBuckets,
	viewLBsAll:               showLBs,
	viewClustersAll:          showClusters,
	viewInstanceGroupsAll:    showInstanceGroups,
	viewInstanceTemplatesAll: showInstanceTemplates,
	viewDisksAll:             showDisks,
	viewSnapshotsAll:         showSnapshots,
	viewImagesAll:            showImages,
	viewRoutesAll:            showRoutes,
	viewFirewallsAll:         showFirewalls,
	viewPeeringsAll:          showPeerings,
	viewAccountsAll:          showAccounts,
	viewDNSZonesAll:          showDNSZones,
	viewCloudRoutersAll:      showCloudRouters,
	viewVPNsAll:              showVPNs,
	viewPSCAll:               showPSC,
	viewReservationsAll:      showReservations,
	viewIAMAll:               showIAM,
}

// refreshSearchResults refills the results table with one section header per
// resource type followed by its hits, so every group shows its own count and
// the panel doubles as a per-type overview of the query. While the index is
// still building, the title keeps a "searching" marker so partial results are
// not mistaken for the final count.
func (a *app) refreshSearchResults() {
	table := a.searchResults
	table.Clear()
	a.searchGroupRows = a.searchGroupRows[:0]
	headers := []string{"Name", "Project"}
	for col, header := range headers {
		table.SetCell(0, col, tview.NewTableCell(header).
			SetTextColor(tview.Styles.SecondaryTextColor).
			SetSelectable(false).
			SetExpansion(1).
			SetAttributes(tcell.AttrBold))
	}

	shown := a.searchHits
	overflow := 0
	if len(shown) > maxSearchHits {
		shown = shown[:maxSearchHits]
		overflow = len(a.searchHits) - maxSearchHits
	}
	if strings.TrimSpace(a.searchInput.GetText()) == "" {
		shown = nil
		overflow = 0
	}
	row := 1
	var currentKind viewKind
	for i := range shown {
		hit := &shown[i]
		if hit.kind != currentKind {
			currentKind = hit.kind
			// The » prefix marks group headers: they open the whole resource
			// list in the main view instead of jumping to one row.
			table.SetCell(row, 0, tview.NewTableCell(fmt.Sprintf("» %s (%d)", views[hit.kind].title, searchKindCount(shown, hit.kind))).
				SetTextColor(theme.titleColor()).
				SetSelectable(true).
				SetAttributes(tcell.AttrBold).
				SetExpansion(1))
			table.SetCell(row, 1, tview.NewTableCell("").SetSelectable(true))
			a.searchGroupRows = append(a.searchGroupRows, searchGroupRow{header: true, hit: *hit})
			row++
		}
		name := ""
		if len(hit.row) > 0 {
			name = hit.row[0]
		}
		table.SetCell(row, 0, tview.NewTableCell(name).SetExpansion(1))
		table.SetCell(row, 1, tview.NewTableCell(hit.project))
		a.searchGroupRows = append(a.searchGroupRows, searchGroupRow{hit: *hit})
		row++
	}
	if len(a.searchGroupRows) > 0 {
		table.Select(1, 0)
	}
	table.SetFixed(1, 0)

	title := fmt.Sprintf(" %d", len(a.searchHits))
	if overflow > 0 {
		title = fmt.Sprintf(" %d+", maxSearchHits)
	}
	if strings.TrimSpace(a.searchInput.GetText()) == "" {
		title = " " + searchPlaceholder + " "
	} else if a.searchIndex.warmCount() < len(searchKinds) {
		title += " matches | searching… "
	} else {
		title += " matches "
	}
	table.SetTitle(title)
}

// searchKindCount counts the hits of one resource type within the shown
// slice, so a group header never claims more rows than the panel displays
// when maxSearchHits truncation cuts a group short.
func searchKindCount(shown []searchEntry, kind viewKind) int {
	count := 0
	for i := range shown {
		if shown[i].kind == kind {
			count++
		}
	}
	return count
}

// searchSelect acts on one row of the grouped results: a header row opens
// the resource list view (the same trigger as its numeric shortcut), a hit
// row jumps to that resource with the filter preset.
func (a *app) searchSelect(row int) {
	if row <= 0 || row > len(a.searchGroupRows) {
		return
	}
	group := a.searchGroupRows[row-1]
	if group.header {
		jump, ok := searchGroupJumpers[group.hit.kind]
		if !ok {
			return
		}
		jump(a)
		a.tapp.SetFocus(a.info)
		a.searchPanelFocus = false
		a.updateSearchPanelStyle()
		return
	}
	a.searchJump(&group.hit)
}

// searchJump opens the hit's resource in its per-project view, with the
// filter preset to the hit's name, following the node pool VM handoff.
func (a *app) searchJump(hit *searchEntry) {
	a.closeSearch()
	if hit.project == "" {
		a.statusText("[red]Result has no project[-]")
		return
	}
	appState.AllProjects = false
	appState.ProjectID = hit.project
	if p, err := a.cache.GetProject(hit.project); err == nil && p != nil {
		appState.Name = p.Name
		appState.ProjectNumber = p.ProjectNumber
	}
	renderSummary(a.summary)
	kind := viewKind(strings.TrimSuffix(string(hit.kind), "-all"))
	a.setView(kind)

	name := ""
	if len(hit.row) > 0 {
		name = hit.row[0]
	}
	appState.CmdMode = modeFilter
	a.cmd.SetBorder(true)
	a.cmd.SetLabel(" / ")
	a.cmd.SetText(name)
	a.applyFilter()
	for i, r := range a.visibleRows {
		if len(r) > 0 && r[0] == name {
			a.info.Select(i+1, 0)
			break
		}
	}
	a.tapp.SetFocus(a.info)
	a.statusInfo(fmt.Sprintf("%s: %s (Esc clears filter)", views[kind].title, name))
}
