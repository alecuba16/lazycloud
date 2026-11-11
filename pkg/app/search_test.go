package app

import (
	"strings"
	"testing"

	"github.com/elferherrera/lazycloud/cmd/providers"
	"github.com/rivo/tview"
)

// seedSearchRows fills the cache with one VM in p1 and one in p2, plus a
// bucket in p1 only, so index and panel behavior has real rows to match.
func seedSearchRows(t *testing.T, a *app) {
	t.Helper()
	if err := a.cache.SaveProjects([]providers.Project{
		{ProjectID: "p1", Name: "P One", ProjectNumber: "111"},
		{ProjectID: "p2", Name: "P Two", ProjectNumber: "222"},
	}); err != nil {
		t.Fatalf("SaveProjects() error = %v", err)
	}
	if err := a.cache.SaveVMs("p1", []providers.VM{{Name: "alpha-web", Zone: "z1", Status: "RUNNING"}}); err != nil {
		t.Fatalf("SaveVMs(p1) error = %v", err)
	}
	if err := a.cache.SaveVMs("p2", []providers.VM{{Name: "beta-db", Zone: "z2", Status: "RUNNING"}}); err != nil {
		t.Fatalf("SaveVMs(p2) error = %v", err)
	}
	if err := a.cache.SaveBuckets("p1", []providers.Bucket{{Name: "alpha-bucket"}}); err != nil {
		t.Fatalf("SaveBuckets(p1) error = %v", err)
	}
}

func buildSearchIndexSync(t *testing.T, a *app) {
	t.Helper()
	if a.searchIndex == nil {
		a.searchIndex = newSearchIndex()
	}
	// Pre-mark every pull guard so the builder never reaches the network:
	// these tests exercise the cache-only index, not the background fetch.
	for _, kind := range searchKinds {
		a.searchIndex.pulled[kind] = true
	}
	a.searchIndex.projectsPulled = true
	a.ensureSearchIndex(true)
	// The builder goroutine finishes when every kind is warm; entries for
	// kinds with no cached rows come back empty, not dirty.
	idx := a.searchIndex
	for {
		idx.mu.Lock()
		done := true
		for _, kind := range searchKinds {
			if idx.dirty[kind] {
				done = false
				break
			}
		}
		building := idx.building
		idx.mu.Unlock()
		if done && !building {
			return
		}
	}
}

// newSearchTestApp returns an app with the panel built and wired into a
// search column, mirroring what run() sets up. Built on the fetch test
// app so background pulls have a logger.
func newSearchTestApp(t *testing.T) *app {
	t.Helper()
	a := newFetchTestApp(t)
	a.buildSearchPanel()
	mainView := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.summary, 4, 1, false).
		AddItem(a.cmd, 0, 0, false).
		AddItem(a.info, 0, 1, false).
		AddItem(a.status, 1, 1, false)
	a.searchColumn = tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(a.searchPanel, 0, 0, false).
		AddItem(mainView, 0, 1, false)
	return a
}

func TestSearchIndexedKindMapping(t *testing.T) {
	cases := []struct {
		kind viewKind
		want viewKind
	}{
		{viewVMs, viewVMsAll},
		{viewVMsAll, viewVMsAll},
		{viewProjects, ""},
		{viewDNSRecords, ""},
		{viewBucketIAM, ""},
	}
	for _, tc := range cases {
		if got := searchIndexedKind(tc.kind); got != tc.want {
			t.Errorf("searchIndexedKind(%s) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestParseSearchTerms(t *testing.T) {
	includes, excludes := parseSearchTerms("web alpha -db")
	if len(includes) != 2 || includes[0] != "web" || includes[1] != "alpha" {
		t.Errorf("includes = %v, want [web alpha]", includes)
	}
	if len(excludes) != 1 || excludes[0] != "db" {
		t.Errorf("excludes = %v, want [db]", excludes)
	}
}

func TestSearchMatchScopeAndExcludes(t *testing.T) {
	a := newFilterTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)

	hits := a.searchIndex.match("alpha")
	if len(hits) == 0 {
		t.Fatal("match(alpha) returned no hits")
	}
	for _, hit := range hits {
		if !strings.Contains(hit.text, "alpha") {
			t.Errorf("hit %q does not contain alpha", hit.text)
		}
	}

	hits = a.searchIndex.match("beta")
	if len(hits) != 1 {
		t.Fatalf("match(beta) = %d hits, want 1", len(hits))
	}
	if hits[0].project != "p2" {
		t.Errorf("hit project = %q, want p2", hits[0].project)
	}

	hits = a.searchIndex.match("alpha -bucket")
	for _, hit := range hits {
		if strings.Contains(hit.text, "bucket") {
			t.Errorf("excluded term matched: %q", hit.text)
		}
	}
}

func TestSearchOpenClosePreservesQuery(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)

	a.openSearch()
	a.searchInput.SetText("alpha")
	if !a.searchOpen.Load() {
		t.Fatal("searchOpen not set after openSearch")
	}
	if a.searchInput.GetText() != "alpha" {
		t.Fatalf("input text = %q, want alpha", a.searchInput.GetText())
	}
	if got := len(a.searchIndex.match("alpha")); got == 0 {
		t.Error("no hits for seeded query")
	}
	a.closeSearch()
	if a.searchOpen.Load() {
		t.Fatal("searchOpen still set after closeSearch")
	}
	if a.searchQuery != "alpha" {
		t.Fatalf("searchQuery = %q, want alpha", a.searchQuery)
	}
	a.openSearch()
	if a.searchInput.GetText() != "alpha" {
		t.Fatalf("reopened input text = %q, want alpha", a.searchInput.GetText())
	}
	a.closeSearch()
}

func TestSearchPanelGroupsResultsByKind(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	if len(a.searchGroupRows) == 0 {
		t.Fatal("no grouped rows after search")
	}
	var headers int
	for _, g := range a.searchGroupRows {
		if g.header {
			headers++
		}
	}
	if headers < 2 {
		t.Fatalf("group headers = %d, want at least 2 (vms + buckets)", headers)
	}
	if got := a.searchResults.GetRowCount() - 1; got != len(a.searchGroupRows) {
		t.Fatalf("table rows = %d, want %d", got, len(a.searchGroupRows))
	}
	cell := a.searchResults.GetCell(1, 0)
	if !strings.Contains(cell.Text, "VMs (1)") {
		t.Errorf("first group header = %q, want VMs count in title", cell.Text)
	}
}

func TestSearchGroupHeaderOpensView(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)

	a.openSearch()
	a.searchInput.SetText("alpha")
	a.runSearch()

	// The first group header is the VMs section.
	appState.ProjectID = "p1"
	a.searchSelect(1)
	if appState.ViewKind != viewVMs {
		t.Errorf("ViewKind = %q, want vms", appState.ViewKind)
	}
	if !a.searchOpen.Load() {
		t.Error("panel closed by group header select, want it open")
	}
	if a.searchPanelFocus {
		t.Error("panel still focused after group header select, want main view")
	}
}

func TestSearchJumpPresetsFilterAndSelects(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)

	a.openSearch()
	a.searchInput.SetText("beta-db")
	a.runSearch()

	// Find the hit row (the row after the VMs group header).
	if len(a.searchGroupRows) < 2 {
		t.Fatalf("searchGroupRows = %d, want at least 2", len(a.searchGroupRows))
	}
	a.searchSelect(2)
	if a.searchOpen.Load() {
		t.Error("search still open after jump")
	}
	if appState.ProjectID != "p2" {
		t.Errorf("ProjectID = %q, want p2", appState.ProjectID)
	}
	if appState.ViewKind != viewVMs {
		t.Errorf("ViewKind = %q, want vms", appState.ViewKind)
	}
	if a.cmd.GetText() != "beta-db" {
		t.Errorf("filter text = %q, want beta-db", a.cmd.GetText())
	}
	if len(a.visibleRows) != 1 {
		t.Errorf("visibleRows = %d, want 1", len(a.visibleRows))
	}
	if row, _ := a.info.GetSelection(); row != 1 {
		t.Errorf("selected row = %d, want 1", row)
	}
}

func TestSearchTabTogglesFocus(t *testing.T) {
	a := newSearchTestApp(t)
	seedSearchRows(t, a)
	buildSearchIndexSync(t, a)

	a.openSearch()
	if !a.searchPanelFocus {
		t.Fatal("panel focus not set after openSearch")
	}
	a.toggleSearchFocus()
	if a.searchPanelFocus {
		t.Fatal("panel focus still set after toggleSearchFocus")
	}
	if !a.searchOpen.Load() {
		t.Fatal("panel closed by focus toggle")
	}
	a.toggleSearchFocus()
	if !a.searchPanelFocus {
		t.Fatal("panel focus not restored by second toggle")
	}
	a.closeSearch()
}
