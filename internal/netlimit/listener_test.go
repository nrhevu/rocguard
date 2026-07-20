package netlimit

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

func TestListenerBlocksAcceptUntilConnectionCloses(t *testing.T) {
	base := newQueuedListener()
	listener := NewListener(base, 1)
	defer listener.Close()

	serverOne, clientOne := net.Pipe()
	defer clientOne.Close()
	serverTwo, clientTwo := net.Pipe()
	defer clientTwo.Close()
	base.connections <- serverOne
	base.connections <- serverTwo

	first, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	secondResult := make(chan net.Conn, 1)
	secondError := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			secondError <- acceptErr
			return
		}
		secondResult <- connection
	}()

	select {
	case connection := <-secondResult:
		connection.Close()
		t.Fatal("second connection was accepted before the first closed")
	case err := <-secondError:
		t.Fatalf("second accept failed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-secondResult:
		if err := second.Close(); err != nil {
			t.Fatal(err)
		}
	case err := <-secondError:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("second accept remained blocked after the first closed")
	}
}

type queuedListener struct {
	connections chan net.Conn
	done        chan struct{}
	closeOnce   sync.Once
}

func newQueuedListener() *queuedListener {
	return &queuedListener{connections: make(chan net.Conn, 2), done: make(chan struct{})}
}

func (l *queuedListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *queuedListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return nil
}

func (l *queuedListener) Addr() net.Addr { return testAddr("queued") }

type testAddr string

func (a testAddr) Network() string { return string(a) }
func (a testAddr) String() string  { return string(a) }

func TestListenerCloseUnblocksAdmission(t *testing.T) {
	base := newQueuedListener()
	listener := NewListener(base, 1)
	server, client := net.Pipe()
	defer client.Close()
	base.connections <- server
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	done := make(chan error, 1)
	go func() {
		_, acceptErr := listener.Accept()
		done <- acceptErr
	}()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("accept error = %v, want net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener close did not unblock admission")
	}
}
