package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	stashExec "github.com/stashapp/stash/pkg/exec"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/plugin/common"
	"github.com/stashapp/stash/pkg/python"
)

type rawTaskBuilder struct{}

func (*rawTaskBuilder) build(task pluginTask) Task {
	return &rawPluginTask{
		pluginTask: task,
	}
}

type rawPluginTask struct {
	pluginTask

	started   bool
	waitGroup sync.WaitGroup
	cmd       *exec.Cmd
	done      chan bool
}

func (t *rawPluginTask) Start() error {
	if t.started {
		return errors.New("task already started")
	}

	command := t.plugin.getExecCommand(t.operation)
	if len(command) == 0 {
		return fmt.Errorf("empty exec value")
	}

	var cmd *exec.Cmd
	if python.IsPythonCommand(command[0]) {
		pythonPath := t.serverConfig.GetPythonPath()
		p, err := python.Resolve(pythonPath)

		if err != nil {
			logger.Warnf("%s", err)
		} else {
			cmd = p.Command(t.ctx, command[1:])

			envVariable, _ := filepath.Abs(filepath.Dir(filepath.Dir(t.plugin.path)))
			python.AppendPythonPath(cmd, envVariable)
		}
	}

	if cmd == nil {
		// if could not find python, just use the command args as-is
		cmd = stashExec.Command(command[0], command[1:]...)
	}

	inBytes, err := json.Marshal(t.input)
	if err != nil {
		return fmt.Errorf("error marshalling plugin input: %w", err)
	}
	cmd.Stdin = bytes.NewReader(inBytes)
	cmd.WaitDelay = time.Second

	// Own these pipes: Cmd.Wait can reap the primary process without closing
	// readers before their buffered diagnostics have drained.
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("plugin stderr not available: %w", err)
	}
	defer stderrWriter.Close()

	stdout, stdoutWriter, err := os.Pipe()
	if nil != err {
		stderr.Close()
		return fmt.Errorf("plugin stdout not available: %w", err)
	}
	defer stdoutWriter.Close()
	cmd.Stderr, cmd.Stdout = stderrWriter, stdoutWriter

	t.done = make(chan bool, 1)
	if err = cmd.Start(); err != nil {
		stderr.Close()
		stdout.Close()
		return fmt.Errorf("error running plugin: %w", err)
	}
	// Only the process and its descendants should retain write handles now.
	stderrWriter.Close()
	stdoutWriter.Close()
	t.waitGroup.Add(1)

	var diagnostic stderrTail
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		defer stderr.Close()
		t.handlePluginStderr(t.plugin.Name, io.NopCloser(io.TeeReader(stderr, &diagnostic)))
		// The local line scanner may stop at an oversized line; still drain the process.
		_, _ = io.Copy(io.Discard, io.TeeReader(stderr, &diagnostic))
	}()
	t.cmd = cmd

	logger.Debugf("Plugin %s started: %s", t.plugin.Name, strings.Join(cmd.Args, " "))

	stdoutDone := make(chan struct{})
	var stdoutData []byte
	go func() {
		defer close(stdoutDone)
		defer stdout.Close()
		stdoutData, _ = io.ReadAll(stdout)
	}()

	go func() {
		defer t.waitGroup.Done()
		defer close(t.done)
		err := cmd.Wait()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		out, diag := stdoutDone, stderrDone
		for out != nil || diag != nil {
			select {
			case <-out:
				out = nil
			case <-diag:
				diag = nil
			case <-timer.C:
				// Inherited handles must not hold a completed primary task open.
				stdout.Close()
				stderr.Close()
				<-stdoutDone
				<-stderrDone
				out, diag = nil, nil
			}
		}
		if errors.Is(err, exec.ErrWaitDelay) {
			// An inherited input pipe is not a failed primary process.
			err = nil
		}
		output := t.getOutput(string(stdoutData))
		logger.Debugf("Plugin %s finished", t.plugin.Name)

		t.complete(&output, err, &diagnostic)
	}()

	t.started = true
	return nil
}

func (t *rawPluginTask) getOutput(output string) common.PluginOutput {
	// try to parse the output as a PluginOutput json. If it fails just
	// get the raw output
	ret := common.PluginOutput{}
	decodeErr := json.Unmarshal([]byte(output), &ret)

	if decodeErr != nil {
		ret.Output = &output
	}

	return ret
}

func (t *rawPluginTask) Wait() {
	t.waitGroup.Wait()
}

func (t *rawPluginTask) Stop() error {
	if t.cmd == nil {
		return nil
	}

	return t.cmd.Process.Kill()
}
