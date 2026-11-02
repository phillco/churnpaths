"""Isolated manifest discovery and Spotlight ownership checks; no live writes."""

import json
import socket
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import churnpaths_spotlight
from _load import load

churnpaths = load("churnpaths")


class Fixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.source = self.root / "manifest.txt"

    def folder(self, relative):
        folder = self.root / relative
        folder.mkdir(parents=True, exist_ok=True)
        return folder

    def resolve(self, text, host="alpha"):
        self.source.write_text(text)
        return churnpaths.resolve(self.source, host)


class ManifestTests(Fixture):
    def test_host_scopes_casefold_wildcard_and_spaces(self):
        shared = self.folder("shared space")
        alpha = self.folder("alpha")
        other = self.folder("other")
        wildcard = self.folder("wildcard")
        result = self.resolve(
            f"# comment\npath {shared}\n@ALPHA path {alpha}\n"
            f"@laptop path {other}\n@* path {wildcard}\n"
        )
        self.assertEqual(result["paths"], sorted(map(str, [shared, alpha, wildcard])))

    def test_missing_exact_and_unmatched_glob_remain_distinct(self):
        missing = self.root / "missing"
        pattern = self.root / "job-*"
        result = self.resolve(f"path {missing}\nglob {pattern}\n")
        self.assertEqual(result["paths"], [])
        self.assertEqual(result["missing"], [str(missing)])
        self.assertEqual(result["unmatched"], [str(pattern)])

    def test_glob_only_finds_immediate_children(self):
        direct = self.folder("job-a")
        self.folder("parent/job-b")
        (self.root / "job-not-a-folder.json").write_text("{}")
        result = self.resolve(f"glob {self.root}/job-*\n")
        self.assertEqual(result["paths"], [str(direct)])

    def test_recursive_or_parent_component_globs_rejected(self):
        for pattern in ["**/cache", "job-*/cache", "**", "?/cache", "[ab]/cache"]:
            with self.subTest(pattern=pattern), self.assertRaises(ValueError):
                self.resolve(f"glob {self.root}/{pattern}\n")

    def test_broad_relative_and_parent_paths_rejected(self):
        for raw in [
            "/",
            "/Users",
            "/Volumes",
            "/Volumes/Disk1",
            "/System/Volumes/Data",
            str(Path.home()),
            "relative",
            f"{self.root}/../cache",
        ]:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                self.resolve(f"path {raw}\n")

    def test_glob_hit_cannot_bypass_home_folder_guard(self):
        home = self.folder("homes/someone")
        with patch.object(churnpaths.Path, "home", return_value=home):
            with self.assertRaises(ValueError):
                self.resolve(f"glob {home.parent}/*\n")

    def test_exact_file_is_not_a_folder(self):
        file = self.root / "file"
        file.write_text("data")
        with self.assertRaises(ValueError):
            self.resolve(f"path {file}\n")

    def test_named_virtualenv_requires_environment_evidence(self):
        cfg = self.folder("a/.venv")
        (cfg / "pyvenv.cfg").touch()
        activation = self.folder("b/venv/bin")
        (activation / "activate").touch()
        self.folder("c/venv")
        self.folder("d/.venv")
        result = self.resolve(f"name .venv {self.root}\nname venv {self.root}\n")
        self.assertEqual(result["paths"], sorted([str(cfg), str(activation.parent)]))

    def test_named_walk_does_not_follow_child_symlinks_or_git(self):
        outside = self.folder("outside/node_modules")
        scan = self.folder("scan")
        (scan / "link").symlink_to(outside.parent, target_is_directory=True)
        self.folder("scan/.git/node_modules")
        self.folder("scan/.jj/node_modules")
        result = self.resolve(f"name node_modules {scan}\n")
        self.assertEqual(result["paths"], [])

    def test_name_root_symlink_is_not_walked(self):
        outside = self.folder("outside/node_modules")
        link = self.root / "linked-root"
        link.symlink_to(outside.parent, target_is_directory=True)
        try:
            result = self.resolve(f"name node_modules {link}\n")
        except ValueError:
            return  # Explicit rejection is also fail-closed.
        self.assertEqual(result["paths"], [])

    def test_exact_symlink_and_symlink_ancestor_are_rejected(self):
        outside = self.folder("outside/cache")
        link = self.root / "link"
        link.symlink_to(outside.parent, target_is_directory=True)
        for rule in [f"path {link}", f"path {link}/cache", f"glob {link}/ca*"]:
            with self.subTest(rule=rule), self.assertRaises(ValueError):
                self.resolve(rule + "\n")

    def test_symlinked_glob_hit_is_skipped_not_fatal(self):
        real = self.folder("src/job-real")
        target = self.folder("elsewhere/job-target")
        (self.root / "src/job-alias").symlink_to(target, target_is_directory=True)
        result = self.resolve(f"glob {self.root}/src/job-*\n")
        self.assertEqual(result["paths"], [str(real)])
        self.assertEqual(result["skipped"], [str(self.root / "src/job-alias")])
        self.assertEqual(result["unmatched"], [])

    def test_glob_matching_only_symlinks_is_not_unmatched(self):
        target = self.folder("elsewhere/job-target")
        self.folder("src")
        (self.root / "src/job-alias").symlink_to(target, target_is_directory=True)
        result = self.resolve(f"glob {self.root}/src/job-*\n")
        self.assertEqual(result["paths"], [])
        self.assertEqual(result["unmatched"], [])
        self.assertEqual(len(result["skipped"]), 1)

    def test_name_discovery_stops_after_four_levels(self):
        included = self.folder("a/b/c/node_modules")
        self.folder("a/b/c/d/node_modules")
        result = self.resolve(f"name node_modules {self.root}\n")
        self.assertEqual(result["paths"], [str(included)])

    def test_matching_name_prunes_nested_matches(self):
        parent = self.folder("project/node_modules")
        self.folder("project/node_modules/nested/node_modules")
        result = self.resolve(f"name node_modules {self.root}\n")
        self.assertEqual(result["paths"], [str(parent)])

    def test_rust_target_requires_generated_build_evidence(self):
        generated = self.folder("rust/target/debug/deps")
        self.folder("source/target")
        result = self.resolve(f"name target {self.root}\n")
        self.assertEqual(result["paths"], [str(generated.parent.parent)])

    def test_exact_parent_covers_nested_glob_and_named_hits(self):
        parent = self.folder("job")
        self.folder("job/node_modules")
        result = self.resolve(
            f"path {parent}\nglob {parent}/node_*\nname node_modules {self.root}\n"
        )
        self.assertEqual(result["paths"], [str(parent)])


class OwnershipTests(Fixture):
    def setUp(self):
        super().setUp()
        self.state = self.root / "state/spotlight.json"
        self.args = SimpleNamespace(
            state=self.state,
            apply=True,
            hostname=socket.gethostname().split(".", 1)[0],
        )
        self.current = set()
        self.calls = []
        self.read_patch = patch.object(
            churnpaths_spotlight, "exclusions", side_effect=lambda: sorted(self.current)
        )
        self.write_patch = patch.object(
            churnpaths_spotlight, "set_excluded", side_effect=self.set_excluded
        )
        self.read_patch.start()
        self.write_patch.start()
        self.addCleanup(self.read_patch.stop)
        self.addCleanup(self.write_patch.stop)

    def set_excluded(self, path, excluded):
        self.calls.append((path, excluded))
        if excluded:
            self.current.add(path)
        else:
            self.current.discard(path)

    def save_state(self, managed):
        self.state.parent.mkdir(parents=True, exist_ok=True)
        self.state.write_text(json.dumps({"version": 1, "managed": managed}))

    def result(self, paths=(), missing=()):
        return {"paths": list(paths), "missing": list(missing), "unmatched": []}

    def managed(self):
        return json.loads(self.state.read_text())["managed"]

    def test_preserves_unrelated_and_preexisting_desired_exclusions(self):
        existing = str(self.folder("existing"))
        added = str(self.folder("added"))
        unrelated = str(self.folder("unrelated"))
        self.current.update([existing, unrelated])
        churnpaths.reconcile(self.args, self.result([existing, added]))
        self.assertEqual(self.current, {existing, added, unrelated})
        self.assertEqual(self.managed(), [added])
        self.assertEqual(self.calls, [(added, True)])

    def test_preview_has_no_api_or_ownership_mutations(self):
        desired = str(self.folder("desired"))
        self.args.apply = False
        self.assertTrue(churnpaths.reconcile(self.args, self.result([desired])))
        self.assertFalse(self.state.exists())
        self.assertEqual(self.calls, [])

    def test_removes_only_stale_owned_exclusion(self):
        stale = str(self.folder("stale"))
        desired = str(self.folder("desired"))
        unrelated = str(self.folder("unrelated"))
        self.save_state([stale])
        self.current.update([stale, unrelated])
        churnpaths.reconcile(self.args, self.result([desired]))
        self.assertEqual(self.current, {unrelated, desired})
        self.assertEqual(self.managed(), [desired])
        self.assertCountEqual(self.calls, [(desired, True), (stale, False)])

    def test_owned_folder_replaced_by_symlink_is_removed_not_fatal(self):
        target = self.folder("elsewhere/real")
        link = self.root / "was-a-folder"
        link.symlink_to(target, target_is_directory=True)
        desired = str(self.folder("desired"))
        self.save_state([str(link)])
        self.current.add(str(link))
        churnpaths.reconcile(self.args, self.result([desired]))
        self.assertEqual(self.current, {desired})
        self.assertCountEqual(self.calls, [(desired, True), (str(link), False)])

    def test_missing_declared_path_retains_ownership(self):
        missing = str(self.root / "missing")
        self.save_state([missing])
        self.current.add(missing)
        self.assertFalse(
            churnpaths.reconcile(self.args, self.result(missing=[missing]))
        )
        self.assertEqual(self.managed(), [missing])
        self.assertEqual(self.calls, [])

    def test_absent_stale_path_loses_ownership(self):
        stale = str(self.root / "gone")
        self.save_state([stale])
        churnpaths.reconcile(self.args, self.result())
        self.assertEqual(self.managed(), [])
        self.assertEqual(self.calls, [])

    def test_invalid_state_fails_before_any_api_mutation(self):
        for state in [
            [],
            None,
            {"version": 99, "managed": []},
            {"version": 1, "managed": [None]},
            {"version": 1, "managed": ["/"]},
        ]:
            with self.subTest(state=state):
                self.state.parent.mkdir(parents=True, exist_ok=True)
                self.state.write_text(json.dumps(state))
                with self.assertRaises(ValueError):
                    churnpaths.reconcile(
                        self.args, self.result([str(self.folder("desired"))])
                    )
                self.assertEqual(self.calls, [])

    def test_wrong_host_fails_before_mutation(self):
        self.args.hostname = "a-different-host.invalid"
        with self.assertRaises(ValueError):
            churnpaths.reconcile(self.args, self.result([str(self.folder("desired"))]))
        self.assertFalse(self.state.exists())
        self.assertEqual(self.calls, [])

    def test_failure_before_api_never_creates_an_exclusion(self):
        desired = str(self.folder("desired"))
        with patch.object(churnpaths, "write_json", side_effect=OSError("disk full")):
            with self.assertRaises(OSError):
                churnpaths.reconcile(self.args, self.result([desired]))
        self.assertEqual(self.current, set())
        self.assertEqual(self.calls, [])

    def test_add_then_readback_failure_retains_ownership_for_recovery(self):
        desired = str(self.folder("desired"))

        def fail_after_add(path, excluded):
            self.set_excluded(path, excluded)
            raise churnpaths_spotlight.SpotlightError("readback failed")

        with patch.object(
            churnpaths_spotlight, "set_excluded", side_effect=fail_after_add
        ):
            with self.assertRaises(churnpaths_spotlight.SpotlightError):
                churnpaths.reconcile(self.args, self.result([desired]))
        self.assertEqual(self.managed(), [desired])
        self.assertIn(desired, self.current)
        churnpaths.reconcile(self.args, self.result())
        self.assertNotIn(desired, self.current)
        self.assertEqual(self.managed(), [])

    def test_remove_then_failure_can_finish_ownership_cleanup(self):
        stale = str(self.folder("stale"))
        self.save_state([stale])
        self.current.add(stale)

        def fail_after_remove(path, excluded):
            self.set_excluded(path, excluded)
            raise churnpaths_spotlight.SpotlightError("readback failed")

        with patch.object(
            churnpaths_spotlight, "set_excluded", side_effect=fail_after_remove
        ):
            with self.assertRaises(churnpaths_spotlight.SpotlightError):
                churnpaths.reconcile(self.args, self.result())
        self.assertEqual(self.managed(), [stale])
        churnpaths.reconcile(self.args, self.result())
        self.assertEqual(self.managed(), [])


class IncludeTests(Fixture):
    def test_include_adds_exact_folders_and_skips_bad_entries(self):
        cache = self.folder("work/cache")
        link = self.root / "link"
        link.symlink_to(cache)
        listing = self.root / "generated.txt"
        listing.write_text(
            f"# generated\npath {cache}\npath {self.root}/gone\npath {link}\nglob {self.root}/*\npath relative\n"
        )
        result = self.resolve(f"include {listing}\ninclude {self.root}/absent.txt\n")
        self.assertEqual(result["paths"], [str(cache)])
        self.assertEqual(result["missing"], [])
        self.assertIn(str(link), result["skipped"])
        self.assertIn("relative", result["skipped"])


class ConfigTests(Fixture):
    def env(self, **values):
        return patch.dict(
            churnpaths.os.environ,
            {"CHURNPATHS_SOURCE": "", "CHURNPATHS_CONFIG": "", "CHURNPATHS_STATE": ""}
            | values,
        )

    def test_default_source_without_config(self):
        with self.env(CHURNPATHS_CONFIG=str(self.root / "none.toml")):
            self.assertEqual(
                churnpaths.default_source(), churnpaths.CONFIG_DIR / "churn-paths.txt"
            )

    def test_config_file_sets_source_and_state(self):
        config = self.root / "config.toml"
        config.write_text('source = "~/lists/mine.txt"\nstate = "/tmp/x/state.json"\n')
        with self.env(CHURNPATHS_CONFIG=str(config)):
            self.assertEqual(
                churnpaths.default_source(), Path.home() / "lists/mine.txt"
            )
            self.assertEqual(churnpaths.default_state(), Path("/tmp/x/state.json"))

    def test_environment_beats_config_and_flag_beats_environment(self):
        config = self.root / "config.toml"
        config.write_text('source = "/from/config"\n')
        other = self.folder("flag")
        with self.env(CHURNPATHS_CONFIG=str(config), CHURNPATHS_SOURCE="/from/env"):
            self.assertEqual(churnpaths.default_source(), Path("/from/env"))
            self.source.write_text(f"path {other}\n")
            with patch("sys.stdout") as out:
                self.assertEqual(
                    churnpaths.main(
                        ["-l", "-m", "backblaze", "-j", "-s", str(self.source)]
                    ),
                    0,
                )
            self.assertIn(
                str(other), "".join(c.args[0] for c in out.write.call_args_list)
            )

    def test_bad_config_is_an_error(self):
        config = self.root / "config.toml"
        config.write_text("source = 3\n")
        with self.env(CHURNPATHS_CONFIG=str(config)):
            self.assertEqual(churnpaths.main(["-l"]), 2)

    def test_unknown_directive_and_bad_include_line(self):
        with self.assertRaises(ValueError):
            self.resolve("bogus /x\n")


if __name__ == "__main__":
    unittest.main()
