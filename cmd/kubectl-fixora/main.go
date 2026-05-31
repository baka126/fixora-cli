package main

import (
	"os"

	"github.com/fixora/kubectl-fixora/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
