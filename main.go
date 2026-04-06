package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ana-rodrigues/wap/internal/tui"
	"github.com/ana-rodrigues/wap/internal/whatsapp"
)

func main() {
	noEmoji := flag.Bool("no-emoji", false, "Replace emoji with :shortcode: text")
	flag.Parse()

	client, err := whatsapp.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "wap: init error: %v\n", err)
		os.Exit(1)
	}

	if err := client.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "wap: connect error: %v\n", err)
		os.Exit(1)
	}

	// Start on contacts if session exists, otherwise show QR screen
	startScreen := tui.ScreenAuth
	if client.HasSession() {
		startScreen = tui.ScreenContacts
	}
	app := tui.New(client, startScreen, *noEmoji)
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "wap: %v\n", err)
		os.Exit(1)
	}
}
