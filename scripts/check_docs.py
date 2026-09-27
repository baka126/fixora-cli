"""Dependency-free checks for Fixora's Jekyll documentation source."""

import argparse
import json
from pathlib import Path


def _pages(source: Path):
    pages = set()
    for path in source.rglob("*.md"):
        if any(part in {"superpowers", "_site"} for part in path.parts):
            continue
        body = path.read_text(encoding="utf-8")
        if body.startswith("---\n"):
            front = body.split("---\n", 2)[1]
            for line in front.splitlines():
                if line.startswith("permalink:"):
                    pages.add(line.split(":", 1)[1].strip().strip('"\''))
        if path.name == "index.md" and path.parent == source:
            pages.add("/")
    return pages


def check_source(source: Path) -> list[str]:
    issues = []
    data = source / "_data"
    try:
        navigation = json.loads((data / "navigation.json").read_text(encoding="utf-8"))
        features = json.loads((data / "feature_status.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        return [f"site data cannot be read: {error}"]
    pages = _pages(source)
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
