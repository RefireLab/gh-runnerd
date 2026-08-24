package guest

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// AgentConfig is the in-VM guest agent.
type AgentConfig struct {
	HostIP      string
	Port        int
	RunnerDir   string
	Hostname    string
	ConnectWait time.Duration
	Dial        func() (net.Conn, error)
	// RunnerStart launches the actions runner with the JIT config and
	// blocks until it exits. It must call onJobStart as soon as the runner
	// reports that it picked up a workflow job.
	RunnerStart func(encoded string, onJobStart func()) (int, error)
}

func defaults(cfg AgentConfig) AgentConfig {
	if cfg.HostIP == "" {
		cfg.HostIP = "10.87.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.RunnerDir == "" {
		cfg.RunnerDir = "/opt/actions-runner"
	}
	if cfg.Hostname == "" {
		h, _ := os.Hostname()
		cfg.Hostname = h
	}
	if cfg.ConnectWait == 0 {
		cfg.ConnectWait = 2 * time.Minute
	}
	if cfg.Dial == nil {
		cfg.Dial = func() (net.Conn, error) {
			return dialHost(cfg.HostIP, cfg.Port)
		}
	}
	if cfg.RunnerStart == nil {
		cfg.RunnerStart = func(encoded string, onJobStart func()) (int, error) {
			cmd := exec.Command("./run.sh", "--jitconfig", encoded)
			cmd.Dir = cfg.RunnerDir
			cmd.Stderr = os.Stderr
			cmd.Env = append(os.Environ(), "RUNNER_ALLOW_RUNASROOT=1")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				return 1, err
			}
			if err := cmd.Start(); err != nil {
				return 1, err
			}
			watchRunnerOutput(stdout, os.Stdout, onJobStart)
			err = cmd.Wait()
			if err == nil {
				return 0, nil
			}
			if ee, ok := err.(*exec.ExitError); ok {
				return ee.ExitCode(), nil
			}
			return 1, err
		}
	}
	return cfg
}

// watchRunnerOutput mirrors the runner's stdout to sink while watching for
// the "Running job:" line the runner prints when it takes a workflow job.
func watchRunnerOutput(r io.Reader, sink io.Writer, onJobStart func()) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			_, _ = io.WriteString(sink, line)
			if onJobStart != nil && strings.Contains(line, "Running job:") {
				onJobStart()
			}
		}
		if err != nil {
			return
		}
	}
}

// RunAgent is the guest-side control loop.
func RunAgent(cfg AgentConfig) error {
	cfg = defaults(cfg)
	deadline := time.Now().Add(cfg.ConnectWait)
	var raw net.Conn
	var err error
	for time.Now().Before(deadline) {
		raw, err = cfg.Dial()
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if raw == nil {
		return fmt.Errorf("guest agent could not reach gh-runnerd host: %w", err)
	}
	defer raw.Close()
	c := NewConn(raw)
	if err := c.Send(Message{Type: KindHello, Hostname: cfg.Hostname, IP: cfg.HostIP}); err != nil {
		return err
	}
	msg, err := c.Recv()
	if err != nil {
		return err
	}
	if msg.Type != KindJIT || msg.Encoded == "" {
		return fmt.Errorf("expected jit message, got %s", msg.Type)
	}
	_ = c.Send(Message{Type: KindJobStarted})
	var once sync.Once
	onJobStart := func() {
		once.Do(func() { _ = c.Send(Message{Type: KindJobActive}) })
	}
	code, runErr := cfg.RunnerStart(msg.Encoded, onJobStart)
	_ = c.Send(Message{Type: KindJobFinished, ExitCode: code, Text: errText(runErr)})
	return runErr
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
