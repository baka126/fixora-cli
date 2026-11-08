package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
func run(args []string, in io.Reader, out, errout io.Writer) int {
	flags := flag.NewFlagSet("fixora-service-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rawArg := flags.String("request-json", "", "bound verifier request")
	listen := flags.Bool("listen", false, "run the fixed TCP isolation-control responder")
	tcpListenArg := flags.String("tcp-listen", "", "listen on a local TCP port as a fixed nonce responder")
	responseNonceArg := flags.String("response-nonce", "", "fixed response nonce for --tcp-listen")
	if flags.Parse(args) != nil || len(flags.Args()) != 0 {
		fmt.Fprintln(errout, "invalid verifier arguments")
		return 2
	}
	if *listen {
		if *rawArg != "" || *tcpListenArg != "" || *responseNonceArg != "" {
			fmt.Fprintln(errout, "isolation listener mode cannot include verifier or nonce arguments")
			return 2
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		listener, err := serviceprobe.StartIsolationListener(ctx, fmt.Sprintf(":%d", serviceprobe.IsolationListenerPort))
		if err != nil {
			fmt.Fprintln(errout, "isolation listener could not start")
			return 1
		}
		if listener.Wait() != nil {
			fmt.Fprintln(errout, "isolation listener stopped unexpectedly")
			return 1
		}
		return 0
	}
	if *tcpListenArg != "" || *responseNonceArg != "" {
		if *tcpListenArg == "" || *rawArg != "" || !validListenAddress(*tcpListenArg) || !validResponseNonce(*responseNonceArg) {
			fmt.Fprintln(errout, "invalid TCP responder arguments")
			return 2
		}
		listener, err := net.Listen("tcp", *tcpListenArg)
		if err != nil {
			fmt.Fprintln(errout, "TCP responder could not bind requested port")
			return 1
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := serviceprobe.RunServer(ctx, *responseNonceArg, listener); err != nil {
			fmt.Fprintln(errout, "TCP responder stopped")
			return 1
		}
		return 0
	}
	var raw []byte
	var err error
	if *rawArg != "" {
		raw = []byte(*rawArg)
	} else {
		raw, err = io.ReadAll(io.LimitReader(in, serviceprobe.MaxWireBytes+1))
	}
	if err != nil || len(raw) > serviceprobe.MaxWireBytes {
		fmt.Fprintln(errout, "verifier input exceeds limit or is unreadable")
		return 2
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req serviceprobe.Request
	if dec.Decode(&req) != nil {
		fmt.Fprintln(errout, "invalid verifier JSON")
		return 2
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		fmt.Fprintln(errout, "one verifier request required")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	result, err := serviceprobe.Run(ctx, req)
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil || len(data) > serviceprobe.MaxWireBytes {
		fmt.Fprintln(errout, "verifier output exceeds limit")
		return 1
	}
	if _, writeErr := out.Write(append(data, '\n')); writeErr != nil {
		return 1
	}
	if err != nil {
		fmt.Fprintln(errout, "verification contract failed")
		return 1
	}
	return 0
}

func validListenAddress(address string) bool {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || (host != "" && host != "0.0.0.0" && host != "::") {
		return false
	}
	port, err := strconv.Atoi(portText)
	return err == nil && port > 0 && port <= 65535 && !strings.Contains(address, "%")
}

func validResponseNonce(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
