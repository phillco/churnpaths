import tempfile
import unittest
from pathlib import Path

from _load import load
import backblaze_xml as xml

backblaze = load("churnpaths-backblaze")

USER_RULE = b'<excludefname_rule plat="mac" osVers="*" ruleIsOptional="t" skipFirstCharThenStartsWith="users/" contains_1="/itunes/" contains_2="*" doesNotContain="*" endsWith="*" hasFileExtension="*" bzmergeblock="custom-v1" />'
ORIGINAL = (
    b'<?xml version="1.0" encoding="UTF-8"?>\n<bzexclusions>\n<!-- Keep me. -->\n  '
    + USER_RULE
    + b"\n</bzexclusions>\n"
)


class XMLTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.config = self.root / "editable.xml"
        self.config.write_bytes(ORIGINAL)
        self.config.chmod(0o640)
        self.state = self.root / "state"
        self.policy = xml.XMLPolicy(self.config, self.state)

    def test_add_preserves_unowned_bytes_and_backs_up(self):
        self.policy.set_many(["/opt/cache one", "/opt/a&b"])
        changed = self.config.read_bytes()
        self.assertIn(USER_RULE, changed)
        start, end = changed.index(xml.BEGIN), changed.index(xml.END) + len(xml.END)
        self.assertEqual(changed[:start] + changed[end + 1 :], ORIGINAL)
        self.assertEqual(self.policy.owned(), {"/opt/cache one", "/opt/a&b"})
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o640)
        backups = list((self.state / "xml-backups").iterdir())
        self.assertEqual([b.read_bytes() for b in backups], [ORIGINAL])
        self.assertTrue((self.state / "xml-changes.ndjson").exists())

    def test_remove_restores_original_bytes_around_block(self):
        self.policy.set_many(["/opt/cache"])
        self.policy.set_many(["/opt/cache"], False)
        self.assertEqual(self.policy.owned(), set())
        self.assertIn(USER_RULE, self.config.read_bytes())

    def test_prefix_ends_with_slash_so_siblings_do_not_match(self):
        attrs = xml.attributes("/opt/cache")
        self.assertEqual(attrs["skipFirstCharThenStartsWith"], "opt/cache/")

    def test_unsafe_prefixes_rejected(self):
        for bad in ["/", "/Users", "relative", "/opt/../x", "/opt/a*b", "/ab"]:
            with self.subTest(bad=bad), self.assertRaises(xml.XMLPolicyError):
                xml.attributes(bad)

    def test_tampered_block_is_refused(self):
        self.policy.set_many(["/opt/cache"])
        head, tail = self.config.read_bytes().split(xml.BEGIN)
        data = head + xml.BEGIN + tail.replace(b'endsWith="*"', b'endsWith="x"', 1)
        self.config.write_bytes(data)
        with self.assertRaises(xml.XMLPolicyError):
            self.policy.owned()

    def test_dtd_symlink_and_missing_file_refused(self):
        self.config.write_bytes(b"<!DOCTYPE x [<!ENTITY a 'b'>]><bzexclusions/>")
        with self.assertRaises(xml.XMLPolicyError):
            self.policy.owned()
        link = self.root / "link.xml"
        link.symlink_to(self.config)
        with self.assertRaises(xml.XMLPolicyError):
            xml.XMLPolicy(link, self.state).owned()
        self.assertEqual(
            xml.XMLPolicy(self.root / "nope.xml", self.state).owned(), set()
        )

    def test_concurrent_change_is_not_overwritten(self):
        original = self.policy.snapshot

        def racing():
            result = original()
            self.config.write_bytes(ORIGINAL + b"\n")
            return result

        self.policy.snapshot = racing
        with self.assertRaises(xml.XMLPolicyError):
            self.policy.set_many(["/opt/cache"])
        self.policy.snapshot = original

    def test_cli_dry_run_apply_and_check(self):
        listing = self.root / "list.txt"
        folder = self.root / "scratch"
        folder.mkdir()
        listing.write_text(f"path {folder}\npath {self.root}/gone\n")
        base = [
            "-s",
            str(listing),
            "-x",
            str(self.config),
            "--state-dir",
            str(self.state),
        ]
        self.assertEqual(backblaze.main(base), 0)
        self.assertEqual(self.config.read_bytes(), ORIGINAL)
        self.assertEqual(backblaze.main(base + ["-c"]), 1)
        self.assertEqual(backblaze.main(base + ["-a"]), 0)
        self.assertEqual(len(self.policy.owned()), 2)
        self.assertEqual(backblaze.main(base + ["-c"]), 0)
        listing.write_text("")
        self.assertEqual(backblaze.main(base + ["-a"]), 0)
        self.assertEqual(self.policy.owned(), set())


if __name__ == "__main__":
    unittest.main()
