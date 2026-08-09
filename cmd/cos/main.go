package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Lucky3028/cos/internal/appserver"
	"github.com/Lucky3028/cos/internal/domain"
	"github.com/Lucky3028/cos/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: cos [options]\n\nBrowse, resume, and delete Codex sessions.\n\nOptions:\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *showVersion {
		fmt.Printf("cos %s\n", appserver.Version)
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

	store := appserver.NewDefaultStore()
	defer func() { _ = store.Close() }()
	program := tea.NewProgram(tui.NewModel(store, cwd), tea.WithAltScreen(), tea.WithMouseCellMotion())
	finalModel, err := program.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cos: %v\n", err)
		os.Exit(1)
	}
	final, ok := finalModel.(tui.Model)
	if !ok {
		return
	}
	resumeSession, resumeRequested := final.ResumeSession()
	if !resumeRequested {
		return
	}
	if err := store.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "cos: close app-server before resume: %v\n", err)
		os.Exit(1)
	}
	if err := runResume(resumeSession); err != nil {
		fmt.Fprintf(os.Stderr, "cos: resume: %v\n", err)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() >= 0 {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

func resumeArgs(thread domain.Thread) []string {
	args := []string{"resume", thread.ID}
	if thread.CWD != "" {
		args = append([]string{"--cd", thread.CWD}, args...)
	}
	return args
}

func resumeCommand(command string, thread domain.Thread) *exec.Cmd {
	cmd := exec.Command(command, resumeArgs(thread)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func runResume(thread domain.Thread) error {
	return resumeCommand("codex", thread).Run()
}
