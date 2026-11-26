"""Validate Fixora's Claude Code plugin wiring against its MCP tool registry."""

import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
EXPECTED_TOOLS = {
    "analyze", "incidents", "health", "runbook", "plan-fix", "preview-fix",
    "validate-fix", "list-resources", "get-resource", "get-logs", "list-events",
    "list-filters", "config",
}


def main() -> int:
    issues = []
    try:
        marketplace = json.loads((ROOT / ".claude-plugin/marketplace.json").read_text())
        manifest = json.loads((ROOT / "plugins/fixora/.claude-plugin/plugin.json").read_text())
        mcp = json.loads((ROOT / "plugins/fixora/.mcp.json").read_text())
    except (OSError, json.JSONDecodeError) as error:
        print(f"plugin configuration is invalid: {error}")
        return 1

    if manifest.get("name") != "fixora":
        issues.append("plugin manifest name must be fixora to provide the /fixora root skill")
    entries = marketplace.get("plugins", [])
    if marketplace.get("name") != "fixora-cli" or not any(
        item.get("name") == "fixora" and item.get("source") == "./plugins/fixora"
        for item in entries
    ):
        issues.append("Fixora marketplace must list the local plugins/fixora plugin")
    server = mcp.get("mcpServers", {}).get("fixora", {})
    if server.get("command") != "kubectl-fixora" or server.get("args") != ["serve", "--mcp"]:
        issues.append("MCP server must invoke kubectl-fixora serve --mcp without enabling shadow writes")

    skill_path = ROOT / "plugins/fixora/SKILL.md"
    skill = skill_path.read_text(encoding="utf-8") if skill_path.exists() else ""
    frontmatter = skill.split("---", 2)
    if len(frontmatter) < 3 or not re.search(r"(?m)^name:\s*fixora\s*$", frontmatter[1]):
        issues.append("root SKILL.md must declare name: fixora")
    if "never" not in skill.lower() or "preview" not in skill.lower() or "verification" not in skill.lower():
        issues.append("/fixora guidance must preserve preview and verification gates")
    documented = set(re.findall(r"`([a-z][a-z0-9-]*)`", skill.split("The default MCP tool set is:", 1)[-1]))

    sdk = (ROOT / "internal/mcp/sdk.go").read_text(encoding="utf-8")
    registered = set(re.findall(r'addTool\[[^\]]+\]\(server, s, "([^"]+)"', sdk))
    if not EXPECTED_TOOLS.issubset(registered):
        issues.append(f"MCP registry is missing expected tools: {sorted(EXPECTED_TOOLS - registered)}")
    if documented != registered:
        issues.append(f"/fixora tool list differs from the default MCP registry: documented={sorted(documented)} registered={sorted(registered)}")
    if 'if s.EnableShadow' not in sdk or 'Name: "shadow-verify"' not in sdk:
        issues.append("could not confirm the opt-in shadow tool remains separate from the default tool set")

    if issues:
        print("\n".join(issues))
        return 1
    print("Fixora Claude plugin wiring checks passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
