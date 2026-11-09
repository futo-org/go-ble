package hci

import (
	"bytes"
	"encoding/binary"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/go-ble/ble"
	"github.com/go-ble/ble/linux/hci/evt"
)

const testConnectionHandle = 0x0010

var readByGroupTypeRequest = []byte{0x10, 0x01, 0x00, 0xff, 0xff, 0x00, 0x28}

var ignoredEventPacket = []byte{pktTypeEvent, 0xff, 0x00}

type fakeSocket struct {
	in     chan []byte
	closed chan struct{}
	once   sync.Once
}

func newFakeSocket() *fakeSocket {
	return &fakeSocket{in: make(chan []byte), closed: make(chan struct{})}
}

func (s *fakeSocket) Read(b []byte) (int, error) {
	select {
	case p := <-s.in:
		return copy(b, p), nil
	case <-s.closed:
		return 0, io.EOF
	}
}

func (s *fakeSocket) Write(b []byte) (int, error) {
	return len(b), nil
}

func (s *fakeSocket) Close() error {
	s.once.Do(func() { close(s.closed) })
	return nil
}

func newTestHCI(t *testing.T) (*HCI, *fakeSocket) {
	t.Helper()
	h, err := NewHCI()
	if err != nil {
		t.Fatalf("new hci: %v", err)
	}
	h.registerHandlers()
	h.pool = NewPool(1+4+ble.DefaultMTU, 8)
	skt := newFakeSocket()
	h.skt = skt
	go h.sktLoop()
	return h, skt
}

func aclPacket(att []byte) []byte {
	l2cap := make([]byte, 4, 4+len(att))
	binary.LittleEndian.PutUint16(l2cap[0:], uint16(len(att)))
	binary.LittleEndian.PutUint16(l2cap[2:], cidLEAtt)
	l2cap = append(l2cap, att...)

	b := make([]byte, 5, 5+len(l2cap))
	b[0] = pktTypeACLData
	binary.LittleEndian.PutUint16(b[1:], testConnectionHandle|pbfControllerToHostStart<<12)
	binary.LittleEndian.PutUint16(b[3:], uint16(len(l2cap)))
	return append(b, l2cap...)
}

func leConnectionCompletePacket() []byte {
	return []byte{
		pktTypeEvent, 0x3E, 19,
		evt.LEConnectionCompleteSubCode, 0x00,
		byte(testConnectionHandle), byte(testConnectionHandle >> 8),
		roleSlave, 0x01,
		0x31, 0x33, 0x81, 0x35, 0x36, 0x65,
		0x0f, 0x00, 0x00, 0x00, 0xf4, 0x01, 0x06,
	}
}

func writeCommand(handle byte) []byte {
	return []byte{0x52, handle, 0x00, 0xaa}
}

func accept(t *testing.T, h *HCI) ble.Conn {
	t.Helper()
	c, err := h.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return c
}

func readRequest(t *testing.T, c ble.Conn) []byte {
	t.Helper()
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, ble.DefaultMTU)
		n, err := c.Read(buf)
		if err != nil {
			got <- nil
			return
		}
		got <- buf[:n]
	}()
	select {
	case b := <-got:
		return b
	case <-time.After(time.Second):
		t.Fatal("no request reached the connection")
		return nil
	}
}

func TestHCI_RequestDelivery(t *testing.T) {
	tests := []struct {
		name           string
		isRequestFirst bool
	}{
		{name: "request before connection complete", isRequestFirst: true},
		{name: "request after connection complete", isRequestFirst: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, skt := newTestHCI(t)
			defer skt.Close()

			if tt.isRequestFirst {
				skt.in <- aclPacket(readByGroupTypeRequest)
			}
			skt.in <- leConnectionCompletePacket()
			c := accept(t, h)
			if !tt.isRequestFirst {
				skt.in <- aclPacket(readByGroupTypeRequest)
			}

			if got := readRequest(t, c); !bytes.Equal(got, readByGroupTypeRequest) {
				t.Fatalf("connection read % X, expected % X", got, readByGroupTypeRequest)
			}
		})
	}
}

func TestHCI_HoldsAtMostEightRequestsBeforeConnectionComplete(t *testing.T) {
	t.Parallel()
	h, skt := newTestHCI(t)
	defer skt.Close()

	for i := byte(0); i < 9; i++ {
		skt.in <- aclPacket(writeCommand(i))
	}
	skt.in <- leConnectionCompletePacket()
	c := accept(t, h)
	skt.in <- aclPacket(readByGroupTypeRequest)

	for i := byte(0); i < 8; i++ {
		if got := readRequest(t, c); !bytes.Equal(got, writeCommand(i)) {
			t.Fatalf("held request %d: connection read % X, expected % X", i, got, writeCommand(i))
		}
	}
	if got := readRequest(t, c); !bytes.Equal(got, readByGroupTypeRequest) {
		t.Fatalf("after held requests: connection read % X, expected % X", got, readByGroupTypeRequest)
	}
}

func TestHCI_DropsRequestHeldLongerThanHoldTime(t *testing.T) {
	t.Parallel()
	h, skt := newTestHCI(t)
	defer skt.Close()
	start := time.Unix(0, 0)
	now := start
	h.now = func() time.Time { return now }

	skt.in <- aclPacket(writeCommand(0))
	skt.in <- ignoredEventPacket
	now = start.Add(101 * time.Millisecond)
	skt.in <- leConnectionCompletePacket()
	c := accept(t, h)
	skt.in <- aclPacket(readByGroupTypeRequest)

	if got := readRequest(t, c); !bytes.Equal(got, readByGroupTypeRequest) {
		t.Fatalf("connection read % X, expected % X", got, readByGroupTypeRequest)
	}
}
