"""Small, lazy adapter to the macOS Search Privacy backend (private API).

Evidence on macOS 27.0 (26A428): SpotlightPreferenceExtension imports both
_MDCopyExclusionList and _MDSetExclusion. Its arm64 disassembly passes a path
string and 1 when adding, or 0 when removing; it checks the integer result for
zero. Reproduce with:

    nm -u /System/Library/ExtensionKit/Extensions/SpotlightPreferenceExtension.appex/Contents/MacOS/SpotlightPreferenceExtension
    otool -tvV -arch arm64e <same executable>

This is not a public Apple API. A future macOS release may remove it or change
its behavior. Fail closed on unavailable symbols, malformed data, API errors,
or missing readback; never edit VolumeConfiguration.plist as a fallback.

Copy/Create functions return retained Core Foundation objects. Every such
object owned here is released in a finally block; borrowed data bytes are read
before their containing CFData is released. No framework loads at import time.
"""

from __future__ import annotations

import ctypes
import functools
import os
import plistlib
import sys
from xml.parsers.expat import ExpatError


class SpotlightError(RuntimeError):
    """The Search Privacy backend could not complete or verify an operation."""


def _bind(library, name, arguments, result):
    function = getattr(library, name)
    function.argtypes = arguments
    function.restype = result
    return function


class _Backend:
    def __init__(self):
        pointer = ctypes.c_void_p
        self.metadata = ctypes.CDLL(
            "/System/Library/Frameworks/CoreServices.framework/Frameworks/"
            "Metadata.framework/Metadata"
        )
        self.cf = ctypes.CDLL(
            "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
        )
        self.copy = _bind(self.metadata, "_MDCopyExclusionList", [], pointer)
        self.set = _bind(
            self.metadata, "_MDSetExclusion", [pointer, ctypes.c_uint], ctypes.c_int
        )
        self.release = _bind(self.cf, "CFRelease", [pointer], None)
        self.create_string = _bind(
            self.cf,
            "CFStringCreateWithCString",
            [pointer, ctypes.c_char_p, ctypes.c_uint32],
            pointer,
        )
        self.create_data = _bind(
            self.cf,
            "CFPropertyListCreateData",
            [pointer, pointer, ctypes.c_long, ctypes.c_ulong, pointer],
            pointer,
        )
        self.data_length = _bind(self.cf, "CFDataGetLength", [pointer], ctypes.c_long)
        self.data_bytes = _bind(self.cf, "CFDataGetBytePtr", [pointer], pointer)

    def exclusions(self) -> list[str]:
        array = self.copy()
        if not array:
            raise SpotlightError("Search Privacy returned no exclusion list")
        try:
            data = self.create_data(None, array, 100, 0, None)  # XML plist format
            if not data:
                raise SpotlightError("Search Privacy list is not a property list")
            try:
                length = self.data_length(data)
                address = self.data_bytes(data)
                if length < 0 or length > 16 * 1024 * 1024 or not address:
                    raise SpotlightError("Search Privacy returned invalid list data")
                try:
                    paths = plistlib.loads(ctypes.string_at(address, length))
                except (
                    ValueError,
                    TypeError,
                    plistlib.InvalidFileException,
                    ExpatError,
                ) as error:
                    raise SpotlightError(
                        "Search Privacy returned malformed list data"
                    ) from error
            finally:
                self.release(data)
        finally:
            self.release(array)
        if not isinstance(paths, list) or any(not isinstance(p, str) for p in paths):
            raise SpotlightError("Search Privacy returned unexpected exclusion types")
        return paths

    def set_excluded(self, path: str, excluded: bool) -> None:
        string = self.create_string(None, path.encode("utf-8"), 0x08000100)
        if not string:
            raise SpotlightError("Could not encode the exclusion path")
        try:
            result = self.set(string, int(excluded))
        finally:
            self.release(string)
        if result != 0:
            raise SpotlightError(f"Search Privacy rejected {path!r} (status {result})")


@functools.lru_cache(maxsize=1)
def _load_backend() -> _Backend:
    if sys.platform != "darwin":
        raise SpotlightError("Search Privacy is only available on macOS")
    try:
        return _Backend()
    except (AttributeError, OSError) as error:
        raise SpotlightError("The macOS Search Privacy API is unavailable") from error


def exclusions() -> list[str]:
    """Return the live Search Privacy list without changing it."""
    return _load_backend().exclusions()


def set_excluded(path: str, excluded: bool) -> None:
    """Add/remove one absolute path and verify the live backend's readback.

    This never renames data, restarts mds, or changes another exclusion. The
    caller owns backups, change logging, and any rollback policy. API readback
    confirms policy registration; use an mdfind canary to verify indexing.
    """
    if not isinstance(path, str) or not os.path.isabs(path) or "\0" in path:
        raise ValueError("An absolute path without NUL bytes is required")
    if not isinstance(excluded, bool):
        raise ValueError("excluded must be a bool")
    backend = _load_backend()
    backend.set_excluded(path, excluded)
    if (path in backend.exclusions()) != excluded:
        raise SpotlightError(f"Search Privacy readback did not confirm {path!r}")
