"""Owned folder rules in Backblaze's documented editable exclusions file.

https://www.backblaze.com/computer-backup/docs/configure-custom-exclusions-using-xml-mac
Only a marked block belongs to this helper. All other bytes remain unchanged.
"""

from __future__ import annotations

import hashlib
import json
import os
import stat
import subprocess
import tempfile
import time
import xml.etree.ElementTree as ET
from pathlib import Path

DEFAULT_CONFIG = Path("/Library/Backblaze.bzpkg/bzdata/bzexcluderules_editable.xml")
BEGIN = b"<!-- churnpaths managed folders v1 BEGIN -->"
END = b"<!-- churnpaths managed folders v1 END -->"
FIELDS = {
    "plat",
    "osVers",
    "ruleIsOptional",
    "skipFirstCharThenStartsWith",
    "contains_1",
    "contains_2",
    "doesNotContain",
    "endsWith",
    "hasFileExtension",
}


class XMLPolicyError(RuntimeError):
    """Unsafe or unexpectedly changed editable exclusions."""


def attributes(directory):
    path = Path(directory)
    if (
        not path.is_absolute()
        or ".." in path.parts
        or str(path) != directory
        or path in {Path("/"), Path("/Users"), Path("/Volumes"), Path.home()}
        or any(c in directory for c in "*?[]\x00\r\n")
    ):
        raise XMLPolicyError(f"unsafe exact folder prefix: {directory}")
    prefix = directory[1:] + "/"
    if len(prefix) < 4:
        raise XMLPolicyError(f"folder prefix is too short: {directory}")
    return dict(
        plat="mac",
        osVers="*",
        ruleIsOptional="t",
        skipFirstCharThenStartsWith=prefix,
        contains_1="*",
        contains_2="*",
        doesNotContain="*",
        endsWith="*",
        hasFileExtension="*",
    )


def parse(data):
    if b"<!DOCTYPE" in data or b"<!ENTITY" in data:
        raise XMLPolicyError("document types/entities are not accepted")
    try:
        root = ET.fromstring(
            data, parser=ET.XMLParser(target=ET.TreeBuilder(insert_comments=True))
        )
    except ET.ParseError as error:
        raise XMLPolicyError("editable exclusions XML is malformed") from error
    if root.tag != "bzexclusions" or root.attrib or (root.text or "").strip():
        raise XMLPolicyError("unexpected editable exclusions root")
    if data.count(b"</bzexclusions>") != 1:
        raise XMLPolicyError("expected one closing bzexclusions tag")
    counts = (data.count(BEGIN), data.count(END))
    if counts not in {(0, 0), (1, 1)}:
        raise XMLPolicyError("managed block markers are missing or duplicated")
    owned, inside, ended = set(), False, False
    for node in root:
        if (node.tail or "").strip():
            raise XMLPolicyError("unexpected text in editable exclusions")
        if node.tag is ET.Comment:
            # Compare comment text, independently of insignificant outside whitespace.
            text = (node.text or "").strip()
            if text == "churnpaths managed folders v1 BEGIN":
                if inside or ended or counts != (1, 1):
                    raise XMLPolicyError("unexpected managed block start")
                inside = True
            elif text == "churnpaths managed folders v1 END":
                if not inside:
                    raise XMLPolicyError("unexpected managed block end")
                inside, ended = False, True
            elif inside:
                raise XMLPolicyError("unexpected comment inside managed block")
            continue
        if (
            node.tag != "excludefname_rule"
            or list(node)
            or (node.text or "").strip()
            or not FIELDS <= node.attrib.keys()
            or node.attrib.keys() - FIELDS - {"bzmergeblock"}
            or any(not value for value in node.attrib.values())
            or node.get("plat") not in {"mac", "win", "*"}
            or node.get("ruleIsOptional") not in {"t", "f"}
        ):
            raise XMLPolicyError("unexpected editable exclusion rule")
        prefix = node.get("skipFirstCharThenStartsWith")
        if prefix != "*" and len(prefix) < 4:
            raise XMLPolicyError("editable exclusion prefix is too short")
        if inside:
            if not prefix.endswith("/"):
                raise XMLPolicyError("managed folder prefix must end with /")
            directory = "/" + prefix[:-1]
            if node.attrib != attributes(directory) or directory in owned:
                raise XMLPolicyError("managed XML rule was changed or duplicated")
            owned.add(directory)
    if inside or (counts == (1, 1) and not ended):
        raise XMLPolicyError("managed block must be directly inside bzexclusions")
    return owned


def render(data, directories):
    parse(data)
    lines = [BEGIN]
    for directory in sorted(directories):
        lines.append(
            b"    "
            + ET.tostring(
                ET.Element("excludefname_rule", attributes(directory)), encoding="utf-8"
            )
        )
    lines.append(END)
    block = b"\n".join(lines)
    if BEGIN in data:
        start = data.index(BEGIN)
        stop = data.index(END, start) + len(END)
        output = data[:start] + block + data[stop:]
    else:
        point = data.index(b"</bzexclusions>")
        output = data[:point] + block + b"\n" + data[point:]
    if parse(output) != set(directories):
        raise XMLPolicyError("generated managed rules did not verify")
    return output


def signature(info):
    return (
        info.st_dev,
        info.st_ino,
        info.st_size,
        info.st_mtime_ns,
        info.st_uid,
        info.st_gid,
        stat.S_IMODE(info.st_mode),
    )


class XMLPolicy:
    def __init__(self, path=DEFAULT_CONFIG, state_dir=None):
        self.path = Path(path)
        self.state_dir = Path(state_dir or Path.home() / ".local/state/churnpaths")

    def snapshot(self):
        if self.path.is_symlink() or not self.path.is_file():
            raise XMLPolicyError(
                "editable exclusions must be an existing regular file, not a symlink"
            )
        before = self.path.stat()
        data = self.path.read_bytes()
        if signature(before) != signature(self.path.stat()):
            raise XMLPolicyError("editable exclusions changed while reading; retry")
        parse(data)
        return data, before

    def owned(self):
        if not self.path.exists():
            return set()
        data, _ = self.snapshot()
        return parse(data)

    def log(self, event):
        self.state_dir.mkdir(parents=True, exist_ok=True)
        with (self.state_dir / "xml-changes.ndjson").open("a") as handle:
            handle.write(json.dumps({"time": time.time(), **event}) + "\n")
            handle.flush()
            os.fsync(handle.fileno())

    def set(self, directory, excluded):
        self.set_many([directory], excluded)

    def set_many(self, directories, excluded=True):
        directories = set(directories)
        if not directories:
            return
        for directory in directories:
            attributes(directory)
        original, info = self.snapshot()
        owned = parse(original)
        updated = owned | directories if excluded else owned - directories
        if updated == owned:
            return
        output = render(original, updated)
        backups = self.state_dir / "xml-backups"
        backups.mkdir(parents=True, exist_ok=True, mode=0o700)
        backup_fd, backup_name = tempfile.mkstemp(
            prefix="editable-", suffix=".xml", dir=backups
        )
        with os.fdopen(backup_fd, "wb") as backup:
            backup.write(original)
            backup.flush()
            os.fsync(backup.fileno())
        temp_fd, temp_name = tempfile.mkstemp(
            prefix=".churnpaths-", dir=self.path.parent
        )
        temp_path = Path(temp_name)
        event = {
            "path": str(self.path),
            "directories": sorted(directories),
            "excluded": excluded,
            "backup": backup_name,
            "before_sha256": hashlib.sha256(original).hexdigest(),
            "after_sha256": hashlib.sha256(output).hexdigest(),
        }
        if len(directories) == 1:
            event["directory"] = next(iter(directories))
        try:
            with os.fdopen(temp_fd, "wb") as handle:
                handle.write(output)
                handle.flush()
                os.fsync(handle.fileno())
                os.fchmod(handle.fileno(), stat.S_IMODE(info.st_mode))
            staged = temp_path.stat()
            if (staged.st_uid, staged.st_gid) != (info.st_uid, info.st_gid):
                command = [
                    "/usr/bin/sudo",
                    "-n",
                    "/usr/sbin/chown",
                    f"{info.st_uid}:{info.st_gid}",
                    str(temp_path),
                ]
                self.log(
                    {"action": "chown staged editable exclusions", "command": command}
                )
                result = subprocess.run(
                    command, capture_output=True, text=True, timeout=30
                )
                if result.returncode:
                    raise XMLPolicyError(
                        "could not preserve editable exclusions owner/group"
                    )
            staged = temp_path.stat()
            if (staged.st_uid, staged.st_gid, stat.S_IMODE(staged.st_mode)) != (
                info.st_uid,
                info.st_gid,
                stat.S_IMODE(info.st_mode),
            ):
                raise XMLPolicyError(
                    "staged exclusions permissions do not match original"
                )
            # Refuse to replace an editor/native-client change observed since snapshot.
            if (
                signature(self.path.stat()) != signature(info)
                or self.path.is_symlink()
                or self.path.read_bytes() != original
            ):
                raise XMLPolicyError(
                    "editable exclusions changed before replacement; retry"
                )
            self.log({"action": "replace intent", **event})
            # Check again after durable logging and immediately before replacement.
            if (
                signature(self.path.stat()) != signature(info)
                or self.path.read_bytes() != original
            ):
                raise XMLPolicyError(
                    "editable exclusions changed before replacement; retry"
                )
            os.replace(temp_path, self.path)
            actual, actual_info = self.snapshot()
            if (
                actual != output
                or parse(actual) != updated
                or (
                    actual_info.st_uid,
                    actual_info.st_gid,
                    stat.S_IMODE(actual_info.st_mode),
                )
                != (info.st_uid, info.st_gid, stat.S_IMODE(info.st_mode))
            ):
                raise XMLPolicyError(
                    "editable exclusions readback did not match replacement"
                )
            self.log({"action": "replace verified", **event})
        finally:
            temp_path.unlink(missing_ok=True)
