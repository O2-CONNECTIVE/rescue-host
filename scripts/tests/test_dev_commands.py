"""Exercise development-tool failures through the command-line interface."""

from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class DevelopmentCommandTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='rescue-dev-check-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        for directory in ('src', 'tests', 'scripts'):
            (self.root / directory).mkdir()
        shutil.copy2(Path(__file__).resolve().parents[1] / 'dev.py', self.root / 'scripts/dev.py')
        (self.root / 'src/example.py').write_text('value = 1\n', encoding='utf-8')
        self.test_file = self.root / 'tests/test_example.py'
        self.test_file.write_text('import unittest\n', encoding='utf-8')

    def assert_rejected(self, command, message):
        result = subprocess.run(
            [sys.executable, str(self.root / 'scripts/dev.py'), command],
            cwd=self.root.parent,
            capture_output=True,
            text=True,
            timeout=30,
        )
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn(message, result.stdout + result.stderr)

    def test_missing_source_is_rejected(self):
        shutil.rmtree(self.root / 'src')
        self.assert_rejected('check', 'Missing or empty Python target: src/')

    def test_empty_source_is_rejected(self):
        (self.root / 'src/example.py').unlink()
        self.assert_rejected('check', 'Missing or empty Python target: src/')

    def test_missing_tests_are_rejected(self):
        shutil.rmtree(self.root / 'tests')
        self.assert_rejected('check', 'Missing or empty Python target: tests/')

    def test_zero_tests_are_rejected(self):
        self.assert_rejected('check', 'No tests discovered in tests/')

    def test_all_skipped_tests_are_rejected(self):
        self.test_file.write_text(
            "import unittest\n"
            "@unittest.skip('development-tool probe')\n"
            "class Example(unittest.TestCase):\n"
            "    def test_example(self): pass\n",
            encoding='utf-8',
        )
        self.assert_rejected('check', 'total=1, executed=0, passed=0, skipped=1')

    def test_failed_test_is_rejected(self):
        self.test_file.write_text(
            "import unittest\n"
            "class Example(unittest.TestCase):\n"
            "    def test_example(self): self.fail('development-tool probe')\n",
            encoding='utf-8',
        )
        self.assert_rejected('check', 'failures=1, errors=0')

    def test_invalid_syntax_is_rejected(self):
        (self.root / 'src/example.py').write_text('value =\n', encoding='utf-8')
        self.assert_rejected('syntax', 'invalid syntax')


if __name__ == '__main__':
    unittest.main()
