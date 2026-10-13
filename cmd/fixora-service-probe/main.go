package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/fixora/kubectl-fixora/internal/serviceprobe"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
func run(args []string, in io.Reader, out, errout io.Writer) int {
	flags := flag.NewFlagSet("fixora-service-probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	rawArg := flags.String("request-json", "", "bound verifier request")
	if flags.Parse(args) != nil || len(flags.Args()) != 0 {
		fmt.Fprintln(errout, "invalid verifier arguments")
		return 2
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
