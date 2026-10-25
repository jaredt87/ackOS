#!/usr/bin/env python3
"""Validate an untrusted agent patch before the trusted publisher applies it."""
import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

MAX_PATCH_BYTES = 1024 * 1024
PATH_RE = re.compile(r"^[A-Za-z0-9._/-]+$")
TEST_RE = re.compile(r"(^|/)[^/]*_test\.go$")
PROTECTED_EXACT = {
    ".github/CODEOWNERS",
    "AGENTS.md",
    "PR_REVIEW_LOOP.md",
    "go.mod",
    "go.sum",
}
PROTECTED_PREFIXES = (".github/",)
FORBIDDEN_MODES = {"100755", "120000", "160000"}


def fail(message):
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def parse_authorized(raw):
    paths = set()
    for entry in re.split(r"[,\n]", raw or ""):
        path = entry.strip()
        if not path:
            continue
        if not PATH_RE.fullmatch(path) or path.startswith("/") or ".." in path.split("/"):
            fail(f"Invalid authorized test path: {path!r}")
        if not TEST_RE.search(path):
            fail(f"Only Go *_test.go paths can be authorized: {path}")
        paths.add(path)
    return paths


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("patch")
    parser.add_argument("--authorized-test-paths", default="")
    args = parser.parse_args()

    patch_path = Path(args.patch)
    try:
        raw = patch_path.read_bytes()
    except OSError as exc:
        fail(f"Cannot read patch: {exc}")
    if not raw:
        fail("Empty patch")
    if len(raw) > MAX_PATCH_BYTES:
        fail(f"Patch exceeds {MAX_PATCH_BYTES} byte limit")
    if b"\x00" in raw:
        fail("NUL byte / binary patch rejected")
    try:
        patch = raw.decode("utf-8")
    except UnicodeDecodeError:
        fail("Patch must be UTF-8 text")
    if "GIT binary patch" in patch or "Binary files " in patch:
        fail("Binary changes are not allowed")

    authorized = parse_authorized(args.authorized_test_paths)
    files = []
    current = None
    old_path = new_path = None
    modes = []

    for line in patch.splitlines():
        if line.startswith("diff --git "):
            if current is not None:
                files.append((current, old_path, new_path, modes))
            match = re.fullmatch(r"diff --git a/([^ ]+) b/([^ ]+)", line)
            if not match:
                fail("Quoted or ambiguous diff paths are rejected")
            old_path, new_path = match.group(1), match.group(2)
            current = old_path
            modes = []
            if not PATH_RE.fullmatch(old_path) or not PATH_RE.fullmatch(new_path):
                fail("Non-portable or ambiguous path rejected")
            if old_path.startswith("/") or new_path.startswith("/") or ".." in old_path.split("/") or ".." in new_path.split("/"):
                fail("Absolute or traversal path rejected")
        elif current is not None:
            if line.startswith(("rename from ", "rename to ", "copy from ", "copy to ")):
                fail("Renames and copies are not allowed")
            if line.startswith(("old mode ", "new mode ", "deleted file mode ")):
                modes.append(line)
                mode = line.rsplit(" ", 1)[-1]
                if mode in FORBIDDEN_MODES:
                    fail(f"Symlink, executable-bit, or submodule mode rejected: {line}")
                if line.startswith("old mode ") or line.startswith("deleted file mode "):
                    fail(f"Mode changes and deletions are not allowed: {line}")
                if line.startswith("new mode ") and mode != "100644":
                    fail(f"Only regular non-executable files may be added: {line}")
            if line == "+++ /dev/null":
                # Deletions are only permitted for explicitly authorized test files.
                if not TEST_RE.search(old_path) or old_path not in authorized:
                    fail(f"Deletion rejected: {old_path}")
            if line.startswith("+++ b/"):
                declared = line[6:]
                if declared != new_path:
                    fail("Patch file headers do not match diff metadata")
            if line.startswith("--- a/"):
                declared = line[6:]
                if declared != old_path:
                    fail("Patch file headers do not match diff metadata")

    if current is not None:
        files.append((current, old_path, new_path, modes))
    if not files:
        fail("No parseable file changes in patch")

    seen = set()
    for old, old_name, new_name, _ in files:
        for path in {old_name, new_name}:
            if path in seen:
                fail(f"Duplicate path in patch: {path}")
            seen.add(path)
            if path in PROTECTED_EXACT or any(path.startswith(prefix) for prefix in PROTECTED_PREFIXES):
                fail(f"Protected path rejected: {path}")
            if TEST_RE.search(path):
                # New test files are allowed. Existing/changed tests require explicit authorization.
                is_new = old_name == new_name and not any(
                    ln == f"new file mode 100644" for ln in []
                )
                if path not in authorized:
                    # Determine addition by the patch's /dev/null old header.
                    chunk_start = patch.find(f"diff --git a/{old_name} b/{new_name}")
                    chunk_end = patch.find("\ndiff --git ", chunk_start + 1)
                    chunk = patch[chunk_start:chunk_end if chunk_end >= 0 else None]
                    is_added = "--- /dev/null" in chunk
                    if not is_added:
                        fail(f"Test modification requires explicit authorization: {path}")

    # A patch can only create regular files; verify the resulting worktree separately
    # after application as defense in depth.
    print(f"Patch policy accepted: {len(files)} file(s)")


if __name__ == "__main__":
    main()
