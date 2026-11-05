package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

func executeNoArgs(interactive bool, in io.Reader, stdout, stderr io.Writer) int {
	if !interactive {
		printHelp(stdout)
		return 0
	}
	command := chooseStartCommand(in, stdout)
	if command == "" {
		return 0
	}
	return Execute([]string{command}, stdout, stderr)
}

func chooseStartCommand(in io.Reader, out io.Writer) string {
	if in == nil {
		in = os.Stdin
	}
	reader := bufio.NewReader(in)
	fmt.Fprintln(out, "Fixora: what would you like to do?")
	fmt.Fprintln(out, "  1) Scan for issues")
	fmt.Fprintln(out, "  2) Check cluster setup")
	fmt.Fprintln(out, "  3) Open dashboard (default)")
	for {
		fmt.Fprint(out, "Choose [1-3, q to quit]: ")
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return ""
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "1":
			return "scan"
		case "2":
			return "doctor"
		case "", "3":
			return "dashboard"
		case "q", "quit":
			return ""
		default:
			fmt.Fprintln(out, "Choose 1, 2, 3, or q.")
		}
	}
}
