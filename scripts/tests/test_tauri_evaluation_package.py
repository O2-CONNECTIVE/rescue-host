"""配布ファイル境界の試験。fixture bytes は Windows 実行の証拠ではない。"""

import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[2]
COMMAND = ROOT / "scripts/package_tauri_evaluation.py"
SOURCE = "6d5211bc76eec175c9fc53f40a26eb31c5502d1d"


class EvaluationPackageTests(unittest.TestCase):
    def test_rust_standard_runtime_notices_are_in_the_collected_distribution(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            (folder / "LICENSE").write_text("crate fixture license", encoding="utf-8")
            docs = folder / "rust-docs"
            docs.mkdir()
            for name in ["COPYRIGHT", "LICENSE-APACHE", "LICENSE-MIT"]:
                (docs / name).write_text("Rust fixture " + name, encoding="utf-8")
            (docs / "COPYRIGHT").rename(docs / "COPYRIGHT.html")
            metadata = folder / "metadata.json"
            metadata.write_text(json.dumps({"resolve": {"root": "fixture", "nodes": [{"id": "fixture"}]},
                                           "packages": [{"id": "fixture", "name": "fixture", "version": "1.0.0",
                                                         "license": "MIT", "source": "registry+https://github.com/rust-lang/crates.io-index",
                                                         "manifest_path": str(folder / "Cargo.toml")}]}), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, "-B", str(ROOT / "scripts/collect_tauri_licenses.py"),
                 "--metadata", str(metadata), "--frontend-notice", str(folder / "LICENSE"),
                 "--rust-docs", str(docs), "--output", str(folder / "output")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((folder / "output/rust-toolchain/LICENSE-MIT").read_text(),
                             "Rust fixture LICENSE-MIT")

    def test_package_license_scope_follows_the_filtered_windows_graph(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            (folder / "LICENSE").write_text("fixture license", encoding="utf-8")
            packages = [{"id": "windows-fixture", "name": "fixture", "version": "1.0.0",
                         "license": "MIT", "source": "registry+https://github.com/rust-lang/crates.io-index",
                         "manifest_path": str(folder / "Cargo.toml")},
                        {"id": "other-os", "name": "other-os", "version": "1.0.0",
                         "license": None, "source": "registry+https://github.com/rust-lang/crates.io-index",
                         "manifest_path": str(folder / "not-used/Cargo.toml")}]
            metadata = folder / "metadata.json"
            metadata.write_text(json.dumps({"packages": packages,
                                           "resolve": {"root": "windows-fixture", "nodes": [
                                               {"id": "windows-fixture", "dependencies": []},
                                               {"id": "other-os", "dependencies": []}]}}), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, "-B", str(ROOT / "scripts/collect_tauri_licenses.py"),
                 "--metadata", str(metadata), "--frontend-notice", str(folder / "LICENSE"),
                 "--output", str(folder / "output")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            dependencies = json.loads((folder / "output/DEPENDENCIES.json").read_text(encoding="utf-8"))
            self.assertEqual([item["name"] for item in dependencies["packages"]], ["fixture"])

    def test_missing_crate_text_uses_only_its_fixed_upstream_revision(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            (folder / "NOTICE").write_text("fixture frontend notice", encoding="utf-8")
            (folder / ".cargo_vcs_info.json").write_text(json.dumps({
                "git": {"sha1": "0a81fd6928ea3b33c8cd484aa4575d50ffb98012"},
                "path_in_vcs": "alloc-stdlib",
            }), encoding="utf-8")
            metadata = folder / "metadata.json"
            metadata.write_text(json.dumps({"resolve": {"root": "fixture", "nodes": [{"id": "fixture"}]}, "packages": [{
                "id": "fixture",
                "name": "alloc-stdlib", "version": "0.3.0", "license": "BSD-3-Clause",
                "source": "registry+https://github.com/rust-lang/crates.io-index",
                "manifest_path": str(folder / "Cargo.toml"),
            }]}), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, "-B", str(ROOT / "scripts/collect_tauri_licenses.py"),
                 "--metadata", str(metadata), "--frontend-notice", str(folder / "NOTICE"),
                 "--output", str(folder / "output")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            license_file = folder / "output/rust/alloc-stdlib-0.3.0/UPSTREAM-LICENSE"
            self.assertEqual(hashlib.sha256(license_file.read_bytes()).hexdigest(),
                             "c0c56f26d9c051cac4d200c34c84e7ae9aaa853e01a982a1df08b09931e518ae")
            (folder / ".cargo_vcs_info.json").write_text(json.dumps({
                "git": {"sha1": "0" * 40}, "path_in_vcs": "alloc-stdlib",
            }), encoding="utf-8")
            refused = subprocess.run(
                [sys.executable, "-B", str(ROOT / "scripts/collect_tauri_licenses.py"),
                 "--metadata", str(metadata), "--frontend-notice", str(folder / "NOTICE"),
                 "--output", str(folder / "refused")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertNotEqual(refused.returncode, 0)
            self.assertIn("UPSTREAM_LICENSE_IDENTITY_MISMATCH", refused.stderr)
            self.assertFalse((folder / "refused").exists())

    def test_notice_only_crate_cannot_supply_the_required_license(self):
        # 独立レビュー修正の回帰。先行実装後の追加であり TDD とは呼ばない。
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            (folder / "NOTICE").write_text("fixture notice", encoding="utf-8")
            metadata = folder / "metadata.json"
            metadata.write_text(json.dumps({"resolve": {"root": "fixture", "nodes": [{"id": "fixture"}]}, "packages": [{
                "id": "fixture",
                "name": "fixture", "version": "1.0.0", "license": "MIT",
                "source": "registry+https://github.com/rust-lang/crates.io-index",
                "manifest_path": str(folder / "Cargo.toml"),
            }]}), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, "-B", str(ROOT / "scripts/collect_tauri_licenses.py"),
                 "--metadata", str(metadata), "--frontend-notice", str(folder / "NOTICE"),
                 "--output", str(folder / "output")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("CRATE_LICENSE_MISSING:fixture-1.0.0", result.stderr)
            self.assertFalse((folder / "output").exists())

    def test_empty_license_directories_cannot_be_distributed(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            executable = folder / "rescue-host-shell.exe"
            executable.write_bytes(b"abc")
            licenses = folder / "licenses"
            (licenses / "empty").mkdir(parents=True)
            result = subprocess.run(
                [sys.executable, "-B", str(COMMAND), "--exe", str(executable),
                 "--licenses", str(licenses), "--source-sha", SOURCE,
                 "--output", str(folder / "output")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((folder / "output").exists())

    def test_package_does_not_collect_a_license_symlink_outside_its_input(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            executable = folder / "rescue-host-shell.exe"
            executable.write_bytes(b"abc")
            private = folder / "private.txt"
            private.write_text("not distribution material", encoding="utf-8")
            licenses = folder / "licenses"
            licenses.mkdir()
            (licenses / "linked.txt").symlink_to(private)
            result = subprocess.run(
                [sys.executable, "-B", str(COMMAND), "--exe", str(executable),
                 "--licenses", str(licenses), "--source-sha", SOURCE,
                 "--output", str(folder / "output")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse((folder / "output").exists())

    def test_evaluator_receives_source_bound_unwired_package_and_hashes(self):
        with tempfile.TemporaryDirectory() as temporary:
            folder = Path(temporary)
            executable = folder / "rescue-host-shell.exe"
            executable.write_bytes(b"abc")
            licenses = folder / "licenses"
            licenses.mkdir()
            (licenses / "runtime.txt").write_text("fixture license\n", encoding="utf-8")
            result = subprocess.run(
                [sys.executable, "-B", str(COMMAND), "--exe", str(executable),
                 "--licenses", str(licenses), "--source-sha", SOURCE,
                 "--output", str(folder / "output")],
                capture_output=True, text=True, timeout=15,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            archive = folder / "output/RescueHostTauriUnwiredEvaluation-x64.zip"
            with zipfile.ZipFile(archive) as bundle:
                prefix = "RescueHostTauriUnwiredEvaluation/"
                manifest = json.loads(bundle.read(prefix + "MANIFEST.json"))
                self.assertEqual(manifest["sourceCommit"], SOURCE)
                self.assertEqual(manifest["integration"], "not_connected")
                self.assertEqual(manifest["signature"], "unsigned")
                self.assertFalse(manifest["canStart"])
                self.assertFalse(manifest["canStop"])
                self.assertTrue(manifest["productionGateClosed"])
                self.assertEqual(manifest["files"]["rescue-host-shell.exe"],
                                 "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
                self.assertIn(prefix + "README_JA.txt", bundle.namelist())
                self.assertIn(prefix + "licenses/runtime.txt", bundle.namelist())
            digest = hashlib.sha256(archive.read_bytes()).hexdigest()
            self.assertEqual((archive.with_suffix(".zip.sha256")).read_text().strip(),
                             digest + "  " + archive.name)


if __name__ == "__main__":
    unittest.main()
