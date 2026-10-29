"""ZIP検証CLIの試験。fixture bytesはEXE/backendの実動作証拠ではない。"""

import hashlib
import json
from pathlib import Path
import subprocess
import struct
import sys
import tempfile
import unittest
import warnings
import zipfile

from evaluation_package_fixture import write_license_fixture


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
        write_license_fixture(licenses)
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

    def test_non_distribution_compression_is_rejected_before_reading_members(self):
        # 未許可methodをheaderに宣言するfixture。LZMA実装を必要とせず、本文読取前の拒否を試す。
        data = bytearray(self.archive.read_bytes())
        self.assertEqual(data[:4], b"PK\x03\x04")
        central = data.index(b"PK\x01\x02")
        struct.pack_into("<H", data, 8, zipfile.ZIP_LZMA)
        struct.pack_into("<H", data, central + 10, zipfile.ZIP_LZMA)
        self.archive.write_bytes(data)
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stderr, "")
        self.assertEqual(json.loads(result.stdout)["code"], "PACKAGE_COMPRESSION_UNSUPPORTED")

    def test_nul_suffix_in_original_member_name_is_rejected(self):
        name = PREFIX + "LICENSE"
        alias = name + "_NUL_SUFFIX"

        def rename(entries):
            entries[alias] = entries.pop(name)

        self.rewrite(rename)
        raw = self.archive.read_bytes()
        self.assertEqual(raw.count(alias.encode("ascii")), 2)
        # local/central headerの同じ長さの原名だけを変更し、本文とoffsetは保持する。
        self.archive.write_bytes(raw.replace(alias.encode("ascii"),
                                            (name + "\x00NUL_SUFFIX").encode("ascii")))
        with zipfile.ZipFile(self.archive) as bundle:
            info = bundle.getinfo(name)
            self.assertEqual(info.filename, name)
            self.assertEqual(info.orig_filename, name + "\x00NUL_SUFFIX")
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stderr, "")
        self.assertEqual(json.loads(result.stdout)["code"], "PACKAGE_MEMBER_PATH_INVALID")

    def test_nested_runtime_cannot_hide_in_license_files_or_inventory(self):
        original = self.archive.read_bytes()
        for referenced in [False, True]:
            with self.subTest(referenced=referenced):
                def inject(entries):
                    path = "licenses/rust/fixture-1.0.0/backend.exe"
                    digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
                    entries[PREFIX + path] = b"abc"
                    manifest = json.loads(entries[PREFIX + "MANIFEST.json"])
                    manifest["files"][path] = digest
                    if referenced:
                        inv_path = PREFIX + "licenses/DEPENDENCIES.json"
                        inventory = json.loads(entries[inv_path])
                        inventory["packages"][0]["texts"]["backend.exe"] = digest
                        entries[inv_path] = json.dumps(inventory).encode()
                        manifest["files"]["licenses/DEPENDENCIES.json"] = hashlib.sha256(entries[inv_path]).hexdigest()
                    entries[PREFIX + "MANIFEST.json"] = json.dumps(manifest).encode()
                self.rewrite(inject)
                refused = self.verify()
                self.assertNotEqual(refused.returncode, 0)
                self.assertEqual(json.loads(refused.stdout)["code"], "PACKAGE_COMPONENT_SET_UNSUPPORTED")
                self.archive.write_bytes(original)

    def test_broken_deflate_reports_only_a_fixed_code(self):
        with zipfile.ZipFile(self.archive) as bundle:
            entries = {name: bundle.read(name) for name in bundle.namelist()}
        with zipfile.ZipFile(self.archive, "w", zipfile.ZIP_DEFLATED) as bundle:
            for name, data in entries.items():
                bundle.writestr(name, data)
        with zipfile.ZipFile(self.archive) as bundle:
            info = bundle.getinfo(PREFIX + "MANIFEST.json")
        data = bytearray(self.archive.read_bytes())
        name_size, extra_size = struct.unpack_from("<HH", data, info.header_offset + 26)
        # DEFLATEの予約block type(3)。圧縮データ以外のZIP構造は保持する。
        data[info.header_offset + 30 + name_size + extra_size] = 7
        self.archive.write_bytes(data)
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stderr, "")
        self.assertEqual(json.loads(result.stdout)["code"], "PACKAGE_INVALID")

    def test_cli_argument_errors_do_not_repeat_private_values(self):
        marker = "PRIVATE_FIXTURE_ARGUMENT"
        result = subprocess.run(
            [sys.executable, "-B", str(VERIFY), "--archive", str(self.archive), "--source", SOURCE,
             "--archive-sha256", "a" * 64, "--exe-sha256", "b" * 64, "--unexpected", marker],
            capture_output=True, text=True, timeout=15)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(marker, result.stderr)
        self.assertEqual(json.loads(result.stdout), {"check": "tauri-unwired-package", "status": "fail",
                                                    "code": "PACKAGE_ARGUMENT_INVALID"})
        self.assertEqual(result.stderr, "")
        self.assertNotIn(marker, result.stdout)

    def test_external_source_archive_and_exe_expectations_are_enforced(self):
        # 実装後の回帰。先行した6つの赤→緑とは区別する。
        for args, code in [({"source": "f" * 40}, "PACKAGE_SOURCE_MISMATCH"),
                           ({"archive-sha256": "f" * 64}, "PACKAGE_ARCHIVE_HASH_MISMATCH"),
                           ({"exe-sha256": "f" * 64}, "PACKAGE_EXE_HASH_MISMATCH")]:
            with self.subTest(code=code):
                result = self.verify(**args)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(json.loads(result.stdout)["code"], code)

    def test_open_gate_and_missing_gui_remain_rejected(self):
        original = self.archive.read_bytes()
        for mutation, code in [("gate", "PACKAGE_BOUNDARY_MISMATCH"),
                               ("gui", "PACKAGE_COMPONENT_SET_UNSUPPORTED")]:
            with self.subTest(mutation=mutation):
                def change(entries):
                    manifest = json.loads(entries[PREFIX + "MANIFEST.json"])
                    if mutation == "gate":
                        manifest["productionGateClosed"] = False
                    else:
                        del manifest["files"]["rescue-host-shell.exe"]
                        del entries[PREFIX + "rescue-host-shell.exe"]
                    entries[PREFIX + "MANIFEST.json"] = json.dumps(manifest).encode()
                self.rewrite(change)
                result = self.verify()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(json.loads(result.stdout)["code"], code)
                self.archive.write_bytes(original)

    def test_duplicate_case_alias_and_symlink_members_are_rejected(self):
        original = self.archive.read_bytes()
        for mutation in ["duplicate", "case-alias", "symlink"]:
            with self.subTest(mutation=mutation):
                if mutation == "duplicate":
                    with warnings.catch_warnings(), zipfile.ZipFile(self.archive, "a") as bundle:
                        warnings.simplefilter("ignore", UserWarning)
                        bundle.writestr(PREFIX + "rescue-host-shell.exe", b"abc")
                elif mutation == "case-alias":
                    def alias(entries):
                        manifest = json.loads(entries[PREFIX + "MANIFEST.json"])
                        name = "licenses/RUST/fixture-1.0.0/LICENSE"
                        entries[PREFIX + name] = b"abc"
                        manifest["files"][name] = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
                        entries[PREFIX + "MANIFEST.json"] = json.dumps(manifest).encode()
                    self.rewrite(alias)
                else:
                    with zipfile.ZipFile(self.archive) as bundle:
                        entries = [(info, bundle.read(info)) for info in bundle.infolist()]
                    with zipfile.ZipFile(self.archive, "w") as bundle:
                        for info, data in entries:
                            if info.filename == PREFIX + "rescue-host-shell.exe":
                                info.create_system = 3
                                info.external_attr = 0o120777 << 16
                            bundle.writestr(info, data)
                refused = self.verify()
                self.assertNotEqual(refused.returncode, 0)
                self.assertEqual(json.loads(refused.stdout)["code"], "PACKAGE_MEMBER_AMBIGUOUS")
                self.archive.write_bytes(original)

    def test_windows_ambiguous_members_are_rejected_before_extraction(self):
        original = self.archive.read_bytes()
        for path in ["licenses/../backend.exe", "licenses/runtime.txt:payload", "licenses/NUL.txt"]:
            with self.subTest(path=path):
                def inject(entries):
                    manifest = json.loads(entries[PREFIX + "MANIFEST.json"])
                    entries[PREFIX + path] = b"abc"
                    manifest["files"][path] = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
                    entries[PREFIX + "MANIFEST.json"] = json.dumps(manifest).encode()
                self.rewrite(inject)
                refused = self.verify()
                self.assertNotEqual(refused.returncode, 0)
                self.assertEqual(json.loads(refused.stdout)["code"], "PACKAGE_MEMBER_PATH_INVALID")
                self.archive.write_bytes(original)

    def test_unknown_manifest_diagnostics_are_rejected_without_copying_private_values(self):
        marker = "PRIVATE_FIXTURE_TOKEN_AND_INTERNAL_PATH"
        def inject(entries):
            manifest = json.loads(entries[PREFIX + "MANIFEST.json"])
            manifest["diagnostics"] = {"token": marker, "profilePath": str(self.folder)}
            entries[PREFIX + "MANIFEST.json"] = json.dumps(manifest).encode()
        self.rewrite(inject)
        refused = self.verify()
        self.assertNotEqual(refused.returncode, 0)
        report = json.loads(refused.stdout)
        self.assertEqual(report, {"check": "tauri-unwired-package", "status": "fail",
                                  "code": "PACKAGE_MANIFEST_UNSUPPORTED"})
        self.assertNotIn(marker, refused.stdout + refused.stderr)
        self.assertNotIn(str(self.folder), refused.stdout + refused.stderr)

    def test_dependency_and_runtime_license_bodies_cannot_be_omitted(self):
        for name in ["licenses/rust-toolchain/COPYRIGHT-library.html",
                     "licenses/rust/fixture-1.0.0/LICENSE"]:
            with self.subTest(name=name):
                original = self.archive.read_bytes()
                def remove(entries):
                    del entries[PREFIX + name]
                    manifest = json.loads(entries[PREFIX + "MANIFEST.json"])
                    del manifest["files"][name]
                    entries[PREFIX + "MANIFEST.json"] = json.dumps(manifest).encode()
                self.rewrite(remove)
                refused = self.verify()
                self.assertNotEqual(refused.returncode, 0)
                self.assertEqual(json.loads(refused.stdout)["code"], "PACKAGE_LICENSE_INCOMPLETE")
                self.archive.write_bytes(original)

    def test_manifest_cannot_approve_an_unfinished_backend(self):
        def inject(entries):
            entries[PREFIX + "backend.exe"] = b"abc"
            manifest = json.loads(entries[PREFIX + "MANIFEST.json"])
            manifest["files"]["backend.exe"] = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
            entries[PREFIX + "MANIFEST.json"] = json.dumps(manifest).encode()
        self.rewrite(inject)
        refused = self.verify()
        self.assertNotEqual(refused.returncode, 0)
        self.assertEqual(json.loads(refused.stdout)["code"], "PACKAGE_COMPONENT_SET_UNSUPPORTED")

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
