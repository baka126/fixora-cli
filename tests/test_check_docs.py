import json
import tempfile
import unittest
from pathlib import Path

from scripts.check_docs import check_source, check_site


class SourceChecks(unittest.TestCase):
    def make_site(self, navigation, features):
        temporary = tempfile.TemporaryDirectory()
        root = Path(temporary.name)
        (root / "_data").mkdir()
        (root / "index.md").write_text("---\ntitle: Home\npermalink: /\n---\n# Home\n")
        (root / "_data" / "navigation.json").write_text(json.dumps(navigation))
        (root / "_data" / "feature_status.json").write_text(json.dumps(features))
        self.addCleanup(temporary.cleanup)
        return root

    def test_duplicate_navigation_path(self):
        root = self.make_site(
            [{"title": "Home", "path": "/"}, {"title": "Again", "path": "/"}],
            [],
        )
        self.assertTrue(any("duplicate navigation path" in issue for issue in check_source(root)))

    def test_missing_feature_owner(self):
        root = self.make_site(
            [{"title": "Home", "path": "/"}],
            [{"name": "MCP", "status": "upcoming", "owner": "/guides/mcp/"}],
        )
        self.assertTrue(any("missing feature owner" in issue for issue in check_source(root)))

    def test_missing_front_matter(self):
        root = self.make_site([{"title": "Home", "path": "/"}], [])
        (root / "broken.md").write_text("# Missing front matter\n")
        self.assertTrue(any("missing front matter" in issue for issue in check_source(root)))

    def test_missing_navigation_target(self):
        root = self.make_site([{"title": "Unknown", "path": "/missing/"}], [])
        self.assertTrue(any("missing navigation target" in issue for issue in check_source(root)))

    def test_duplicate_permalink(self):
        root = self.make_site([{"title": "Home", "path": "/"}], [])
        (root / "again.md").write_text("---\ntitle: Again\npermalink: /\n---\n# Again\n")
        self.assertTrue(any("duplicate permalink" in issue for issue in check_source(root)))

    def test_command_needs_consequence(self):
        root = self.make_site([{"title": "Home", "path": "/"}], [])
        (root / "index.md").write_text("---\ntitle: Home\npermalink: /\n---\n```bash\nkubectl fixora fix pod/api --apply\n```\n")
        self.assertTrue(any("missing command effect" in issue for issue in check_source(root)))

    def test_invalid_page_status(self):
        root = self.make_site([{"title": "Home", "path": "/"}], [])
        (root / "index.md").write_text("---\ntitle: Home\npermalink: /\nstatus: tomorrow\n---\n# Home\n")
        self.assertTrue(any("invalid page status" in issue for issue in check_source(root)))

    def test_missing_feature_anchor(self):
        root = self.make_site([{"title": "Home", "path": "/"}], [{"name": "Crash loops", "status": "released", "owner": "/", "anchor": "crash-loops"}])
        self.assertTrue(any("missing feature anchor" in issue for issue in check_source(root)))

    def test_broken_page_fragment(self):
        root = self.make_site([{"title": "Home", "path": "/"}], [])
        (root / "index.md").write_text("---\ntitle: Home\npermalink: /\n---\n# Home\n[Missing](#nope)\n")
        self.assertTrue(any("broken page fragment" in issue for issue in check_source(root)))

    def test_missing_command_reference(self):
        root = self.make_site([{"title": "Home", "path": "/"}], [])
        (root / "_data" / "commands.json").write_text(json.dumps([{"name": "scan", "status": "released"}]))
        (root / "reference.md").write_text("---\ntitle: Reference\npermalink: /reference/commands/\n---\n# Commands\n")
        self.assertTrue(any("missing command reference: scan" in issue for issue in check_source(root)))


class GeneratedChecks(unittest.TestCase):
    def make_site(self, html):
        temporary = tempfile.TemporaryDirectory()
        root = Path(temporary.name)
        (root / "index.html").write_text(html)
        self.addCleanup(temporary.cleanup)
        return root

    def test_missing_baseurl(self):
        root = self.make_site('<a href="/get-started/">Start</a>')
        self.assertTrue(any("missing baseurl" in issue for issue in check_site(root, "/fixora-cli")))

    def test_excluded_working_page(self):
        root = self.make_site("<h1>Home</h1>")
        (root / "superpowers").mkdir()
        (root / "superpowers" / "plan.html").write_text("plan")
        self.assertTrue(any("excluded page published" in issue for issue in check_site(root, "/fixora-cli")))

    def test_missing_site_asset(self):
        root = self.make_site('<link rel="stylesheet" href="/fixora-cli/assets/css/missing.css">')
        self.assertTrue(any("broken local link" in issue for issue in check_site(root, "/fixora-cli")))

    def test_missing_generated_fragment(self):
        root = self.make_site('<a href="/fixora-cli/#absent">Missing section</a>')
        self.assertTrue(any("broken local fragment" in issue for issue in check_site(root, "/fixora-cli")))


if __name__ == "__main__":
    unittest.main()
