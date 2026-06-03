import re

with open("internal/analyzer/precision.go", "r") as f:
    content = f.read()

content = content.replace("""	type precisionAnalyzer struct {
		name    string
		aliases []string
		run     func(*ScanContext) ([]Finding, error)
	}
	analyzers := []precisionAnalyzer{""",
"""	type precisionAnalyzer struct {
		name    string
		aliases []string
		run     func(*ScanContext) ([]Finding, error)
	}
	analyzers := []precisionAnalyzer{""")

# Actually let's just make sure we are not over-complicating it. The user said:
# HIGH-26: Analyzer interface/registry pattern (types.go, analyzer.go, registry.go)
