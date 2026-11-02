import contextlib
import io
import plistlib
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import tm_exclusion_api as tm
from _load import load

churnpaths = load("churnpaths")
REAL_RUN = subprocess.run


def tmutil_output(states):
    rows = [{"Path": str(p), "IsExcluded": v} for p, v in states.items()]
    return subprocess.CompletedProcess([], 0, plistlib.dumps(rows).decode(), "")


class TimeMachineTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.a = self.root / "a"
        self.b = self.root / "b"
        self.a.mkdir()
        self.b.mkdir()
        self.listing = self.root / "list.txt"
        self.listing.write_text(
            f"path {self.a}\npath {self.b}\npath {self.root}/gone\n"
        )

    def run_tm(self, args, states):
        calls = []

        def fake(command, **kwargs):
            if command[0] == "tmutil":
                paths = [Path(p) for p in command[3:]]
                return tmutil_output({p: states.get(p, False) for p in paths})
            return REAL_RUN(command, **kwargs)

        with (
            mock.patch.object(tm.subprocess, "run", side_effect=fake),
            mock.patch.object(tm, "set_excluded", side_effect=calls.append),
        ):
            with contextlib.redirect_stdout(io.StringIO()):
                code = churnpaths.main(
                    ["-t", "timemachine", "-s", str(self.listing)] + args
                )
        return code, calls

    def test_resolves_present_and_missing(self):
        result = churnpaths.resolve(self.listing, "any")
        self.assertEqual(result["paths"], [str(self.a), str(self.b)])
        self.assertEqual(result["missing"], [f"{self.root}/gone"])

    def test_dry_run_writes_nothing(self):
        code, calls = self.run_tm(["-n"], {})
        self.assertEqual((code, calls), (0, []))

    def test_apply_only_writes_included_folders_and_reads_back(self):
        # tmutil reports b excluded already; a is still included after the write.
        code, calls = self.run_tm(["-a"], {self.b: True})
        self.assertEqual(calls, [self.a])
        self.assertEqual(code, 2)  # a never read back as excluded

    def test_check_reports_included(self):
        self.assertEqual(self.run_tm(["-c"], {self.a: True})[0], 1)
        self.assertEqual(self.run_tm(["-c"], {self.a: True, self.b: True})[0], 0)

    def test_partial_tmutil_output_is_an_error(self):
        partial = tmutil_output({self.a: True})
        with mock.patch.object(tm.subprocess, "run", return_value=partial):
            with self.assertRaises(RuntimeError):
                tm.exclusion_states([self.a, self.b])

    def test_check_and_apply_conflict(self):
        with self.assertRaises(SystemExit):
            churnpaths.main(["-c", "-a"])

    def test_default_preview_writes_nothing_and_status_reads(self):
        self.assertEqual(self.run_tm([], {}), (0, []))
        self.assertEqual(self.run_tm(["status"], {self.a: True}), (0, []))


if __name__ == "__main__":
    unittest.main()
