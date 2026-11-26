package shadow

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Filesystem candidates may add workload-specific ephemeral mounts, but cannot
// supply persistent or host storage, alter container identity, or execute code.
func validateFilesystemSpec(spec map[string]any) []string {
	var reasons []string
	if sc, ok := nestedMap(spec, "securityContext"); ok {
		group, err := strconv.ParseInt(fmt.Sprint(sc["fsGroup"]), 10, 64)
		if len(sc) != 1 || err != nil || group <= 0 || group > 4294967294 {
			reasons = append(reasons, "filesystem patch may only set a positive fsGroup")
		}
	}
	volumes := sliceMaps(spec["volumes"])
	if len(volumes) == 0 {
		reasons = append(reasons, "filesystem patch requires an ephemeral emptyDir volume")
	}
	volumeNames := map[string]bool{}
	for _, volume := range volumes {
		name := stringValue(volume["name"])
		empty, ok := nestedMap(volume, "emptyDir")
		if name == "" || volumeNames[name] {
			reasons = append(reasons, "filesystem volume names must be non-empty and unique")
		}
		volumeNames[name] = true
		if len(volume) != 2 || !ok || len(empty) != 0 {
			reasons = append(reasons, "filesystem volumes must use emptyDir: {} only")
		}
	}
	containers := sliceMaps(spec["containers"])
	if len(containers) == 0 {
		reasons = append(reasons, "filesystem patch must mount storage into a named container")
	}
	containerNames := map[string]bool{}
	for _, c := range containers {
		name := stringValue(c["name"])
		mounts := sliceMaps(c["volumeMounts"])
		if name == "" || containerNames[name] || len(c) != 2 || len(mounts) == 0 {
			reasons = append(reasons, "filesystem patch requires unique named containers with volume mounts only")
		}
		containerNames[name] = true
		mountPaths := map[string]bool{}
		for _, mount := range mounts {
			mountName, mountPath := stringValue(mount["name"]), stringValue(mount["mountPath"])
			if len(mount) != 2 || !volumeNames[mountName] {
				reasons = append(reasons, "filesystem mount must reference a volume in the candidate")
			}
			if !safeEphemeralMountPath(mountPath) || mountPaths[mountPath] {
				reasons = append(reasons, "filesystem mount must target a unique, safe absolute application path")
			}
			mountPaths[mountPath] = true
		}
	}
	return reasons
}

func safeEphemeralMountPath(value string) bool {
	if !path.IsAbs(value) || path.Clean(value) != value || strings.ContainsAny(value, "\\\n\r\x00") || value == "/" {
		return false
	}
	for _, reserved := range []string{"/proc", "/sys", "/dev", "/etc", "/usr", "/bin", "/sbin", "/lib", "/boot", "/var/lib"} {
		if value == reserved || strings.HasPrefix(value, reserved+"/") {
			return false
		}
	}
	return true
}
