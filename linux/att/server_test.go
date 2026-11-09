package att_test

import (
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux/att"
)

var (
	serviceChangedIndication = []byte{0x1d, 0x03, 0x00, 0x5a, 0x00, 0xff, 0xff}
	batteryLevelNotification = []byte{0x1b, 0x2a, 0x00, 0x64}
	handleValueConfirmation  = []byte{0x1e}
)

type fakeConn struct {
	ble.Conn
	in      chan []byte
	written chan []byte
}

func newFakeConn() *fakeConn {
	return &fakeConn{in: make(chan []byte), written: make(chan []byte, 4)}
}

func (c *fakeConn) RxMTU() int {
	return ble.DefaultMTU
}

func (c *fakeConn) Read(b []byte) (int, error) {
	p, ok := <-c.in
	if !ok {
		return 0, io.EOF
	}
	return copy(b, p), nil
}

func (c *fakeConn) Write(b []byte) (int, error) {
	c.written <- append([]byte(nil), b...)
	return len(b), nil
}

func (c *fakeConn) Close() error {
	return nil
}

func TestServer_Loop(t *testing.T) {
	tests := []struct {
		name     string
		in       [][]byte
		expected []byte
	}{
		{
			name:     "confirms an indication from the peer",
			in:       [][]byte{serviceChangedIndication},
			expected: handleValueConfirmation,
		},
		{
			name:     "sends nothing back for a notification from the peer",
			in:       [][]byte{batteryLevelNotification, serviceChangedIndication},
			expected: handleValueConfirmation,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := newFakeConn()
			s, err := att.NewServer(att.NewDB(nil, 1), c)
			if err != nil {
				t.Fatalf("new server: %v", err)
			}
			go s.Loop()
			defer close(c.in)

			for _, p := range tt.in {
				c.in <- p
			}

			select {
			case got := <-c.written:
				if !bytes.Equal(got, tt.expected) {
					t.Fatalf("server wrote % X, expected % X", got, tt.expected)
				}
			case <-time.After(time.Second):
				t.Fatal("server wrote nothing")
			}
		})
	}
}
