package mcp

import (
	"io"
	"os"
	"os/exec"
	"sync"
)

// Runner abstracts a stdio-transport MCP server process. Start launches it
// and returns its stdin (the client's write side), stdout, and stderr as
// plain streams. This is the one real abstraction seam: unit tests substitute
// in-memory pipes instead of subprocesses.
type Runner interface {
	Start() (stdin io.WriteCloser, stdout io.Reader, stderr io.Reader, err error)
}

// waiter is implemented by execRunner so Close can reap the process.
type waiter interface {
	Wait() error
}

type execRunner struct {
	command string
	args    []string
	env     []string

	mu  sync.Mutex
	cmd *exec.Cmd
}

// NewStdioRunner returns a Runner that spawns `command` with `args` and the
// given "K=V" env additions merged over os.Environ().
func NewStdioRunner(command string, args []string, env []string) Runner {
	return &execRunner{command: command, args: args, env: env}
}

func (r *execRunner) Start() (io.WriteCloser, io.Reader, io.Reader, error) {
	cmd := exec.Command(r.command, r.args...)
	cmd.Env = append(os.Environ(), r.env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}
	r.mu.Lock()
	r.cmd = cmd
	r.mu.Unlock()
	return stdin, stdout, stderr, nil
}

func (r *execRunner) Wait() error {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()
	if cmd == nil {
		return nil
	}
	return cmd.Wait()
}
