"""Dependency-free checks for Fixora's Jekyll documentation source."""

import argparse
import json
import re
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urljoin, urlsplit


class _Links(HTMLParser):
    def __init__(self):
        super().__init__()
        self.links = []
        self.ids = set()

    def handle_starttag(self, tag, attrs):
        values = dict(attrs)
        if values.get("id"):
            self.ids.add(values["id"])
        for key in ("href", "src"):
            if values.get(key):
                self.links.append(values[key])


def _pages(source: Path):
    pages = set()
    anchors = {}
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
        if fields.get("status") and fields["status"] not in {"released", "upcoming"}:
            issues.append(f"invalid page status: {path.relative_to(source)}")
        lines = body.splitlines()
        for index, line in enumerate(lines):
            if line.strip() == "```bash" and not any("Effect:" in prior for prior in lines[max(0, index - 3):index]):
                issues.append(f"missing command effect: {path.relative_to(source)}:{index + 1}")
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
            headings = re.findall(r"^#{1,6}\s+(.+)$", body, re.MULTILINE)
            anchor_set = {re.sub(r"[^a-z0-9 -]", "", heading.lower()).strip().replace(" ", "-") for heading in headings}
            anchors[permalink] = anchor_set
            for fragment in re.findall(r"\]\(#([^)]+)\)", body):
                if fragment not in anchor_set:
                    issues.append(f"broken page fragment: {path.relative_to(source)}#{fragment}")
    return pages, anchors, issues


def check_source(source: Path) -> list[str]:
    issues = []
    data = source / "_data"
    try:
        navigation = json.loads((data / "navigation.json").read_text(encoding="utf-8"))
        features = json.loads((data / "feature_status.json").read_text(encoding="utf-8"))
    except (OSError, ValueError) as error:
        return [f"site data cannot be read: {error}"]
    pages, anchors, page_issues = _pages(source)
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
        elif feature.get("anchor") and feature["anchor"] not in anchors.get(owner, set()):
            issues.append(f"missing feature anchor: {feature.get('name', '<unnamed>')} -> {owner}#{feature['anchor']}")
        if feature.get("status") not in {"released", "upcoming"}:
            issues.append(f"invalid feature status: {feature.get('name', '<unnamed>')}")
    command_data = data / "commands.json"
    if command_data.exists():
        try:
            commands = json.loads(command_data.read_text(encoding="utf-8"))
        except ValueError as error:
            issues.append(f"command inventory cannot be read: {error}")
            commands = []
        reference = "\n".join(path.read_text(encoding="utf-8") for path in source.rglob("*.md") if "permalink: /reference/commands/" in path.read_text(encoding="utf-8"))
        for command in commands:
            name = command.get("name", "")
            if f"`{name}`" not in reference:
                issues.append(f"missing command reference: {name}")
    return issues


def check_site(site: Path, baseurl: str) -> list[str]:
    issues = []
    html_files = list(site.rglob("*.html"))
    parsed = {}
    for path in html_files:
        if any(part == "superpowers" for part in path.relative_to(site).parts):
            issues.append(f"excluded page published: {path.relative_to(site)}")
        parser = _Links()
        parser.feed(path.read_text(encoding="utf-8"))
        parsed[path] = parser
    for source, parser in parsed.items():
        relative = source.relative_to(site).as_posix()
        page_url = baseurl.rstrip("/") + "/" + (relative[:-10] if relative.endswith("index.html") else relative)
        for link in parser.links:
            parsed_url = urlsplit(link)
            if parsed_url.scheme or parsed_url.netloc or link.startswith("//"):
                continue
            absolute = urlsplit(urljoin(page_url, link))
            target_path = unquote(absolute.path)
            if not (target_path == baseurl or target_path.startswith(baseurl + "/")):
                issues.append(f"missing baseurl: {relative} -> {link}")
                continue
            site_relative = target_path[len(baseurl):].lstrip("/")
            target = site / site_relative
            if target.is_dir():
                target = target / "index.html"
            if not target.is_file():
                issues.append(f"broken local link: {relative} -> {link}")
                continue
            if absolute.fragment and target.suffix == ".html":
                target_parser = parsed.get(target)
                if target_parser and absolute.fragment not in target_parser.ids:
                    issues.append(f"broken local fragment: {relative} -> {link}")
    return issues


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--site", type=Path)
    parser.add_argument("--baseurl", default="/fixora-cli")
    args = parser.parse_args()
    issues = check_source(args.source)
    if args.site:
        issues.extend(check_site(args.site, args.baseurl))
    for issue in issues:
        print(issue)
    if issues:
        return 1
    print("Documentation source checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
