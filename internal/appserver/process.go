package appserver

import (
	"context"
	"io"
	"os/exec"
	"sync"
)

// appServerProcess owns the app-server process independently of individual
// request contexts. A canceled request closes its transport; the store can
// then establish a fresh process for the next operation.
type appServerProcess struct {
	client *rpcClient
	cmd    *exec.Cmd

	closeOnce sync.Once
	closeErr  error
	waitOnce  sync.Once
	waitErr   error
}

func (p *appServerProcess) close() error {
	if p == nil {
		return nil
	}
	p.closeOnce.Do(func() {
		clientErr := p.client.close()
		waitErr := p.wait()
		if clientErr != nil {
			p.closeErr = clientErr
		} else {
			p.closeErr = waitErr
		}
	})
	return p.closeErr
}

func (p *appServerProcess) wait() error {
	if p == nil || p.cmd == nil {
		return nil
	}
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
	})
	return p.waitErr
}

func startAppServer(_ context.Context, command string, args ...string) (*appServerProcess, error) {
	cmd := exec.Command(command, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	client := newRPCClient(stdin, stdout)
	return &appServerProcess{client: client, cmd: cmd}, nil
}
