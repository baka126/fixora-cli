package shadow

func validateStartupSpec(spec map[string]any) []string {
	containers := sliceMaps(spec["containers"])
	if len(containers) != 1 {
		return []string{"startup restoration requires exactly one container"}
	}
	c := containers[0]
	var reasons []string
	for key := range c {
		if key != "name" && key != "command" && key != "args" {
			reasons = append(reasons, "startup restoration may change only command and args")
		}
	}
	command, ok := c["command"].([]any)
	if !ok || len(command) == 0 {
		reasons = append(reasons, "startup restoration requires a command array")
	}
	for _, key := range []string{"command", "args"} {
		raw, exists := c[key]
		if !exists {
			continue
		}
		values, ok := raw.([]any)
		if !ok {
			reasons = append(reasons, "startup "+key+" must be a string array")
			continue
		}
		for _, v := range values {
			s, ok := v.(string)
			if !ok || (key == "command" && s == "") {
				reasons = append(reasons, "startup "+key+" must contain strings")
			}
		}
	}
	return reasons
}
