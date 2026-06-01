package termui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func ConfirmApply(patch string) bool {
	fmt.Println(patch)
	fmt.Print("Would you like to apply this fix? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(response)
	response = strings.ToLower(response)
	if response == "y" {
		return true
	}
	return false
}
