package netlimit

import (
	"net"
	"sync"
)

// NewListener bounds accepted connections before TLS handshakes or HTTP
// handler goroutines are created. Closing a returned connection releases one
// admission slot.
func NewListener(listener net.Listener, maximum int) net.Listener {
	if maximum <= 0 {
		return listener
	}
	return &limitedListener{
		Listener: listener,
		slots:    make(chan struct{}, maximum),
		done:     make(chan struct{}),
	}
}

type limitedListener struct {
	net.Listener
	slots     chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	connection, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConnection{Conn: connection, release: func() { <-l.slots }}, nil
}

func (l *limitedListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.done)
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}

type limitedConnection struct {
	net.Conn
	closeOnce sync.Once
	closeErr  error
	release   func()
}

func (c *limitedConnection) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		c.release()
	})
	return c.closeErr
}
