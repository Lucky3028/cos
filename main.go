package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

const version = "0.1.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: cos [options]\n\nCodex Session Organizer — browse and delete Codex sessions.\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Printf("cos %s\n", version)
		return
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cos: get current directory: %v\n", err)
		os.Exit(1)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cos: resolve current directory: %v\n", err)
		os.Exit(1)
	}

	store := NewDefaultStore()
	defer func() { _ = store.Close() }()
	program := tea.NewProgram(newModel(store, cwd), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := program.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "cos: %v\n", err)
		os.Exit(1)
	}
}
