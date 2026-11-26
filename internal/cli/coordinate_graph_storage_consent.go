package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/fixora/kubectl-fixora/internal/shadow"
)

const graphStorageConsentToken = "SNAPSHOT"

func confirmGraphStoragePlan(namespace string, items []shadow.GraphStorageConsentItem, in io.Reader, out io.Writer) bool {
	if namespace == "" || len(items) == 0 || in == nil || out == nil {
		return false
	}
	fmt.Fprintln(out, "Separate CSI snapshot authorization")
	fmt.Fprintln(out, "The following source PVCs will be snapshotted. This is crash-consistent; Fixora will not quiesce applications.")
	for _, item := range items {
		fmt.Fprintf(out, "  %s/%s (UID %s): request %d bytes; policy maximum %d bytes; source class %s; snapshot class %s; restore class %s\n", namespace, item.Name, item.UID, item.RequestedBytes, item.MaximumBytes, item.SourceStorageClass, item.VolumeSnapshotClass, item.RestoreStorageClass)
	}
	fmt.Fprintf(out, "Fixora will add an exact temporary ReferenceGrant in namespace %s for these run-owned restored claims and snapshots, then remove it and all snapshots during cleanup. Snapshot storage can incur provider charges.\n", namespace)
	fmt.Fprintf(out, "Type %s to authorize these storage side effects (the ordinary --yes flag does not authorize them): ", graphStorageConsentToken)
	var line string
	var err error
	if buffered, ok := in.(interface{ ReadString(byte) (string, error) }); ok {
		line, err = buffered.ReadString('\n')
	} else {
		line, err = bufio.NewReader(in).ReadString('\n')
	}
	if err != nil && strings.TrimSpace(line) == "" {
		return false
	}
	return err == nil && strings.TrimSpace(line) == graphStorageConsentToken
}
