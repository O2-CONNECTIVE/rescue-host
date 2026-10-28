"""ZIP検証CLIの試験。fixture bytesはEXE/backendの実動作証拠ではない。"""

import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[2]
SOURCE = "c2af8288a259dc8e4718c17727e45fddd13b6bb5"
PREFIX = "RescueHostTauriUnwiredEvaluation/"
VERIFY = ROOT / "scripts/verify_tauri_evaluation.py"


class EvaluationVerificationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.folder = Path(temporary.name)
        self.exe = self.folder / "gui.exe"
        self.exe.write_bytes(b"abc")
        licenses = self.folder / "licenses"
        licenses.mkdir()
        (licenses / "runtime.txt").write_text("fixture license", encoding="utf-8")
        result = subprocess.run(
            [sys.executable, "-B", str(ROOT / "scripts/package_tauri_evaluation.py"),
             "--exe", str(self.exe), "--licenses", str(licenses), "--source-sha", SOURCE,
             "--output", str(self.folder / "output")],
            capture_output=True, text=True, timeout=15,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.archive = self.folder / "output/RescueHostTauriUnwiredEvaluation-x64.zip"

    def verify(self, **changes):
        args = {"source": SOURCE,
                "archive-sha256": hashlib.sha256(self.archive.read_bytes()).hexdigest(),
                "exe-sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}
        args.update(changes)
        command = [sys.executable, "-B", str(VERIFY), "--archive", str(self.archive)]
        for name, value in args.items():
            command.extend(["--" + name, value])
        return subprocess.run(command, capture_output=True, text=True, timeout=15)

    def rewrite(self, change):
        with zipfile.ZipFile(self.archive) as bundle:
            entries = {name: bundle.read(name) for name in bundle.namelist()}
        change(entries)
        with zipfile.ZipFile(self.archive, "w") as bundle:
            for name, data in entries.items():
                bundle.writestr(name, data)

    def test_added_backend_is_rejected_even_with_matching_archive_digest(self):
        accepted = self.verify()
        self.assertEqual(accepted.returncode, 0, accepted.stderr)
        self.assertEqual(json.loads(accepted.stdout)["status"], "pass")
        self.rewrite(lambda entries: entries.update({PREFIX + "backend.exe": b"unapproved fixture"}))
        refused = self.verify()
        self.assertNotEqual(refused.returncode, 0)
        self.assertEqual(json.loads(refused.stdout)["code"], "PACKAGE_MEMBER_SET_MISMATCH")
        self.assertEqual(sorted(p.name for p in self.archive.parent.iterdir()),
                         ["RescueHostTauriUnwiredEvaluation-x64.zip",
                          "RescueHostTauriUnwiredEvaluation-x64.zip.sha256"])


if __name__ == "__main__":
    unittest.main()
