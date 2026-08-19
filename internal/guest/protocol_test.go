package guest

import (
	"bytes"
	"io"
	"testing"
)

type bufConn struct {
	r *bytes.Buffer
	w *bytes.Buffer
}

func (b bufConn) Read(p []byte) (int, error)  { return b.r.Read(p) }
func (b bufConn) Write(p []byte) (int, error) { return b.w.Write(p) }

func TestProtocolRoundTrip(t *testing.T) {
	t.Parallel()
	buf := &bytes.Buffer{}
	c := NewConn(bufConn{r: buf, w: buf})
	if err := c.Send(Message{Type: KindHello, Hostname: "runner-1", CID: 3}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != KindHello || got.Hostname != "runner-1" || got.CID != 3 {
		t.Fatalf("got %+v", got)
	}
	if err := c.Send(Message{Type: KindJIT, Encoded: "abc"}); err != nil {
		t.Fatal(err)
	}
	got, err = c.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got.Encoded != "abc" {
		t.Fatalf("jit %q", got.Encoded)
	}
	_, err = c.Recv()
	if err != io.EOF {
		t.Fatalf("want EOF got %v", err)
	}
}
