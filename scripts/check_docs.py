"""Dependency-free checks for Fixora's Jekyll documentation source."""

import argparse
import json
from pathlib import Path


def _pages(source: Path):
    pages = set()
    issues = []
    for path in source.rglob("*.md"):
        if any(part in {"superpowers", "_site"} for part in path.parts) or path.name == "checklist.md":
            continue
        body = path.read_text(encoding="utf-8")
        if not body.startswith("---\n") or len(body.split("---\n", 2)) < 3:
            issues.append(f"missing front matter: {path.relative_to(source)}")
            continue
        front = body.split("---\n", 2)[1]
        fields = {}
        for line in front.splitlines():
            if ":" in line:
                key, value = line.split(":", 1)
                fields[key.strip()] = value.strip().strip('"\'')
        permalink = fields.get("permalink")
        if not permalink:
            if path.name == "index.md" and path.parent == source:
                permalink = "/"
            else:
                issues.append(f"missing permalink: {path.relative_to(source)}")
        if permalink:
            if permalink in pages:
                issues.append(f"duplicate permalink: {permalink}")
            pages.add(permalink)
    return pages, issues


def check_source(source: Path) -> list[str]:
    issues = []
    data = source / "_data"
    try:
        navigation = json.loads((data / "navigation.json").read_text(encoding="utf-8"))
        features = json.loads((data / "feature_status.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        return [f"site data cannot be read: {error}"]
    pages, page_issues = _pages(source)
    issues.extend(page_issues)
    seen = set()
    for item in navigation:
        path = item.get("path", "")
        if path in seen:
            issues.append(f"duplicate navigation path: {path}")
        seen.add(path)
        if path not in pages:
            issues.append(f"missing navigation target: {path}")
    for feature in features:
        owner = feature.get("owner", "")
        if owner not in pages:
            issues.append(f"missing feature owner: {feature.get('name', '<unnamed>')} -> {owner}")
        if feature.get("status") not in {"released", "upcoming"}:
            issues.append(f"invalid feature status: {feature.get('name', '<unnamed>')}")
    return issues


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    args = parser.parse_args()
    issues = check_source(args.source)
    for issue in issues:
        print(issue)
    if issues:
        return 1
    print("Documentation source checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
