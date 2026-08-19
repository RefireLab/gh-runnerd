package guest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Kind is a control-channel message type.
type Kind string

const (
	KindHello       Kind = "hello"
	KindJIT         Kind = "jit"
	KindHeartbeat   Kind = "heartbeat"
	KindJobStarted  Kind = "job_started"
	KindJobFinished Kind = "job_finished"
	KindLog         Kind = "log"
	KindShutdown    Kind = "shutdown"
	KindError       Kind = "error"
)

// Message is one newline-delimited JSON frame.
type Message struct {
	Type     Kind   `json:"type"`
	Hostname string `json:"hostname,omitempty"`
	CID      uint32 `json:"cid,omitempty"`
	IP       string `json:"ip,omitempty"`
	Encoded  string `json:"encoded,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Text     string `json:"text,omitempty"`
}

// Conn is a bidirectional JSONL guest control channel.
type Conn struct {
	rw   io.ReadWriter
	mu   sync.Mutex
	scan *bufio.Scanner
}

// NewConn wraps a stream (vsock or TCP).
func NewConn(rw io.ReadWriter) *Conn {
	return &Conn{rw: rw, scan: bufio.NewScanner(rw)}
}

// Send writes one message.
func (c *Conn) Send(m Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = c.rw.Write(b)
	return err
}

// Recv reads one message.
func (c *Conn) Recv() (Message, error) {
	if !c.scan.Scan() {
		if err := c.scan.Err(); err != nil {
			return Message{}, err
		}
		return Message{}, io.EOF
	}
	var m Message
	if err := json.Unmarshal(c.scan.Bytes(), &m); err != nil {
		return Message{}, fmt.Errorf("guest protocol: %w", err)
	}
	if m.Type == "" {
		return Message{}, fmt.Errorf("guest protocol: missing type")
	}
	return m, nil
}
