// Package process runs a single command as an argv array — never through
// a shell — piping optional stdin in and capturing stdout/stderr/exit
// code, with a timeout.
package process

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type Spec struct {
	Command string
	Args    []string
	Dir     string
	Stdin   string
	Env     map[string]string
	Timeout time.Duration
}

type Result struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
}

// Run spawns spec.Command with spec.Args directly via os/exec — no shell
// is ever invoked, so shell metacharacters in either field are never
// interpreted.
func Run(ctx context.Context, spec Spec) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir = spec.Dir

	if len(spec.Env) > 0 {
		env := os.Environ()
		for k, v := range spec.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}

	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	res := &Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		// Couldn't even start the process (bad binary, permissions, etc.)
		return nil, err
	}

	res.ExitCode = 0
	return res, nil
}
