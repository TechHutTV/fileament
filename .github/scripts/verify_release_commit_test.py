import subprocess
import tempfile
import unittest
from pathlib import Path

from verify_release_commit import verify_release_commit


class ReleaseCommitTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.remote = self.root / "remote.git"
        self.checkout = self.root / "checkout"
        subprocess.run(["git", "init", "--bare", "--quiet", str(self.remote)], check=True)
        subprocess.run(["git", "init", "--quiet", str(self.checkout)], check=True)
        self.git("config", "user.name", "Test Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        self.git("config", "tag.gpgsign", "false")
        self.git("remote", "add", "origin", str(self.remote))
        self.git("commit", "--quiet", "--allow-empty", "-m", "fixture")
        self.commit = self.git("rev-parse", "HEAD")

    def git(self, *args: str) -> str:
        return subprocess.check_output(["git", *args], cwd=self.checkout, text=True, stderr=subprocess.PIPE).strip()

    def publish_fixture_tag(self, annotated: bool = False) -> None:
        if annotated:
            self.git("tag", "-a", "v1.2.3", "-m", "fixture tag")
        else:
            self.git("tag", "v1.2.3")
        self.git("push", "--quiet", "origin", "refs/tags/v1.2.3")

    def test_accepts_lightweight_tag_at_validated_commit(self) -> None:
        self.publish_fixture_tag()
        verify_release_commit("v1.2.3", self.commit, self.checkout)

    def test_accepts_annotated_tag_at_validated_commit(self) -> None:
        self.publish_fixture_tag(annotated=True)
        verify_release_commit("v1.2.3", self.commit, self.checkout)

    def test_rejects_unvalidated_checkout(self) -> None:
        self.publish_fixture_tag()
        self.git("commit", "--quiet", "--allow-empty", "-m", "unvalidated")
        with self.assertRaisesRegex(ValueError, "checked-out commit"):
            verify_release_commit("v1.2.3", self.commit, self.checkout)

    def test_rejects_tag_moved_after_validation(self) -> None:
        self.publish_fixture_tag(annotated=True)
        self.git("commit", "--quiet", "--allow-empty", "-m", "moved tag")
        self.git("tag", "-f", "v1.2.3")
        self.git("push", "--quiet", "--force", "origin", "refs/tags/v1.2.3")
        self.git("checkout", "--quiet", self.commit)
        with self.assertRaisesRegex(ValueError, "no longer points"):
            verify_release_commit("v1.2.3", self.commit, self.checkout)

    def test_rejects_deleted_remote_tag(self) -> None:
        self.publish_fixture_tag()
        self.git("push", "--quiet", "--delete", "origin", "v1.2.3")
        with self.assertRaises(subprocess.CalledProcessError):
            verify_release_commit("v1.2.3", self.commit, self.checkout)

    def test_rejects_missing_or_abbreviated_validation_sha(self) -> None:
        for commit in ("", self.commit[:7], "main"):
            with self.subTest(commit=commit), self.assertRaises(ValueError):
                verify_release_commit("v1.2.3", commit, self.checkout)


if __name__ == "__main__":
    unittest.main()
