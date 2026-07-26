package main

import (
	"fmt"
	"os"
	"path/filepath"

	"tuichat/config"
	"tuichat/tui"

	tea "charm.land/bubbletea/v2"
)

func main() {
	home, _ := os.UserHomeDir()
	configPath := filepath.Join(home, ".config", "tuichat", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = "config.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config (%s): %v\n", configPath, err)
		os.Exit(1)
	}

	p := tea.NewProgram(tui.New(cfg))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
