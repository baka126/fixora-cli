# Task Status

## Advanced TUI Features (Completed)
- [x] Zoom modal (`z`): Toggles `m.zoomed` and displays `m.detailView(m.width)` without the table pane.
- [x] Interactive Editor (`e`): Suspends UI, writes patch to temp file, launches `$EDITOR` (or `vim`), and prompts for confirmation before applying via `kubectl apply -f -`.
- [x] Live Log Streaming (`l`): Suspends UI and runs `kubectl logs -f [kind]/[name] -n [namespace]`.
- [x] Namespace Switcher (`n`): Fetches namespaces dynamically, displays a filterable list using `bubbles/list`, and rescans with the selected namespace on enter.
- [x] Graph Pivot (Tab 8): Displays a list of graph nodes instead of static text. Pressing enter on a node updates the scan filters to target that specific node and rescans.
- [x] Settings TUI Integration (Tab 9): Added Settings tab, routed `detailView`, mapped command palette inputs, implemented `settingsView()`, and handled `e` hotkey configuration editing with dynamic config reloading.

## Shadow Pod Overhaul (Completed)
- [x] `go get github.com/hexops/gotextdiff` and updated `diff.go` to use `gotextdiff`.
- [x] Updated `clone.go` to retain labels/annotations, mock PVCs with EmptyDir, clear affinity/nodeSelector, and block egress by default.
- [x] Updated `parity.go` and `run.go` to compute parity against the unpatched clone.
- [x] Introduced `ai.Provider` in `types.go` and rewrote `revisePatch` in `revision.go` to generate LLM revisions based on logs and events.
- [x] Verified compilation and tests pass successfully via `make test build`.
