package termui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

type StepChoice int

const (
	StepContinue StepChoice = iota
	StepShowProof
	StepQuit
)

func readLine(in io.Reader) (string, bool) {
	if in == nil {
		in = os.Stdin
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(line)), true
}

func PromptStep(prompt string, in io.Reader, out io.Writer) StepChoice {
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintf(out, "\n%s\n  [Enter] continue · [w] show proof · [q] quit: ", prompt)
	resp, ok := readLine(in)
	if !ok {
		return StepQuit
	}
	switch resp {
	case "w":
		return StepShowProof
	case "q":
		return StepQuit
	default:
		return StepContinue
	}
}

type DeliveryChoice int

const (
	DeliverCancel DeliveryChoice = iota
	DeliverCluster
	DeliverPR
	DeliverPatch
)

// PromptDelivery asks how to deliver a fix. When reviewOnly is set, direct
// cluster apply is neither offered nor implied as "safe to ship" — only PR and
// patch-file delivery are presented.
func PromptDelivery(in io.Reader, out io.Writer, reviewOnly bool) DeliveryChoice {
	if out == nil {
		out = os.Stdout
	}
	if reviewOnly {
		fmt.Fprintln(out, "\nThis patch is review-only (not shadow-verified). How do you want to deliver it?")
		fmt.Fprintln(out, "  1) Open a GitHub/GitLab PR")
		fmt.Fprintln(out, "  2) Write patch file only  (default)")
		fmt.Fprint(out, "Choose [1-2]: ")
		resp, ok := readLine(in)
		if !ok {
			return DeliverCancel
		}
		switch resp {
		case "1":
			return DeliverPR
		case "2", "":
			return DeliverPatch
		default:
			return DeliverCancel
		}
	}
	fmt.Fprintln(out, "\nThis fix is verified and safe to ship. How do you want to deliver it?")
	fmt.Fprintln(out, "  1) Apply directly to the cluster")
	fmt.Fprintln(out, "  2) Open a GitHub/GitLab PR")
	fmt.Fprintln(out, "  3) Write patch file only  (default)")
	fmt.Fprint(out, "Choose [1-3]: ")
	resp, ok := readLine(in)
	if !ok {
		return DeliverCancel
	}
	switch resp {
	case "1":
		return DeliverCluster
	case "2":
		return DeliverPR
	case "3", "":
		return DeliverPatch
	default:
		return DeliverCancel
	}
}

func ConfirmShadowOrEdit(in io.Reader, out io.Writer) (bool, bool) {
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprint(out, "\nValidate this in a shadow sandbox? [Y/n]  ([e] edit patch): ")
	resp, ok := readLine(in)
	if !ok {
		return false, false
	}
	switch resp {
	case "e":
		return true, true
	case "n", "no":
		return false, false
	default:
		return true, false
	}
}

// ConfirmProceedOrEdit mirrors ConfirmShadowOrEdit but without shadow-specific
// wording, for use when shadow verification is disabled (--no-shadow/--quick).
func ConfirmProceedOrEdit(in io.Reader, out io.Writer) (bool, bool) {
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprint(out, "\nProceed with this fix? [Y/n]  ([e] edit patch): ")
	resp, ok := readLine(in)
	if !ok {
		return false, false
	}
	switch resp {
	case "e":
		return true, true
	case "n", "no":
		return false, false
	default:
		return true, false
	}
}
