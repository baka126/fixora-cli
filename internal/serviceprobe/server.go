package serviceprobe

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"time"
)

const maxNonceLine = 33

// RunServer serves the fixed response nonce to clients that present a valid
// 128-bit request nonce. It accepts no user-defined commands or destinations.
func RunServer(ctx context.Context, responseNonce string, listener net.Listener) error {
	if listener == nil || !validNonce(responseNonce) {
		return errors.New("bound response nonce and listener required")
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	defer listener.Close()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("TCP responder listener failed")
		}
		_ = serveOne(conn, responseNonce)
		_ = conn.Close()
	}
}

func serveOne(conn net.Conn, responseNonce string) error {
	if conn == nil || !validNonce(responseNonce) {
		return errors.New("bound request and response nonce required")
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	request, err := readNonceLine(conn)
	if err != nil || !validNonce(request) {
		return errors.New("invalid bounded nonce request")
	}
	if _, err := io.WriteString(conn, responseNonce+"\n"); err != nil {
		return errors.New("TCP responder write failed")
	}
	return nil
}

func validNonce(value string) bool {
	if len(value) != 32 {
		return false
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 16
}

func readNonceLine(reader io.Reader) (string, error) {
	buf := make([]byte, 0, maxNonceLine)
	var one [1]byte
	for len(buf) < maxNonceLine {
		n, err := reader.Read(one[:])
		if n > 0 {
			if one[0] == '\n' {
				if len(buf) != 32 {
					return "", errors.New("invalid nonce line length")
				}
				return string(buf), nil
			}
			buf = append(buf, one[0])
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("nonce line exceeds limit")
}
