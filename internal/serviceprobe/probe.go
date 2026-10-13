package serviceprobe

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxWireBytes = 64 << 10
const MaxTargets = 130

func Validate(req Request) error {
	raw, err := hex.DecodeString(req.Nonce)
	if err != nil || len(raw) != 16 || len(req.Nonce) != 32 {
		return errors.New("bound verification nonce required")
	}
	if len(req.Targets) == 0 || len(req.Targets) > MaxTargets {
		return errors.New("complete bounded verification targets required")
	}
	seen := map[string]bool{}
	for _, t := range req.Targets {
		addr, err := netip.ParseAddr(t.Address)
		if err != nil || addr.Zone() != "" || addr.IsUnspecified() || addr.IsMulticast() || addr.String() != t.Address || t.Port < 1 || t.Port > 65535 || len(t.PodUID) > 128 {
			return errors.New("literal IP and valid port required")
		}
		key := net.JoinHostPort(t.Address, strconv.Itoa(t.Port))
		if seen[key] {
			return errors.New("duplicate verification destination")
		}
		seen[key] = true
	}
	c := req.Contract
	if c.Timeout < time.Second || c.Timeout > 10*time.Second {
		return errors.New("bounded verification timeout required")
	}
	switch c.Method {
	case "tcp":
		if c.Path != "" || c.ExpectedStatus != 0 {
			return errors.New("TCP contract cannot include HTTP fields")
		}
	case "http":
		u, err := url.ParseRequestURI(c.Path)
		if err != nil || !strings.HasPrefix(c.Path, "/") || strings.HasPrefix(c.Path, "//") || strings.Contains(c.Path, "#") || u.IsAbs() || u.Host != "" || u.RawQuery != "" || c.ExpectedStatus < 200 || c.ExpectedStatus > 399 {
			return errors.New("safe explicit HTTP contract required")
		}
	default:
		return errors.New("unsupported verification contract")
	}
	return nil
}
func Run(ctx context.Context, req Request) (Result, error) {
	result := Result{Nonce: req.Nonce}
	if err := Validate(req); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	failed := false
	for _, target := range req.Targets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		check := Check{Target: target}
		address := net.JoinHostPort(target.Address, strconv.Itoa(target.Port))
		c := req.Contract
		switch c.Method {
		case "tcp":
			dialer := net.Dialer{Timeout: c.Timeout}
			connection, err := dialer.DialContext(ctx, "tcp", address)
			if err != nil {
				check.Error = "TCP connection failed"
			} else {
				connection.Close()
				check.Success = true
			}
		case "http":
			transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: c.Timeout}).DialContext, DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: MaxWireBytes, ResponseHeaderTimeout: c.Timeout}
			client := &http.Client{Transport: transport, Timeout: c.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
			endpoint, _ := url.ParseRequestURI(c.Path)
			endpoint.Scheme = "http"
			endpoint.Host = address
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
			if err != nil {
				check.Error = "HTTP request invalid"
			} else {
				response, err := client.Do(request)
				if err != nil {
					check.Error = "HTTP verification failed"
				} else {
					check.Status = response.StatusCode
					_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, MaxWireBytes))
					response.Body.Close()
					if readErr != nil {
						check.Error = "HTTP response read failed"
					} else if check.Status != c.ExpectedStatus {
						check.Error = "HTTP status did not match contract"
					} else {
						check.Success = true
					}
				}
			}
			transport.CloseIdleConnections()
		}
		if !check.Success {
			failed = true
		}
		result.Checks = append(result.Checks, check)
	}
	if failed {
		return result, errors.New("not every destination satisfied the verification contract")
	}
	return result, nil
}
