package app

import (
	"fmt"
	"strings"
	"sync"

	"github.com/elferherrera/lazycloud/cmd/providers"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// searchScope bounds a search to every project or the current one.
type searchScope int

const (
	searchAllProjects searchScope = iota
	searchCurrentProject
)

func (s searchScope) label() string {
	if s == searchCurrentProject {
		return "current project"
	}
	return "all projects"
}

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
	go a.buildSearchIndex()
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
				go a.pullSearchKind(kind)
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
	go func() {
		projects, err := providers.ListGcloudProjects()
		if err != nil {
			a.reportError("projects", "all", "Search failed to fetch projects", err)
			return
		}
		if err := a.cache.SaveProjects(projects); err != nil {
			a.reportError("projects", "all", "Search failed to update cache", err)
			return
		}
		a.ensureSearchIndex(true)
	}()
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

// match returns the hits for query within scope. It reads the index under
// lock; a kind still building simply has no entries yet. Results are capped
// just above the display limit so the title can say "more than N".
func (idx *searchIndex) match(query string, scope searchScope, projectID string) []searchEntry {
	includes, excludes := parseSearchTerms(query)
	idx.mu.Lock()
	defer idx.mu.Unlock()
	var hits []searchEntry
	for _, kind := range searchKinds {
		for _, e := range idx.entries[kind] {
			if scope == searchCurrentProject && e.project != projectID {
				continue
			}
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

func (a *app) buildSearchPage() {
	input := tview.NewInputField().SetLabel(" search: ").SetFieldWidth(0)
	input.SetBorder(true)
	results := tview.NewTable().SetBorders(false).SetSelectable(true, false)
	results.SetBorder(true)
	a.searchInput = input
	a.searchResults = results

	input.SetChangedFunc(func(text string) { a.runSearch() })
	input.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			if results.GetRowCount() > 1 {
				a.tapp.SetFocus(results)
			}
		case tcell.KeyTab, tcell.KeyBacktab:
			a.cycleSearchScope()
		}
	})
	results.SetSelectedFunc(func(row, _ int) { a.searchJump(row) })

	page := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(input, 3, 1, true).
		AddItem(results, 0, 1, true)
	a.pages.AddPage("search", page, true, false)
}

func (a *app) openSearch() {
	if a.searchIndex == nil {
		a.searchIndex = newSearchIndex()
	}
	if a.searchInput == nil {
		a.buildSearchPage()
	}
	a.searchInput.SetText(a.searchQuery)
	a.searchOpen.Store(true)
	a.pages.ShowPage("search")
	a.tapp.SetFocus(a.searchInput)
	a.runSearch()
	a.ensureSearchIndex(false)
	a.ensureSearchProjects()
}

func (a *app) closeSearch() {
	if !a.searchOpen.Load() {
		return
	}
	a.searchOpen.Store(false)
	a.searchQuery = strings.TrimSpace(a.searchInput.GetText())
	a.pages.HidePage("search")
	a.tapp.SetFocus(a.info)
}

func (a *app) cycleSearchScope() {
	if a.searchScope == searchAllProjects {
		a.searchScope = searchCurrentProject
	} else {
		a.searchScope = searchAllProjects
	}
	a.runSearch()
}

// runSearch re-runs the query against the index and refreshes the results
// table. It runs on the UI thread (keystrokes) and from the index builder
// via queueUpdateDraw.
func (a *app) runSearch() {
	if !a.searchOpen.Load() {
		return
	}
	query := a.searchInput.GetText()
	a.searchHits = a.searchIndex.match(query, a.searchScope, appState.ProjectID)
	a.refreshSearchResults()
}

func (a *app) refreshSearchResults() {
	table := a.searchResults
	table.Clear()
	headers := []string{"View", "Name", "Details", "Project"}
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
	for i, hit := range shown {
		name := ""
		if len(hit.row) > 0 {
			name = hit.row[0]
		}
		details := ""
		if len(hit.row) > 2 {
			details = strings.Join(hit.row[1:len(hit.row)-1], " ")
		}
		cells := []string{views[hit.kind].title, name, details, hit.project}
		for col, val := range cells {
			table.SetCell(i+1, col, tview.NewTableCell(val))
		}
	}
	if len(shown) > 0 {
		table.Select(1, 0)
	}
	table.SetFixed(1, 0)

	title := fmt.Sprintf(" %d", len(a.searchHits))
	if overflow > 0 {
		title = fmt.Sprintf(" %d+", maxSearchHits)
	}
	title += fmt.Sprintf(" matches | %d/%d views indexed | scope: %s ",
		a.searchIndex.warmCount(), len(searchKinds), a.searchScope.label())
	table.SetTitle(title)
}

// searchJump opens the hit's resource in its per-project view, with the
// filter preset to the hit's name, following the node pool VM handoff.
func (a *app) searchJump(row int) {
	if row <= 0 || row > len(a.searchHits) {
		return
	}
	hit := a.searchHits[row-1]
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
