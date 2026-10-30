"""公開前 CLI の正例と拒否例。ダミーbinaryは実配布物ではない。"""
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts/tests'))
from evaluation_package_fixture import write_license_fixture

SOURCE = '1eda7a4e964d34a751dcc89509ee07b3482917a7'
MIT = ROOT / 'docs/third-party/tauri-driver-LICENSE-MIT'
def sha(data):
    return hashlib.sha256(data).hexdigest()

class PayloadTests(unittest.TestCase):
    def test_fixed_mit_source_bytes_are_protected_from_checkout_conversion(self):
        result = subprocess.run(['git', 'check-attr', 'text', '--', 'docs/third-party/tauri-driver-LICENSE-MIT'], cwd=ROOT, capture_output=True, text=True, check=True)
        self.assertTrue(result.stdout.strip().endswith(': unset'), result.stdout)
        self.assertEqual(sha(MIT.read_bytes()), '9dd42ea92cff2ede5cd477cbfcce051b2d0115c0ac7f368ee88cb545055dff1d')

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        self.payload = root / 'payload'
        self.payload.mkdir()
        licenses = root / 'app-licenses'
        write_license_fixture(licenses)
        exe = root / 'fake.exe'
        exe.write_bytes(b'fixture executable')
        result = subprocess.run([sys.executable, str(ROOT / 'scripts/package_tauri_evaluation.py'), '--exe', str(exe), '--licenses', str(licenses), '--source-sha', SOURCE, '--output', str(root / 'package')], capture_output=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        (self.payload / 'RescueHostTauriUnwiredEvaluation-x64.zip').write_bytes((root / 'package/RescueHostTauriUnwiredEvaluation-x64.zip').read_bytes())
        (self.payload / 'tauri-driver.exe').write_bytes(b'fixture driver')
        self.dep = {'scope': 'Windows x64 tauri-driver 2.1.0 / Cargo resolve graph; linked SBOM ではない。', 'tools': {'rust': '1.99.1', 'node': '24.22.1', 'python': '3.13.17', 'profile': 'release'}, 'packages': [{'name': 'fixture', 'version': '1.0', 'source': 'registry+https://github.com/rust-lang/crates.io-index', 'sourceArchive': 'https://static.crates.io/crates/fixture/fixture-1.0.crate', 'crateChecksum': 'a'*64, 'license': 'MIT', 'supplement': None, 'texts': {'LICENSE': sha(b'fixture license')}}], 'rustToolchainNotices': {'COPYRIGHT.html': sha(b'copyright'), 'COPYRIGHT-library.html': sha(b'library')}, 'driverRoot': {'version': '2.1.0', 'selected': 'MIT', 'license': 'Apache-2.0 OR MIT', 'vcs': '447fa9f3f993fe77724189e355078b38ce20baea', 'licenseSha256': sha(MIT.read_bytes()), 'crateSha256': '2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953'}, 'driverLockSha256':'94bf7caee3f76ca1d558e2636a846f12738db64264abe6dd7fa930548d3f03e1'}
        self.license_entries = {'DEPENDENCIES.json': json.dumps(self.dep).encode(), 'rust/fixture-1.0/LICENSE': b'fixture license', 'rust/tauri-driver-2.1.0/LICENSE-MIT': MIT.read_bytes(), 'rust-toolchain/COPYRIGHT.html': b'copyright', 'rust-toolchain/COPYRIGHT-library.html': b'library'}
        self.write_licenses()
        self.exe_hash = sha(exe.read_bytes())
        postimage = json.loads((ROOT/'tools/native-driver-trace/postimage.json').read_bytes())
        self.provenance = {'binarySha256': sha(b'fixture driver'), 'variant': 'tauri-driver-2.1.0-stage-1', 'upstreamSha256': '2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953', 'patchSha256': 'df8dd46174fc2b8aa4df4aed0c382713d7a3cadb5accda5f62aef74a960b5b9e', 'diagnosticCodeSha256': {'diagnostic.rs': postimage['src/diagnostic.rs'], 'diagnostic_tests.rs': postimage['src/diagnostic_tests.rs']}, 'preimage': {'src/main.rs': 'a'*64, 'src/server.rs': 'b'*64, 'src/diagnostic.rs': None, 'src/diagnostic_tests.rs': None}, 'postimage': postimage, 'postimageManifestSha256': sha((ROOT/'tools/native-driver-trace/postimage.json').read_bytes()), 'upstreamFileCount': 20, 'cargoLockSha256': '94bf7caee3f76ca1d558e2636a846f12738db64264abe6dd7fa930548d3f03e1'}
        (self.payload / 'driver-provenance.json').write_text(json.dumps(self.provenance))
        self.write_manifest()

    def write_licenses(self):
        with zipfile.ZipFile(self.payload / 'driver-licenses.zip', 'w', zipfile.ZIP_DEFLATED) as archive:
            for name, data in self.license_entries.items(): archive.writestr(name, data)

    def write_manifest(self):
        files = self.payload
        m = {'schema': 1, 'source': SOURCE, 'zipSha': sha((files/'RescueHostTauriUnwiredEvaluation-x64.zip').read_bytes()), 'exeSha': self.exe_hash, 'driverSha': sha((files/'tauri-driver.exe').read_bytes()), 'provenanceSha': sha((files/'driver-provenance.json').read_bytes()), 'licensesSha': sha((files/'driver-licenses.zip').read_bytes())}
        (files/'manifest.json').write_text(json.dumps(m))

    def run_cli(self):
        return subprocess.run([sys.executable, '-I', str(ROOT/'scripts/verify_cross_payload.py'), '--payload', str(self.payload), '--app-scripts', str(ROOT/'scripts')], capture_output=True, text=True, timeout=20)

    def test_valid_payload_is_accepted(self):
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(json.loads(result.stdout)['status'], 'pass')

    def test_extra_file_or_path_traversal_is_rejected(self):
        self.license_entries['../unexpected'] = b'no'
        self.write_licenses(); self.write_manifest()
        self.assertNotEqual(self.run_cli().returncode, 0)

    def test_license_body_mismatch_is_rejected_even_if_zip_hash_is_updated(self):
        self.license_entries['rust/fixture-1.0/LICENSE'] = b'changed'
        self.write_licenses(); self.write_manifest()
        self.assertNotEqual(self.run_cli().returncode, 0)

    def test_secret_candidate_inside_license_is_rejected(self):
        secret = b'-----BEGIN OPENSSH PRIVATE KEY-----'
        self.license_entries['rust/fixture-1.0/LICENSE'] = secret
        self.dep['packages'][0]['texts']['LICENSE'] = sha(secret)
        self.license_entries['DEPENDENCIES.json'] = json.dumps(self.dep).encode()
        self.write_licenses(); self.write_manifest()
        result = self.run_cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(secret.decode(), result.stdout + result.stderr)

    def test_directory_entries_for_real_parents_are_accepted(self):
        self.license_entries['rust/'] = b''
        self.write_licenses(); self.write_manifest()
        self.assertEqual(self.run_cli().returncode, 0)

    def test_directory_entry_cannot_contain_bytes(self):
        self.license_entries['rust/'] = b'not a directory body'
        self.write_licenses(); self.write_manifest()
        self.assertNotEqual(self.run_cli().returncode, 0)

    def test_provenance_unknown_field_with_private_path_is_rejected(self):
        self.provenance['localPath'] = '/Users/private/file'
        (self.payload/'driver-provenance.json').write_text(json.dumps(self.provenance))
        self.write_manifest()
        self.assertNotEqual(self.run_cli().returncode, 0)

    def test_json_escaped_private_path_is_rejected_after_decoding(self):
        self.dep['packages'][0]['license'] = '/Users/private/data'
        raw = json.dumps(self.dep).replace('/Users/', r'\u002fUsers\u002f').encode()
        self.license_entries['DEPENDENCIES.json'] = raw
        self.write_licenses(); self.write_manifest()
        self.assertNotEqual(self.run_cli().returncode, 0)

    def test_inventory_cannot_authorize_an_executable_as_license(self):
        self.dep['packages'][0]['texts']['backend.exe'] = sha(b'MZcontent')
        self.license_entries['rust/fixture-1.0/backend.exe'] = b'MZcontent'
        self.license_entries['DEPENDENCIES.json'] = json.dumps(self.dep).encode()
        self.write_licenses(); self.write_manifest()
        self.assertNotEqual(self.run_cli().returncode, 0)

    def test_secret_candidate_is_rejected_without_printing_it(self):
        secret = 'ghp_' + 'a'*36
        (self.payload/'tauri-driver.exe').write_text(secret)
        self.provenance['binarySha256'] = sha(secret.encode())
        (self.payload/'driver-provenance.json').write_text(json.dumps(self.provenance))
        self.write_manifest()
        result = self.run_cli()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(secret, result.stdout + result.stderr)

    def test_extra_payload_file_and_hash_mismatch_are_rejected(self):
        (self.payload/'unexpected.txt').write_text('extra')
        self.assertNotEqual(self.run_cli().returncode, 0)
        (self.payload/'unexpected.txt').unlink()
        (self.payload/'tauri-driver.exe').write_bytes(b'modified')
        self.assertNotEqual(self.run_cli().returncode, 0)

    def test_windows_reparse_attributes_are_rejected_by_the_path_check(self):
        spec = importlib.util.spec_from_file_location('payload_preflight', ROOT/'scripts/verify_cross_payload.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        class FakePath:
            def lstat(self):
                return type('Info', (), {'st_mode': 0o100600, 'st_file_attributes': 0x400})()
        self.assertFalse(module.ordinary(FakePath(), directory=False))

if __name__ == '__main__': unittest.main()
