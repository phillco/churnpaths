"""Isolated API, failure, and Core Foundation lifetime checks; no live writes."""

import ctypes
import plistlib
import unittest
from unittest.mock import Mock, patch

import churnpaths_spotlight as spotlight


class PublicAdapterTests(unittest.TestCase):
    def test_reads_without_mutation(self):
        backend = Mock()
        backend.exclusions.return_value = ["/existing"]
        with patch.object(spotlight, "_load_backend", return_value=backend):
            self.assertEqual(spotlight.exclusions(), ["/existing"])
        backend.set_excluded.assert_not_called()

    def test_add_and_remove_confirm_readback(self):
        for excluded, readback in [(True, ["/test"]), (False, [])]:
            backend = Mock()
            backend.exclusions.return_value = readback
            with patch.object(spotlight, "_load_backend", return_value=backend):
                spotlight.set_excluded("/test", excluded)
            backend.set_excluded.assert_called_once_with("/test", excluded)

    def test_readback_mismatch_is_an_error(self):
        backend = Mock()
        backend.exclusions.return_value = []
        with patch.object(spotlight, "_load_backend", return_value=backend):
            with self.assertRaisesRegex(spotlight.SpotlightError, "readback"):
                spotlight.set_excluded("/test", True)

    def test_api_failure_does_not_claim_success(self):
        backend = Mock()
        backend.set_excluded.side_effect = spotlight.SpotlightError("rejected")
        with patch.object(spotlight, "_load_backend", return_value=backend):
            with self.assertRaisesRegex(spotlight.SpotlightError, "rejected"):
                spotlight.set_excluded("/test", True)
        backend.exclusions.assert_not_called()

    def test_bad_paths_never_load_framework(self):
        with patch.object(spotlight, "_load_backend") as load:
            for path in ["relative", "/nul\0path", None]:
                with self.assertRaises(ValueError):
                    spotlight.set_excluded(path, True)
            with self.assertRaises(ValueError):
                spotlight.set_excluded("/test", 1)
        load.assert_not_called()

    def test_unsupported_platform_is_actionable(self):
        spotlight._load_backend.cache_clear()
        with patch.object(spotlight.sys, "platform", "linux"):
            with self.assertRaisesRegex(spotlight.SpotlightError, "macOS"):
                spotlight._load_backend()
        spotlight._load_backend.cache_clear()

    def test_missing_private_symbol_is_actionable(self):
        spotlight._load_backend.cache_clear()
        with patch.object(spotlight.sys, "platform", "darwin"):
            with patch.object(
                spotlight, "_Backend", side_effect=AttributeError("symbol")
            ):
                with self.assertRaisesRegex(spotlight.SpotlightError, "unavailable"):
                    spotlight._load_backend()
        spotlight._load_backend.cache_clear()


class CoreFoundationLifetimeTests(unittest.TestCase):
    def make_backend(self, value):
        backend = object.__new__(spotlight._Backend)
        content = plistlib.dumps(value)
        backend.buffer = ctypes.create_string_buffer(content)
        backend.copy = Mock(return_value=10)
        backend.create_data = Mock(return_value=20)
        backend.data_length = Mock(return_value=len(content))
        backend.data_bytes = Mock(return_value=ctypes.addressof(backend.buffer))
        backend.release = Mock()
        return backend

    def test_unicode_paths_and_owned_objects_release(self):
        backend = self.make_backend(["/café/test", "/with spaces"])
        self.assertEqual(backend.exclusions(), ["/café/test", "/with spaces"])
        self.assertEqual(
            backend.release.call_args_list,
            [unittest.mock.call(20), unittest.mock.call(10)],
        )

    def test_invalid_types_still_release_owned_objects(self):
        backend = self.make_backend({"wrong": "type"})
        with self.assertRaises(spotlight.SpotlightError):
            backend.exclusions()
        self.assertEqual(backend.release.call_count, 2)

    def test_malformed_xml_still_releases_owned_objects(self):
        backend = self.make_backend([])
        backend.buffer = ctypes.create_string_buffer(
            b"<?xml version='1.0'?><plist><array>"
        )
        backend.data_bytes.return_value = ctypes.addressof(backend.buffer)
        backend.data_length.return_value = len(backend.buffer) - 1
        with self.assertRaisesRegex(spotlight.SpotlightError, "malformed"):
            backend.exclusions()
        self.assertEqual(backend.release.call_count, 2)

    def test_failed_serialization_releases_array_only(self):
        backend = self.make_backend([])
        backend.create_data.return_value = None
        with self.assertRaises(spotlight.SpotlightError):
            backend.exclusions()
        backend.release.assert_called_once_with(10)

    def test_rejected_write_releases_string(self):
        backend = object.__new__(spotlight._Backend)
        backend.create_string = Mock(return_value=30)
        backend.set = Mock(return_value=-1)
        backend.release = Mock()
        with self.assertRaisesRegex(spotlight.SpotlightError, "status -1"):
            backend.set_excluded("/test", True)
        backend.release.assert_called_once_with(30)
        backend.set.assert_called_once_with(30, 1)


if __name__ == "__main__":
    unittest.main()
