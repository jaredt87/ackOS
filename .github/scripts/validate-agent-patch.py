#!/usr/bin/env python3
"""Validate an untrusted agent patch before a trusted publisher applies it."""
import argparse
import re
import sys
from pathlib import Path

MAX_PATCH_BYTES = 1024 * 1024
PATH_RE = re.compile(r"^[A-Za-z0-9._/-]+$")
TEST_RE = re.compile(r"(^|/)[^/]*_test\.go$")
PROTECTED_EXACT = {
    ".github/CODEOWNERS",
    ".gitmodules",
    ".gitignore",
    ".golangci.yml",
    ".editorconfig",
    ".gitattributes",
    "AGENTS.md",
    "PR_REVIEW_LOOP.md",
    "Makefile",
    "go.mod",
    "go.sum",
    "go.work",
    "go.work.sum",
}
PROTECTED_PREFIXES = (".github/", ".git/")
FORBIDDEN_MODES = {"100755", "120000", "160000"}


def fail(message):
    print(f"::error::{message}", file=sys.stderr)
    raise SystemExit(1)


def parse_authorized(raw):
    result = set()
    for item in re.split(r"[,\n]", raw or ""):
        path = item.strip()
        if not path:
            continue
        if (
            not PATH_RE.fullmatch(path)
            or path.startswith("/")
            or any(part in {".", "..", ".git"} for part in path.split("/"))
        ):
            fail(f"Invalid authorized test path: {path!r}")
        if not TEST_RE.search(path):
            fail(f"Only Go *_test.go paths can be authorized: {path}")
        result.add(path)
    return result


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("patch")
    parser.add_argument("--authorized-test-paths", default="")
    args = parser.parse_args()
    authorized = parse_authorized(args.authorized_test_paths)

    try:
        raw = Path(args.patch).read_bytes()
    except OSError as exc:
        fail(f"Cannot read patch: {exc}")
    if not raw or len(raw) > MAX_PATCH_BYTES:
        fail(f"Patch must be non-empty and no larger than {MAX_PATCH_BYTES} bytes")
    if b"\x00" in raw:
        fail("NUL byte / binary patch rejected")
    try:
        patch = raw.decode("utf-8")
    except UnicodeDecodeError:
        fail("Patch must be UTF-8 text")
    if "GIT binary patch" in patch or "Binary files " in patch:
        fail("Binary changes are not allowed")

    chunks = re.split(r"(?m)(?=^diff --git )", patch)
    if not chunks or chunks[0].strip():
        fail("Malformed patch preamble")
    chunks = [chunk for chunk in chunks if chunk.strip()]
    if not chunks:
        fail("Empty patch")
    paths_seen = set()

    for chunk in chunks:
        lines = chunk.splitlines()
        match = re.fullmatch(r"diff --git a/([^ ]+) b/([^ ]+)", lines[0])
        if not match:
            fail("Quoted, ambiguous, or malformed diff paths are rejected")
        old_path, new_path = match.group(1), match.group(2)
        for path in (old_path, new_path):
            if (
                not PATH_RE.fullmatch(path)
                or path.startswith("/")
                or any(part in {".", "..", ".git"} for part in path.split("/"))
            ):
                fail(f"Unsafe or ambiguous path rejected: {path!r}")
            if path in PROTECTED_EXACT or any(path.startswith(prefix) for prefix in PROTECTED_PREFIXES):
                fail(f"Protected path rejected: {path}")
        if old_path != new_path:
            fail(f"Renames are not allowed: {old_path} -> {new_path}")
        if old_path in paths_seen:
            fail(f"Duplicate path in patch: {old_path}")
        paths_seen.add(old_path)

        is_addition = "--- /dev/null" in lines
        is_deletion = "+++ /dev/null" in lines
        if is_addition and is_deletion:
            fail(f"Malformed add/delete patch for {old_path}")
        if any(line.startswith(("rename from ", "rename to ", "copy from ", "copy to ")) for line in lines):
            fail("Renames and copies are not allowed")
        if any(line.startswith(("old mode ", "new mode ")) for line in lines):
            fail(f"Mode changes are not allowed: {old_path}")
        for line in lines:
            if line.startswith("index ") and re.search(r" (100755|120000|160000)$", line):
                fail(f"Executable, symlink, or submodule change rejected: {old_path}")
            if line.startswith(("new file mode ", "deleted file mode ")):
                mode = line.rsplit(" ", 1)[-1]
                if mode in FORBIDDEN_MODES:
                    fail(f"Symlink, executable, or submodule mode rejected: {old_path}")
                if line.startswith("new file mode ") and mode != "100644":
                    fail(f"Only regular non-executable files may be added: {old_path}")
                if line.startswith("deleted file mode ") and not (TEST_RE.search(old_path) and old_path in authorized):
                    fail(f"Deletion rejected: {old_path}")

        if is_deletion and not (TEST_RE.search(old_path) and old_path in authorized):
            fail(f"Deletion rejected: {old_path}")
        if TEST_RE.search(old_path) and not is_addition and old_path not in authorized:
            fail(f"Test modification/deletion requires explicit authorization: {old_path}")
        if TEST_RE.search(new_path) and not is_addition and new_path not in authorized:
            fail(f"Test modification requires explicit authorization: {new_path}")

        old_headers = [line[6:] for line in lines if line.startswith("--- a/")]
        new_headers = [line[6:] for line in lines if line.startswith("+++ b/")]
        if is_addition:
            if old_headers or new_headers != [new_path]:
                fail(f"Malformed addition headers: {old_path}")
        elif is_deletion:
            if old_headers != [old_path] or new_headers:
                fail(f"Malformed deletion headers: {old_path}")
        elif old_headers != [old_path] or new_headers != [new_path]:
            fail(f"Patch file headers do not match metadata: {old_path}")

    print(f"Patch policy accepted: {len(paths_seen)} file(s)")


if __name__ == "__main__":
    main()
