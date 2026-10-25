#!/usr/bin/env python3
"""Regression tests for the untrusted agent-patch policy."""
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

POLICY = Path(__file__).with_name("validate-agent-patch.py")


def patch_for(path, *, body=None, addition=False, deletion=False, mode=None, old_path=None, index_mode=None):
    old_path = old_path or path
    new_path = path
    header = f"diff --git a/{old_path} b/{new_path}\n"
    if old_path != new_path:
        header += f"similarity index 100%\nrename from {old_path}\nrename to {new_path}\n"
    if mode:
        header += f"{mode}\n"
    if index_mode:
        header += f"index 1111111..2222222 {index_mode}\n"
    if addition:
        header += "--- /dev/null\n"
        header += f"+++ b/{new_path}\n"
    elif deletion:
        header += f"--- a/{old_path}\n+++ /dev/null\n"
    else:
        header += f"--- a/{old_path}\n+++ b/{new_path}\n"
    header += "@@ -1 +1 @@\n-old\n+new\n"
    return header


class PatchPolicyTests(unittest.TestCase):
    def run_policy(self, patch, authorized=""):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "candidate.patch"
            path.write_text(patch, encoding="utf-8")
            return subprocess.run(
                [sys.executable, str(POLICY), str(path), "--authorized-test-paths", authorized],
                capture_output=True,
                text=True,
                check=False,
            )

    def assert_rejected(self, patch, authorized="", message=None):
        result = self.run_policy(patch, authorized)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        if message:
            self.assertIn(message, result.stderr)

    def assert_accepted(self, patch, authorized=""):
        result = self.run_policy(patch, authorized)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_regular_source_change_allowed(self):
        self.assert_accepted(patch_for("internal/example.go"))

    def test_new_regular_file_allowed(self):
        self.assert_accepted(patch_for("internal/new.go", addition=True, mode="new file mode 100644"))

    def test_new_test_file_allowed(self):
        self.assert_accepted(patch_for("internal/example_test.go", addition=True, mode="new file mode 100644"))

    def test_existing_test_change_requires_exact_authorization(self):
        patch = patch_for("internal/example_test.go")
        self.assert_rejected(patch, message="explicit authorization")
        self.assert_accepted(patch, "internal/example_test.go")

    def test_test_deletion_requires_exact_authorization(self):
        patch = patch_for("internal/example_test.go", deletion=True, mode="deleted file mode 100644")
        self.assert_rejected(patch, message="Deletion rejected")
        self.assert_accepted(patch, "internal/example_test.go")

    def test_non_test_deletion_rejected(self):
        self.assert_rejected(
            patch_for("internal/example.go", deletion=True, mode="deleted file mode 100644"),
            message="Deletion rejected",
        )

    def test_ci_workflow_and_codeowners_are_protected(self):
        self.assert_rejected(patch_for(".github/workflows/ci.yml"), message="Protected path")
        self.assert_rejected(patch_for(".github/CODEOWNERS"), message="Protected path")

    def test_policy_script_is_protected(self):
        self.assert_rejected(patch_for(".github/scripts/validate-agent-patch.py"), message="Protected path")

    def test_case_insensitive_protected_paths_are_rejected(self):
        self.assert_rejected(
            patch_for(".GitHub/workflows/ci.yml"),
            message="Protected path",
        )
        self.assert_rejected(
            patch_for(".GITHUB/scripts/test_validate_agent_patch.py"),
            message="Protected path",
        )

    def test_case_insensitive_path_collisions_are_rejected(self):
        patch = patch_for("internal/Widget.go") + patch_for("internal/widget.go")
        self.assert_rejected(patch, message="Case-insensitive path collision")

    def test_rename_rejected(self):
        self.assert_rejected(
            patch_for("internal/new.go", old_path="internal/old.go"),
            message="Renames and copies are not allowed",
        )

    def test_symlink_executable_and_submodule_changes_rejected(self):
        self.assert_rejected(
            patch_for("internal/link", addition=True, mode="new file mode 120000"),
            message="Symlink, executable, or submodule mode rejected",
        )
        self.assert_rejected(
            patch_for("internal/tool", addition=True, mode="new file mode 100755"),
            message="Symlink, executable, or submodule mode rejected",
        )
        self.assert_rejected(
            patch_for("vendor/module", index_mode="160000"),
            message="Executable, symlink, or submodule change rejected",
        )

    def test_binary_patch_rejected(self):
        self.assert_rejected(
            patch_for("internal/data.bin") + "GIT binary patch\nliteral 4\n",
            message="Binary changes are not allowed",
        )

    def test_oversized_patch_rejected(self):
        self.assert_rejected(patch_for("internal/example.go") + ("# padding\n" * 120000), message="byte limit")

    def test_authorization_cannot_name_non_test_or_traversal_paths(self):
        self.assert_rejected(patch_for("internal/example.go"), "../internal/example_test.go", message="Invalid authorized test path")
        self.assert_rejected(patch_for("internal/example.go"), "README.md", message="Only Go *_test.go paths")


if __name__ == "__main__":
    unittest.main(verbosity=2)
