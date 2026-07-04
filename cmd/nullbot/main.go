package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Bradthebrad/nullbot/internal/tui"
	"github.com/Bradthebrad/nullbot/pkg/app"
)

func main() {
	config, err := app.LoadOrInitConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	program := tea.NewProgram(
		tui.New(app.New(config)),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
