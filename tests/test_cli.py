"""Target selection and output contracts of the single `churnpaths` command."""

import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from _load import load

churnpaths = load("churnpaths")


class CliTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.folder = self.root / "scratch"
        self.folder.mkdir()
        self.listing = self.root / "list.txt"
        self.listing.write_text(f"path {self.folder}\n")
        self.xml = self.root / "absent.xml"
        self.base = ["-s", str(self.listing), "-x", str(self.xml)]

    def run_cli(self, args):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            try:
                code = churnpaths.main(self.base + args)
            except SystemExit as exit_:
                code = exit_.code
        return code, out.getvalue(), err.getvalue()

    def fake_planners(self, **plans):
        base = {"add": [], "remove": [], "in_sync": True, "applied": False}
        return patch.dict(
            churnpaths.PLANNERS,
            {
                k: lambda a, r, apply, p=v: base | {"detail": "x"} | p
                for k, v in plans.items()
            },
        )

    def test_list_json_is_the_bare_resolver_result(self):
        code, out, _ = self.run_cli(["-l", "-j", "-m", "backblaze"])
        self.assertEqual(code, 0)
        self.assertEqual(
            set(json.loads(out)), {"paths", "missing", "unmatched", "skipped"}
        )

    def test_default_skips_unavailable_backblaze_with_a_note(self):
        with (
            patch.object(churnpaths.sys, "platform", "darwin"),
            patch.object(churnpaths.shutil, "which", return_value="/usr/bin/tmutil"),
            self.fake_planners(spotlight={}, timemachine={}),
        ):
            code, out, _ = self.run_cli([])
        self.assertEqual(code, 0)
        self.assertIn("backblaze: skipped (Backblaze not installed", out)
        self.assertIn("spotlight [dry run]", out)
        self.assertIn("timemachine [dry run]", out)

    def test_explicit_unavailable_target_is_an_error(self):
        with patch.object(churnpaths.sys, "platform", "darwin"):
            code, _, err = self.run_cli(["-t", "backblaze"])
        self.assertEqual(code, 2)
        self.assertIn("backblaze is unavailable", err)

    def test_target_is_repeatable_and_all_means_everything(self):
        def names(args):
            chosen, _ = churnpaths.select_targets(self.parse(args))
            return [t for t, _ in chosen]

        self.assertEqual(
            names(["-t", "backblaze", "-t", "spotlight"]), ["spotlight", "backblaze"]
        )
        self.assertEqual(names(["-t", "all"]), list(churnpaths.TARGETS))
        self.assertEqual(names([]), list(churnpaths.TARGETS))

    def parse(self, args):
        return churnpaths.build_parser({}).parse_args(args)

    def test_check_exits_one_on_drift_in_any_target(self):
        with (
            patch.object(churnpaths.sys, "platform", "darwin"),
            self.fake_planners(spotlight={}, timemachine={"in_sync": False}),
        ):
            code, _, _ = self.run_cli(["-c", "-t", "spotlight", "-t", "timemachine"])
        self.assertEqual(code, 1)

    def test_apply_for_another_host_is_refused(self):
        with patch.object(churnpaths.sys, "platform", "darwin"):
            code, _, err = self.run_cli(
                ["-a", "-H", "not-this-host", "-t", "spotlight"]
            )
        self.assertEqual(code, 2)
        self.assertIn("hostname must match", err)

    def test_dry_run_conflicts_with_apply_and_list_with_modes(self):
        self.assertEqual(self.run_cli(["-n", "-a"])[0], 2)
        self.assertEqual(self.run_cli(["-l", "-a"])[0], 2)
        self.assertEqual(self.run_cli(["status", "-a"])[0], 2)

    def test_one_failing_target_does_not_stop_the_others(self):
        def boom(args, result, apply):
            raise RuntimeError("no backend")

        with (
            patch.object(churnpaths.sys, "platform", "darwin"),
            self.fake_planners(timemachine={}),
            patch.dict(churnpaths.PLANNERS, {"spotlight": boom}),
        ):
            code, out, err = self.run_cli(["-t", "spotlight", "-t", "timemachine"])
        self.assertEqual(code, 2)
        self.assertIn("spotlight: no backend", err)
        self.assertIn("timemachine [dry run]", out)

    def test_backblaze_preview_says_dry_run_and_writes_nothing(self):
        self.xml.write_bytes(b"<bzexclusions>\n</bzexclusions>\n")
        before = self.xml.read_bytes()
        code, out, _ = self.run_cli(
            ["-t", "backblaze", "--state-dir", str(self.root / "st")]
        )
        self.assertEqual(code, 0)
        self.assertIn("backblaze [dry run]", out)
        self.assertIn("nothing is written to Backblaze without -a", out)
        self.assertEqual(self.xml.read_bytes(), before)

    def test_status_shows_backblaze_state_as_json(self):
        self.xml.write_bytes(b"<bzexclusions>\n</bzexclusions>\n")
        code, out, _ = self.run_cli(
            ["status", "-j", "-t", "backblaze", "--state-dir", str(self.root / "st")]
        )
        self.assertEqual(code, 0)
        self.assertEqual(
            json.loads(out), {"targets": {"backblaze": {str(self.folder): "included"}}}
        )


if __name__ == "__main__":
    unittest.main()
