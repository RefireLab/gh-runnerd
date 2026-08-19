package guest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/mdlayher/vsock"
)

// AgentConfig is the in-VM guest agent.
type AgentConfig struct {
	HostIP      string
	Port        int
	RunnerDir   string
	Hostname    string
	ConnectWait time.Duration
	Dial        func() (net.Conn, error)
	RunnerStart func(encoded string) (int, error)
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
			if c, err := vsock.Dial(2, uint32(cfg.Port), nil); err == nil {
				return c, nil
			}
			return net.DialTimeout("tcp", net.JoinHostPort(cfg.HostIP, fmt.Sprintf("%d", cfg.Port)), 5*time.Second)
		}
	}
	if cfg.RunnerStart == nil {
		cfg.RunnerStart = func(encoded string) (int, error) {
			cmd := exec.Command("./run.sh", "--jitconfig", encoded)
			cmd.Dir = cfg.RunnerDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Env = append(os.Environ(), "RUNNER_ALLOW_RUNASROOT=1")
			err := cmd.Run()
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
	code, runErr := cfg.RunnerStart(msg.Encoded)
	_ = c.Send(Message{Type: KindJobFinished, ExitCode: code, Text: errText(runErr)})
	return runErr
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
