package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/Lucky3028/cos/internal/domain"
)

func TestResumeCommandUsesSavedCWDAndSessionID(t *testing.T) {
	thread := domain.Thread{CWD: "/saved/work", ID: "session-123"}
	cmd := resumeCommand("codex", thread)
	if !reflect.DeepEqual(cmd.Args, []string{"codex", "--cd", "/saved/work", "resume", "session-123"}) {
		t.Fatalf("args = %#v", cmd.Args)
	}
	if cmd.Stdin != os.Stdin || cmd.Stdout != os.Stdout || cmd.Stderr != os.Stderr {
		t.Fatal("resume command did not inherit standard streams")
	}
}

func TestResumeCommandOmitsEmptyCWD(t *testing.T) {
	cmd := resumeCommand("codex", domain.Thread{ID: "session-123"})
	if !reflect.DeepEqual(cmd.Args, []string{"codex", "resume", "session-123"}) {
		t.Fatalf("args = %#v", cmd.Args)
	}
}
