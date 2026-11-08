package serviceprobe

import (
	"context"
	"net"
	"time"
)

const IsolationListenerPort = 19090

// IsolationListener is a fixed-purpose TCP responder used only by shadow
// NetworkPolicy controls. It returns one byte and exposes no application data.
type IsolationListener struct {
	listener net.Listener
	done     chan error
}

func StartIsolationListener(ctx context.Context, address string) (*IsolationListener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server := &IsolationListener{listener: listener, done: make(chan error, 1)}
	go func() {
		go func() {
			<-ctx.Done()
			_ = listener.Close()
		}()
		for {
			connection, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					server.done <- nil
				} else {
					server.done <- err
				}
				return
			}
			_ = connection.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = connection.Write([]byte{0x46})
			_ = connection.Close()
		}
	}()
	return server, nil
}

func (l *IsolationListener) Addr() net.Addr { return l.listener.Addr() }

func (l *IsolationListener) Wait() error { return <-l.done }
