package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

const version = "0.1.0"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: cos [options]\n\nBrowse, resume, and delete Codex sessions.\n\nOptions:\n")
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
	finalModel, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cos: %v\n", err)
		os.Exit(1)
	}
	final, ok := finalModel.(model)
	if !ok || !final.resumeRequested {
		return
	}
	if err := store.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "cos: close app-server before resume: %v\n", err)
		os.Exit(1)
	}
	if err := runResume(final.resumeSession); err != nil {
		fmt.Fprintf(os.Stderr, "cos: resume: %v\n", err)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() >= 0 {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

func resumeArgs(thread Thread) []string {
	args := []string{"resume", thread.ID}
	if thread.CWD != "" {
		args = append([]string{"--cd", thread.CWD}, args...)
	}
	return args
}

func resumeCommand(command string, thread Thread) *exec.Cmd {
	cmd := exec.Command(command, resumeArgs(thread)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func runResume(thread Thread) error {
	return resumeCommand("codex", thread).Run()
}
