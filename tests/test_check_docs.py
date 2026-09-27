import json
import tempfile
import unittest
from pathlib import Path

from scripts.check_docs import check_source


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


if __name__ == "__main__":
    unittest.main()
