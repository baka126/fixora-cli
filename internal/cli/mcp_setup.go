package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type mcpSetupOptions struct {
	scope     string
	context   string
	namespace string
	apply     bool
}

// runMCPSetup prints or applies a host-native Fixora MCP registration.
func runMCPSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		printMCPSetupHelp(stdout)
		return 0
	}
	host := strings.ToLower(strings.TrimSpace(args[0]))
	if host != "claude-code" && host != "gemini-cli" {
		fmt.Fprintf(stderr, "error: unsupported MCP host %q (choose claude-code or gemini-cli)\n", args[0])
		return 2
	}
	for _, arg := range args[1:] {
		if arg == "--help" || arg == "-h" || arg == "help" {
			printMCPSetupHelp(stdout)
			return 0
		}
	}
	opts, err := parseMCPSetupOptions(args[1:])
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	fixoraPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "error: locate Fixora executable: %v\n", err)
		return 1
	}
	fixoraPath, err = filepath.Abs(fixoraPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: resolve Fixora executable path: %v\n", err)
		return 1
	}
	cliName := "claude"
	if host == "gemini-cli" {
		cliName = "gemini"
	}
	cliArgs := mcpHostArgs(host, opts, fixoraPath)
	if !opts.apply {
		fmt.Fprintf(stdout, "Add Fixora to %s (%s scope):\n\n  %s\n\nThis uses %s's own sign-in. Fixora does not receive or store its subscription credentials.\n", displayHost(host), opts.scope, shellCommand(cliName, cliArgs), displayHost(host))
		fmt.Fprintln(stdout, "Run this command to update the host configuration, or repeat with --apply.")
		return 0
	}
	path, err := exec.LookPath(cliName)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s CLI was not found on PATH; install it or run this command after installing it:\n  %s\n", displayHost(host), shellCommand(cliName, cliArgs))
		return 1
	}
	cmd := exec.CommandContext(context.Background(), path, cliArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if code := exitErr.ExitCode(); code >= 0 {
				return code
			}
			return 1
		}
		fmt.Fprintf(stderr, "error: could not configure %s MCP server: %v\n", displayHost(host), err)
		return 1
	}
	fmt.Fprintf(stdout, "Fixora MCP server added to %s (%s scope).\n", displayHost(host), opts.scope)
	return 0
}

func parseMCPSetupOptions(args []string) (mcpSetupOptions, error) {
	opts := mcpSetupOptions{scope: "user"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--apply":
			opts.apply = true
		case "--scope":
			value, next, err := mcpOptionValue(args, i, arg)
			if err != nil {
				return opts, err
			}
			opts.scope, i = value, next
		case "--context":
			value, next, err := mcpOptionValue(args, i, arg)
			if err != nil {
				return opts, err
			}
			opts.context, i = value, next
		case "--namespace", "-n":
			value, next, err := mcpOptionValue(args, i, arg)
			if err != nil {
				return opts, err
			}
			opts.namespace, i = value, next
		case "--help", "-h":
			return opts, errors.New("help must be requested before selecting a host")
		default:
			return opts, fmt.Errorf("unknown option %q", arg)
		}
	}
	if opts.scope != "user" && opts.scope != "project" {
		return opts, fmt.Errorf("--scope must be user or project")
	}
	return opts, nil
}

func mcpOptionValue(args []string, index int, name string) (string, int, error) {
	if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") || strings.TrimSpace(args[index+1]) == "" {
		return "", index, fmt.Errorf("%s requires a value", name)
	}
	return args[index+1], index + 1, nil
}

func mcpHostArgs(host string, opts mcpSetupOptions, fixoraPath string) []string {
	serveArgs := []string{"serve", "--mcp"}
	if opts.context != "" {
		serveArgs = append(serveArgs, "--context", opts.context)
	}
	if opts.namespace != "" {
		serveArgs = append(serveArgs, "--namespace", opts.namespace)
	}
	if host == "claude-code" {
		return append([]string{"mcp", "add", "--transport", "stdio", "--scope", opts.scope, "fixora", "--", fixoraPath}, serveArgs...)
	}
	args := []string{"mcp", "add", "--scope", opts.scope, "fixora", fixoraPath}
	args = append(args, serveArgs[0])
	args = append(args, "--")
	return append(args, serveArgs[1:]...)
}

func shellCommand(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(command))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:@%+=,-", r))
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func displayHost(host string) string {
	if host == "claude-code" {
		return "Claude Code"
	}
	return "Gemini CLI"
}

func printMCPSetupHelp(w io.Writer) {
	fmt.Fprintln(w, `Usage: kubectl fixora mcp setup <claude-code|gemini-cli> [--scope user|project] [--context NAME] [-n NAMESPACE] [--apply]

Print a host-native command to register Fixora's local MCP server. Setup only
prints by default; --apply runs the selected CLI and updates its MCP config.
The host uses its own sign-in. Fixora never reads its subscription token.

Examples:
  kubectl fixora mcp setup claude-code
  kubectl fixora mcp setup gemini-cli --context staging -n payments --apply`)
}
