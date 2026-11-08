package shadow

import (
	"fmt"

	"github.com/fixora/kubectl-fixora/internal/repairpolicy"
)

// A caller cannot relabel new policy fields as a common strategy to bypass proof.
func requirePolicyForFields(req Request) error {
	obj, err := parseSinglePatch(req.Patch)
	if err != nil {
		return err
	}
	spec, ok := patchPodSpec(obj)
	if !ok {
		return fmt.Errorf("shadow candidate has no Pod spec")
	}
	required := ""
	for key := range spec {
		if key == "nodeSelector" || key == "affinity" || key == "tolerations" {
			required = "restore-placement"
		}
	}
	for _, section := range []string{"containers", "initContainers"} {
		for _, c := range sliceMaps(spec[section]) {
			for key := range c {
				switch key {
				case "command", "args":
					if required != "" && required != "restore-command" {
						return fmt.Errorf("mixed policy fields require review")
					}
					required = "restore-command"
				case "securityContext":
					if required != "" && required != "security-strengthen" {
						return fmt.Errorf("mixed policy fields require review")
					}
					required = "security-strengthen"
				}
			}
		}
	}
	if sc, ok := spec["securityContext"].(map[string]any); ok {
		for key := range sc {
			if key != "fsGroup" {
				if required != "" && required != "security-strengthen" {
					return fmt.Errorf("mixed policy fields require review")
				}
				required = "security-strengthen"
			}
		}
	}
	if required != "" && (req.Plan.Strategy != required || req.Plan.PolicyProof == nil || req.Plan.PolicyProof.Family() != required) {
		return fmt.Errorf("matching in-memory policy proof is required for %s fields", required)
	}
	if repairpolicy.Dedicated(req.Plan.Strategy) && required != req.Plan.Strategy {
		return fmt.Errorf("policy strategy does not match candidate fields")
	}
	return nil
}
