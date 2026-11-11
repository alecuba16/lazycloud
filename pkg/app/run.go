package app

import (
	"errors"
	"fmt"
	"os"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/elferherrera/lazycloud/cmd/providers"
)

func checkNestedExecution() error {
	if os.Getenv("LAZYCLOUD_CONTEXT") != "true" {
		return nil
	}
	return errors.New("Detected nested lazycloud execution. Exit this context to start lazycloud")
}

func Run() error {
	if err := checkNestedExecution(); err != nil {
		return err
	}
	tview.Borders.HorizontalFocus = tview.Borders.Horizontal
	tview.Borders.VerticalFocus = tview.Borders.Vertical
	tview.Borders.TopLeftFocus = tview.Borders.TopLeft
	tview.Borders.TopRightFocus = tview.Borders.TopRight
	tview.Borders.BottomLeftFocus = tview.Borders.BottomLeft
	tview.Borders.BottomRightFocus = tview.Borders.BottomRight

	appState.CmdMode = modeCmd
	appState.ViewKind = viewProjects

	cache, err := providers.NewCache()
	if err != nil {
		return fmt.Errorf("opening cache: %w", err)
	}
	if err := cache.SeedViewUpdates(viewKindNames()); err != nil {
		return fmt.Errorf("seeding view updates: %w", err)
	}
	defer cache.Close()

	logger, err := providers.NewLogger()
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer logger.Close()

	config, err := providers.LoadConfig(defaultThemeConfig(), defaultShortcutConfig(), defaultAutoUpdateConfig(), defaultInfobarConfig())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	applyShortcutConfig(config.Shortcuts)
	applyTheme(config.Theme)
	applyAutoUpdateConfig(config.AutoUpdate)
	infobarEnabled = config.Infobar.ShowShortcuts
	infobarSortOrder = config.Infobar.SortOrder

	lastProject, err := providers.LoadLastProject()
	if err != nil {
		return fmt.Errorf("loading last project: %w", err)
	}
	if lastProject.ProjectID != "" {
		appState.ProjectID = lastProject.ProjectID
		appState.Name = lastProject.Name
		appState.ProjectNumber = lastProject.ProjectNumber
	}

	tapp := tview.NewApplication()

	a := &app{
		tapp:             tapp,
		cmd:              tview.NewInputField(),
		info:             newInfoPanel(),
		summary:          newSummaryPanel(),
		status:           tview.NewTextView().SetDynamicColors(true).SetTextColor(theme.statusTextColor()),
		pages:            tview.NewPages(),
		infobar:          newInfobarPanel(),
		cache:            cache,
		logger:           logger,
		autoFetched:      map[string]bool{},
		staleAutoRefresh: config.AutoUpdate.Enabled,
	}

	a.info.SetBorder(true)

	shortcutsTable := newShortcutsPanel()
	helpOverlay, helpRow := newHelpOverlay(shortcutsTable)
	a.shortcuts = shortcutsTable
	a.helpRow = helpRow
	a.helpColumn = helpOverlay

	a.shortcutsSummary = newShortcutsSummaryPanel()

	a.buildMainLayout(helpOverlay)

	a.cmd.SetDoneFunc(a.done)
	a.cmd.SetChangedFunc(a.changed)
	a.cmd.SetFieldStyle(tcell.StyleDefault.Foreground(tview.Styles.PrimaryTextColor))
	a.cmd.SetFocusFunc(func() {
		a.mainView.ResizeItem(a.cmd, 3, 1)
	})
	a.cmd.SetBlurFunc(func() {
		a.mainView.ResizeItem(a.cmd, 0, 0)
	})

	a.info.SetSelectedFunc(a.selected)

	renderSummary(a.summary)
	a.setView(viewProjects)
	a.statusText("help ?")

	tapp.SetRoot(a.pages, true)
	tapp.SetInputCapture(a.inputCapture)
	tapp.SetFocus(a.info)
	tapp.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		a.screen = screen
		return false
	})

	a.uiRunning.Store(true)
	return tapp.Run()
}
