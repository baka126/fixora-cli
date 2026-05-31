package output

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

func WriteOrError(stdout, stderr io.Writer, format string, value any, err error) int {
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return Write(stdout, format, value)
}

func Write(w io.Writer, format string, value any) int {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			fmt.Fprintf(w, "error: %v\n", err)
			return 1
		}
		fmt.Fprintln(w, string(data))
	case "yaml":
		writeYAML(w, value, 0)
	case "markdown", "md":
		writeMarkdown(w, value)
	default:
		writeText(w, value)
	}
	return 0
}

func writeText(w io.Writer, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "%v\n", value)
		return
	}
	fmt.Fprintln(w, string(data))
}

func writeMarkdown(w io.Writer, value any) {
	fmt.Fprintln(w, "```json")
	data, _ := json.MarshalIndent(value, "", "  ")
	fmt.Fprintln(w, string(data))
	fmt.Fprintln(w, "```")
}

func writeYAML(w io.Writer, value any, indent int) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "%v\n", value)
		return
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		fmt.Fprintln(w, string(data))
		return
	}
	writeYAMLValue(w, generic, indent)
}

func writeYAMLValue(w io.Writer, value any, indent int) {
	pad := strings.Repeat(" ", indent)
	switch v := value.(type) {
	case map[string]any:
		for key, val := range v {
			if isScalar(val) {
				fmt.Fprintf(w, "%s%s: %v\n", pad, key, val)
			} else {
				fmt.Fprintf(w, "%s%s:\n", pad, key)
				writeYAMLValue(w, val, indent+2)
			}
		}
	case []any:
		for _, item := range v {
			if isScalar(item) {
				fmt.Fprintf(w, "%s- %v\n", pad, item)
			} else {
				fmt.Fprintf(w, "%s-\n", pad)
				writeYAMLValue(w, item, indent+2)
			}
		}
	default:
		fmt.Fprintf(w, "%s%v\n", pad, value)
	}
}

func isScalar(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return false
	default:
		return true
	}
}
