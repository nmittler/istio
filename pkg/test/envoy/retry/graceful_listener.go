package retry

import (
	"fmt"
	"github.com/hashicorp/go-multierror"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type GracefulListener struct {
	// inner listener
	ln net.Listener

	// maximum wait time for graceful shutdown
	maxWaitTime time.Duration

	// this channel is closed during graceful shutdown on zero open connections.
	done chan struct{}

	// the number of open connections
	openConnCount uint64

	connections []*gracefulConn

	mutex sync.Mutex

	// becomes non-zero when graceful shutdown starts
	shutdown uint64

	closed bool
}

// NewGracefulListener wraps the given listener into 'graceful shutdown' listener.
func NewGracefulListener(ln net.Listener, maxWaitTime time.Duration) net.Listener {
	return &GracefulListener{
		ln:          ln,
		maxWaitTime: maxWaitTime,
		done:        make(chan struct{}),
	}
}

func (ln *GracefulListener) Accept() (net.Conn, error) {
	c, err := ln.ln.Accept()

	if err != nil {
		return nil, err
	}

	ln.mutex.Lock()
	defer ln.mutex.Unlock()

	conn := &gracefulConn{
		Conn: c,
		ln:   ln,
	}

	atomic.AddUint64(&ln.openConnCount, 1)
	ln.connections = append(ln.connections, conn)
	return conn, nil
}

func (ln *GracefulListener) Addr() net.Addr {
	return ln.ln.Addr()
}

// Close closes the inner listener and waits until all the pending open connections
// are closed before returning.
func (ln *GracefulListener) Close() (err error) {
	if ln.closed {
		return nil
	}

	ln.closed = true
	err = multierror.Append(err, ln.ln.Close()).ErrorOrNil()

	if e := ln.waitForZeroConns(); e != nil {
		err = multierror.Append(err, e)
		ln.forceCloseConns()
	}
	return err
}

func (ln *GracefulListener) waitForZeroConns() error {
	atomic.AddUint64(&ln.shutdown, 1)

	if atomic.LoadUint64(&ln.openConnCount) == 0 {
		close(ln.done)
		return nil
	}

	select {
	case <-ln.done:
		return nil
	case <-time.After(ln.maxWaitTime):
		return fmt.Errorf("cannot complete graceful shutdown in %s", ln.maxWaitTime)
	}

	return nil
}

func (ln *GracefulListener) forceCloseConns() {
	ln.mutex.Lock()
	conns := ln.connections
	ln.connections = make([]*gracefulConn, 0)
	ln.mutex.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (ln *GracefulListener) onConnectionClosed() {
	atomic.AddUint64(&ln.openConnCount, ^uint64(0))
}

type gracefulConn struct {
	net.Conn
	ln     *GracefulListener
	closed bool
	mutex  sync.Mutex
}

func (c *gracefulConn) Close() error {
	c.mutex.Lock()
	c.mutex.Unlock()
	if !c.closed {
		c.closed = true
		err := c.Conn.Close()
		c.ln.onConnectionClosed()
		return err
	}
	return nil
}
