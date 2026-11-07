import hashlib
import importlib.util
import pathlib
import io
import tarfile
import tempfile
import unittest
_spec = importlib.util.spec_from_file_location('driver_material', pathlib.Path(__file__).resolve().parents[1] / 'collect_driver_license_material.py')
_module = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_module)
validate_driver_graph = _module.validate_driver_graph
parse_fixed_driver_lock = _module.parse_fixed_driver_lock
verify_cached_archives = _module.verify_cached_archives
collect_driver_material = _module.collect_driver_material
verify_registry_license_files = _module.verify_registry_license_files
verify_root_manifest = _module.verify_root_manifest


class DriverLicenseTests(unittest.TestCase):
    def test_driver_lock_bytes_must_match_fixed_upstream(self):
        with self.assertRaises(ValueError):
            parse_fixed_driver_lock(b'version = 3\n')

    def test_unverified_driver_crate_is_rejected_before_collector_runs(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            for name in ('crate', 'mit', 'collector'):
                (root / name).write_bytes(b'untrusted')
            with self.assertRaisesRegex(ValueError, 'fixed input mismatch'):
                collect_driver_material(root, root / 'crate', root / 'mit', root / 'collector', root, root / 'output')
            self.assertFalse((root / 'output').exists())

    def test_source_manifest_must_match_fixed_crate_member(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            crate = root / 'input.crate'
            source = root / 'source'
            source.mkdir()
            (source / 'Cargo.toml').write_bytes(b'original')
            with tarfile.open(crate, 'w:gz') as tar:
                info = tarfile.TarInfo('tauri-driver-2.1.0/Cargo.toml')
                info.size = len(b'original')
                tar.addfile(info, io.BytesIO(b'original'))
            verify_root_manifest(crate, source)
            (source / 'Cargo.toml').write_bytes(b'changed')
            with self.assertRaises(ValueError):
                verify_root_manifest(crate, source)
    def setUp(self):
        self.metadata = {'resolve': {'root': 'root', 'nodes': [
            {'id': 'root', 'dependencies': ['dep']}, {'id': 'dep', 'dependencies': []}]},
            'packages': [
                {'id': 'root', 'name': 'tauri-driver', 'version': '2.1.0', 'source': None, 'license': 'Apache-2.0 OR MIT'},
                {'id': 'dep', 'name': 'example', 'version': '1.0.0', 'source': 'registry+https://github.com/rust-lang/crates.io-index', 'license': 'MIT'}]}
        self.lock = {'package': [{'name': 'example', 'version': '1.0.0', 'source': 'registry+https://github.com/rust-lang/crates.io-index', 'checksum': 'a' * 64}]}

    def test_only_exact_root_and_reachable_registry_lock_are_accepted(self):
        result = validate_driver_graph(self.metadata, self.lock)
        self.assertEqual([(x['name'], x['version']) for x in result], [('example', '1.0.0')])
        self.lock['package'][0]['checksum'] = 'bad'
        with self.assertRaises(ValueError):
            validate_driver_graph(self.metadata, self.lock)

    def test_unknown_local_dependency_is_rejected(self):
        self.metadata['packages'][1]['source'] = None
        with self.assertRaises(ValueError):
            validate_driver_graph(self.metadata, self.lock)

    def test_registry_archive_bytes_must_match_driver_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            cache = root / 'registry' / 'cache' / 'index'
            cache.mkdir(parents=True)
            archive = cache / 'example-1.0.0.crate'
            archive.write_bytes(b'fixed')
            self.lock['package'][0]['checksum'] = hashlib.sha256(b'fixed').hexdigest()
            verify_cached_archives(self.metadata, self.lock, root)
            archive.write_bytes(b'tampered')
            with self.assertRaises(ValueError):
                verify_cached_archives(self.metadata, self.lock, root)

    def test_collected_license_must_match_archive_member(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source = root / 'example-1.0.0'
            source.mkdir()
            (source / 'LICENSE').write_bytes(b'original license')
            crate = root / 'example-1.0.0.crate'
            with tarfile.open(crate, 'w:gz') as tar:
                member = tarfile.TarInfo('example-1.0.0/LICENSE')
                member.size = len(b'original license')
                tar.addfile(member, io.BytesIO(b'original license'))
            verify_registry_license_files(crate, source, 'example-1.0.0')
            (source / 'LICENSE').write_bytes(b'changed license')
            with self.assertRaises(ValueError):
                verify_registry_license_files(crate, source, 'example-1.0.0')

    def test_missing_notice_from_extracted_source_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            source = root / 'example-1.0.0'
            source.mkdir()
            (source / 'LICENSE').write_bytes(b'license')
            crate = root / 'example-1.0.0.crate'
            with tarfile.open(crate, 'w:gz') as tar:
                for name, value in [('LICENSE', b'license'), ('NOTICE', b'notice')]:
                    member = tarfile.TarInfo('example-1.0.0/' + name)
                    member.size = len(value)
                    tar.addfile(member, io.BytesIO(value))
            with self.assertRaises(ValueError):
                verify_registry_license_files(crate, source, 'example-1.0.0')


if __name__ == '__main__':
    unittest.main()
