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
	case "sarif":
		writeSARIF(w, value)
	case "junit":
		writeJUnit(w, value)
	case "prometheus", "metrics":
		writePrometheus(w, value)
	default:
		writeText(w, value)
	}
	return 0
}

func writeSARIF(w io.Writer, value any) {
	findings := extractFindings(value)
	results := []map[string]any{}
	for _, f := range findings {
		results = append(results, map[string]any{
			"ruleId":  fmt.Sprint(f["status"]),
			"level":   sarifLevel(fmt.Sprint(f["severity"])),
			"message": map[string]string{"text": fmt.Sprint(f["summary"])},
			"locations": []map[string]any{{
				"physicalLocation": map[string]any{
					"artifactLocation": map[string]string{"uri": fmt.Sprintf("kubernetes://%s/%s/%s", f["namespace"], f["resourceKind"], f["resourceName"])},
				},
			}},
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"version": "2.1.0",
		"runs": []map[string]any{{
			"tool":    map[string]any{"driver": map[string]any{"name": "fixora-cli"}},
			"results": results,
		}},
	})
}

func writeJUnit(w io.Writer, value any) {
	findings := extractFindings(value)
	fmt.Fprintf(w, "<testsuite name=\"fixora\" tests=\"%d\" failures=\"%d\">\n", len(findings), len(findings))
	for i, f := range findings {
		name := xmlEscape(fmt.Sprintf("%s/%s/%s", f["namespace"], f["resourceKind"], f["resourceName"]))
		fmt.Fprintf(w, "  <testcase classname=\"fixora\" name=\"%s\">\n", name)
		fmt.Fprintf(w, "    <failure message=\"%s\">%s</failure>\n", xmlEscape(fmt.Sprint(f["status"])), xmlEscape(fmt.Sprint(f["summary"])))
		fmt.Fprintf(w, "  </testcase>\n")
		if i > 5000 {
			break
		}
	}
	fmt.Fprintln(w, "</testsuite>")
}

func writePrometheus(w io.Writer, value any) {
	findings := extractFindings(value)
	counts := map[string]int{}
	for _, f := range findings {
		counts[fmt.Sprint(f["severity"])]++
	}
	fmt.Fprintln(w, "# HELP fixora_findings_total Fixora findings by severity")
	fmt.Fprintln(w, "# TYPE fixora_findings_total gauge")
	for severity, count := range counts {
		fmt.Fprintf(w, "fixora_findings_total{severity=%q} %d\n", severity, count)
	}
	fmt.Fprintf(w, "fixora_findings_total{severity=%q} %d\n", "all", len(findings))
}

func extractFindings(value any) []map[string]any {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	raw, _ := obj["results"].([]any)
	if raw == nil {
		raw, _ = obj["findings"].([]any)
	}
	out := []map[string]any{}
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if ok {
			out = append(out, m)
		}
	}
	return out
}

func sarifLevel(severity string) string {
	switch strings.ToLower(severity) {
	case "critical", "high":
		return "error"
	case "medium":
		return "warning"
	default:
		return "note"
	}
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return replacer.Replace(value)
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
