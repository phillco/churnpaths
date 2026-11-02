"""Set sticky backup exclusions through Apple's public CFURL resource API.

CFURLSetResourcePropertyForKey writes kCFURLIsExcludedFromBackupKey to the
filesystem. This avoids tmutil addexclusion's extra wait for Spotlight to index
a marker, which can stall when the containing folder is in Search Privacy.
Callers must independently verify with tmutil isexcluded -X: unsupported
resource properties may be ignored without an API error.

Apple documents the key and setter, including ownership of returned errors:
https://developer.apple.com/documentation/corefoundation/kcfurlisexcludedfrombackupkey
https://github.com/apple-oss-distributions/CF/blob/main/CFURL.h

No framework loads until the first write. Only created URLs, copied error
strings, and returned CFError objects are released; exported constants are
borrowed and remain owned by the framework.
"""

from __future__ import annotations

import ctypes
import functools
import os
import sys


class BackupExclusionError(RuntimeError):
    """The public macOS resource API could not set a backup exclusion."""


def _bind(library, name, arguments, result):
    function = getattr(library, name)
    function.argtypes = arguments
    function.restype = result
    return function


class _Backend:
    def __init__(self):
        pointer = ctypes.c_void_p
        self.cf = ctypes.CDLL(
            "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
        )
        # Foundation supplies the filesystem resource-property implementation.
        self.foundation = ctypes.CDLL(
            "/System/Library/Frameworks/Foundation.framework/Foundation"
        )
        self.create_url = _bind(
            self.cf,
            "CFURLCreateFromFileSystemRepresentation",
            [pointer, ctypes.c_char_p, ctypes.c_long, ctypes.c_ubyte],
            pointer,
        )
        self.set_property = _bind(
            self.cf,
            "CFURLSetResourcePropertyForKey",
            [pointer, pointer, pointer, ctypes.POINTER(pointer)],
            ctypes.c_ubyte,
        )
        self.release = _bind(self.cf, "CFRelease", [pointer], None)
        self.copy_error_description = _bind(
            self.cf, "CFErrorCopyDescription", [pointer], pointer
        )
        self.string_length = _bind(
            self.cf, "CFStringGetLength", [pointer], ctypes.c_long
        )
        self.string_capacity = _bind(
            self.cf,
            "CFStringGetMaximumSizeForEncoding",
            [ctypes.c_long, ctypes.c_uint32],
            ctypes.c_long,
        )
        self.string_bytes = _bind(
            self.cf,
            "CFStringGetCString",
            [pointer, ctypes.c_char_p, ctypes.c_long, ctypes.c_uint32],
            ctypes.c_ubyte,
        )
        self.key = pointer.in_dll(self.cf, "kCFURLIsExcludedFromBackupKey").value
        self.true = pointer.in_dll(self.cf, "kCFBooleanTrue").value
        self.false = pointer.in_dll(self.cf, "kCFBooleanFalse").value
        if not all((self.key, self.true, self.false)):
            raise BackupExclusionError(
                "The macOS backup resource constants are unavailable"
            )

    def error_description(self, error):
        if not error:
            return "no error details provided"
        description = self.copy_error_description(error)
        if not description:
            return "no error description provided"
        try:
            size = self.string_capacity(self.string_length(description), 0x08000100) + 1
            if size <= 0 or size > 1024 * 1024:
                return "invalid error description"
            buffer = ctypes.create_string_buffer(size)
            if not self.string_bytes(description, buffer, size, 0x08000100):
                return "could not decode error description"
            return buffer.value.decode("utf-8", errors="replace")
        finally:
            self.release(description)

    def set_excluded(self, path: str, excluded: bool) -> None:
        encoded = os.fsencode(path)
        url = self.create_url(None, encoded, len(encoded), int(os.path.isdir(path)))
        if not url:
            raise BackupExclusionError(f"Could not create a file URL for {path!r}")
        error = ctypes.c_void_p()
        try:
            ok = self.set_property(
                url,
                self.key,
                self.true if excluded else self.false,
                ctypes.byref(error),
            )
            if not ok:
                raise BackupExclusionError(
                    f"Could not set backup exclusion for {path!r}: "
                    f"{self.error_description(error.value)}"
                )
        finally:
            if error.value:
                self.release(error.value)
            self.release(url)


@functools.lru_cache(maxsize=1)
def _load_backend():
    if sys.platform != "darwin":
        raise BackupExclusionError("Backup resource properties require macOS")
    try:
        return _Backend()
    except (OSError, AttributeError) as error:
        raise BackupExclusionError(
            "The macOS backup resource API is unavailable"
        ) from error


def set_excluded(path: str | os.PathLike, excluded: bool = True) -> None:
    """Set one sticky exclusion; caller must verify its effective backup state."""
    raw = os.fspath(path)
    if not isinstance(raw, str) or not os.path.isabs(raw) or "\0" in raw:
        raise ValueError("An absolute filesystem path without NUL bytes is required")
    if not isinstance(excluded, bool):
        raise ValueError("excluded must be a bool")
    _load_backend().set_excluded(raw, excluded)
