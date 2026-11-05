package serviceprobe

import (
	"context"
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

func TestIsolationListenerAcceptsTCPAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := StartIsolationListener(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	cancel()
	if err := listener.Wait(); err != nil {
		t.Fatal(err)
	}
}
