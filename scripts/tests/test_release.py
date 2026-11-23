"""python3 -m unittest discover -s scripts/tests"""
import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))
import release as r  # noqa: E402

BASE = """# Changelog

## [Unreleased]

%s

## [0.2.0] - 2026-10-07

### Added

- old
"""


class Bump(unittest.TestCase):
    def nv(self, body):
        return r.next_version(BASE % body)[:2]

    def test_fixed_is_patch(self):
        self.assertEqual(self.nv("### Fixed\n\n- x"), ("0.2.1", "patch"))

    def test_added_is_minor(self):
        self.assertEqual(self.nv("### Added\n\n- x\n\n### Fixed\n\n- y"), ("0.3.0", "minor"))

    def test_breaking_before_1_0_is_minor_bump(self):
        self.assertEqual(self.nv("### Breaking changes\n\n- x"), ("0.3.0", "major"))

    def test_breaking_after_1_0_is_major(self):
        text = (BASE % "### Removed\n\n- x").replace("0.2.0", "1.4.2")
        self.assertEqual(r.next_version(text)[:2], ("2.0.0", "major"))

    def test_planned_heading_wins_when_higher(self):
        text = (BASE % "### Fixed\n\n- x").replace("## [Unreleased]", "## [0.5.0] - unreleased")
        self.assertEqual(r.next_version(text)[0], "0.5.0")

    def test_dated_and_rc(self):
        text = BASE % "### Fixed\n\n- x"
        self.assertIsNotNone(r.dated(text, "0.2.0"))
        self.assertIsNone(r.dated(text, "0.2.1"))
        self.assertIsNotNone(r.dated(text, "0.2.1-rc.1"))  # rc of the unreleased section
        self.assertIn("- old", r.section_body(text, r.dated(text, "0.2.0")))


if __name__ == "__main__":
    unittest.main()


class Conventional(unittest.TestCase):
    def test_mapping_and_skips(self):
        e = r.conventional([
            ("feat(h3_vm): user_data", ""),
            ("fix: retry signing", ""),
            ("docs: readme", ""),
            ("chore(ci): bump", ""),
            ("Merge branch 'x' into 'main'", ""),
            ("refactor!: drop old attr", ""),
            ("feat: new api", "BREAKING CHANGE: field renamed"),
            ("feat(h3_vm): user_data", ""),
        ])
        self.assertEqual(e["Added"], ["h3_vm: user_data"])
        self.assertEqual(e["Fixed"], ["retry signing"])
        self.assertEqual(e["Breaking changes"], ["drop old attr", "new api"])
        self.assertNotIn("Changed", e)

    def test_render_and_bump(self):
        body = r.render(r.conventional([("feat: a", ""), ("fix: b", "")]))
        self.assertTrue(body.startswith("### Added\n\n- a"))
        text = r.fill_unreleased(BASE % "", body)
        self.assertEqual(r.next_version(text)[:2], ("0.3.0", "minor"))

    def test_hand_written_entries_win(self):
        text = BASE % "### Fixed\n\n- by hand"
        self.assertEqual(r.fill_unreleased(text, "### Added\n\n- auto"), text)

    def test_unreleased_created_when_missing(self):
        text = "# Changelog\n\n## [0.2.0] - 2026-10-07\n\n### Added\n\n- old\n"
        new = r.fill_unreleased(text, "### Fixed\n\n- x")
        self.assertEqual(r.next_version(new)[:2], ("0.2.1", "patch"))
