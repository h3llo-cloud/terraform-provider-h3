"""github-sync.sh against a local bare repo standing in for GitHub: private paths are not published,
re-run is idempotent, a moved tag is refused."""
import os
import shutil
import subprocess
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "github-sync.sh")


def git(cwd, *args, env=None):
    return subprocess.run(["git", *args], cwd=cwd, env=env, check=True, capture_output=True, text=True).stdout.strip()


@unittest.skipUnless(shutil.which("git"), "git is required")
class GithubSyncTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.gh = os.path.join(self.tmp, "gh.git")
        self.src = os.path.join(self.tmp, "src")
        git(self.tmp, "init", "-q", "--bare", "-b", "main", self.gh)
        seed = os.path.join(self.tmp, "seed")
        git(self.tmp, "init", "-q", "-b", "main", seed)
        open(os.path.join(seed, "README.md"), "w").write("public\n")
        git(seed, "add", ".")
        git(seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
        git(seed, "push", "-q", self.gh, "main")
        git(self.tmp, "init", "-q", "-b", "main", self.src)
        for name, body in {"main.go": "package main\n", ".gitlab-ci.yml": "x: 1\n",
                           ".gitleaks-baseline.json": "[]\n", ".gitlab/ci/a.yml": "y: 2\n"}.items():
            path = os.path.join(self.src, name)
            os.makedirs(os.path.dirname(path), exist_ok=True)
            open(path, "w").write(body)
        git(self.src, "add", ".")
        git(self.src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "internal message")
        git(self.src, "tag", "v9.9.9")
        self.env = dict(os.environ, GITHUB_TOKEN="dummy", GITHUB_URL="file://" + self.gh)

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def sync(self, tag="v9.9.9"):
        return subprocess.run(["sh", SCRIPT, tag], cwd=self.src, env=self.env, capture_output=True, text=True)

    def test_private_paths_not_published(self):
        r = self.sync()
        self.assertEqual(r.returncode, 0, r.stderr)
        files = git(self.gh, "ls-tree", "-r", "--name-only", "v9.9.9").splitlines()
        self.assertEqual(files, ["main.go"])
        self.assertEqual(git(self.gh, "log", "-1", "--format=%s", "v9.9.9"), "Release v9.9.9")
        self.assertEqual(git(self.gh, "rev-parse", "main"), git(self.gh, "rev-parse", "v9.9.9^{commit}"))

    def test_rerun_is_idempotent(self):
        self.assertEqual(self.sync().returncode, 0)
        r = self.sync()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("same tree", r.stdout)

    def test_moved_tag_refused(self):
        self.assertEqual(self.sync().returncode, 0)
        open(os.path.join(self.src, "main.go"), "w").write("package main // changed\n")
        git(self.src, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-am", "change")
        git(self.src, "tag", "-f", "v9.9.9")
        r = self.sync()
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("ANOTHER tree", r.stderr)


if __name__ == "__main__":
    unittest.main()
