package serviceprobe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func requestFor(address string, port int, method string) Request {
	return Request{Nonce: strings.Repeat("a", 32), Targets: []Target{{Address: address, Port: port, PodUID: "pod-1"}}, Contract: Contract{Method: method, Path: "/ready", ExpectedStatus: 200, Timeout: time.Second}}
}
func serverTarget(t *testing.T, s *httptest.Server) (string, int) {
	t.Helper()
	host, p, err := net.SplitHostPort(s.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(p)
	return host, port
}
func TestServiceProbeChecksEveryTarget(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Error(r.URL.Path)
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	ip, port := serverTarget(t, s)
	req := requestFor(ip, port, "http")
	result, err := Run(context.Background(), req)
	if err != nil || len(result.Checks) != 1 || !result.Checks[0].Success || result.Nonce != req.Nonce {
		t.Fatal(result, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	req.Targets = append(req.Targets, Target{Address: "127.0.0.1", Port: closed, PodUID: "pod-2"})
	if _, err = Run(context.Background(), req); err == nil {
		t.Fatal("one passing replica certified whole set")
	}
	req = requestFor(ip, port, "tcp")
	req.Contract.Path = ""
	req.Contract.ExpectedStatus = 0
	if _, err = Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}
func TestServiceProbeRejectsRedirectProxyAndUnsafeInput(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://not-a-backend.invalid/private", 302)
	}))
	defer s.Close()
	ip, port := serverTarget(t, s)
	req := requestFor(ip, port, "http")
	if _, err := Run(context.Background(), req); err == nil {
		t.Fatal("redirect")
	}
	for _, bad := range []string{"host", "path", "duplicate", "nonce", "timeout"} {
		t.Run(bad, func(t *testing.T) {
			req := requestFor(ip, port, "http")
			switch bad {
			case "host":
				req.Targets[0].Address = "example.invalid"
			case "path":
				req.Contract.Path = "//other/private"
			case "duplicate":
				req.Targets = append(req.Targets, req.Targets[0])
			case "nonce":
				req.Nonce = ""
			case "timeout":
				req.Contract.Timeout = 20 * time.Second
			}
			if _, err := Run(context.Background(), req); err == nil {
				t.Fatal("unsafe request")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, req); err == nil {
		t.Fatal("cancel ignored")
	}
}

func TestServiceProbeEncodedHealthPath(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RequestURI != "/ready%20check" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	ip, port := serverTarget(t, s)
	req := requestFor(ip, port, "http")
	req.Contract.Path = "/ready%20check"
	if _, err := Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestNonceTCPProbeAcceptsMatchingResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	responseNonce := strings.Repeat("b", 32)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunServer(ctx, responseNonce, listener) }()
	addr := listener.Addr().(*net.TCPAddr)
	req := requestFor("127.0.0.1", addr.Port, "tcp")
	req.Contract.Path = ""
	req.Contract.ExpectedStatus = 0
	req.Contract.ExpectedResponseNonce = responseNonce
	result, err := Run(context.Background(), req)
	if err != nil || len(result.Checks) != 1 || !result.Checks[0].Success {
		t.Fatalf("matching nonce failed: result=%+v err=%v", result, err)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRunServerRejectsMalformedOversizedAndInvalidNonce(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
	}{
		{name: "malformed", line: "not-json\n"},
		{name: "invalid nonce", line: strings.Repeat("z", 32) + "\n"},
		{name: "oversized", line: strings.Repeat("a", MaxWireBytes+1) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			errCh := make(chan error, 1)
			go func() {
				defer server.Close()
				errCh <- serveOne(server, strings.Repeat("b", 32))
			}()
			_, _ = client.Write([]byte(tc.line))
			_ = client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			buf := make([]byte, 1)
			if n, err := client.Read(buf); n != 0 || err == nil {
				t.Fatalf("invalid request received response %q (%v)", buf[:n], err)
			}
			if err := <-errCh; err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
}

func TestRunServerContextCancellationAndListenerFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunServer(ctx, strings.Repeat("b", 32), listener) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("cancelled server returned %v", err)
	}
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := RunServer(context.Background(), strings.Repeat("b", 32), closed); err == nil {
		t.Fatal("closed listener error was ignored")
	}
}

func TestNonceTCPProbeRejectsMismatchedResponse(t *testing.T) {
	// The probe path is exercised through a local listener below; a mismatched
	// response must not be reported as an open TCP port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 33)
		_, _ = conn.Read(buf)
		_, _ = conn.Write([]byte(strings.Repeat("c", 32) + "\n"))
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	req := requestFor("127.0.0.1", port, "tcp")
	req.Contract.Path = ""
	req.Contract.ExpectedStatus = 0
	req.Contract.ExpectedResponseNonce = strings.Repeat("b", 32)
	if _, err := Run(context.Background(), req); err == nil {
		t.Fatal("mismatched response nonce was accepted")
	}
}
