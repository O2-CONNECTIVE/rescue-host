"""Check token-validation wiring through the development CLI, not native UI."""

import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class TokenCommandTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='rescue-token-check-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        scripts = Path(__file__).resolve().parents[1]
        (self.root / 'scripts').mkdir()
        shutil.copy2(scripts / 'dev.py', self.root / 'scripts/dev.py')
        self.design = self.root / 'ui/tauri/design'
        self.design.mkdir(parents=True)
        for name in ('check_tokens.py', 'tokens.css', 'components.css', 'preview.css'):
            shutil.copy2(scripts.parent / 'ui/tauri/design' / name, self.design / name)

    def run_tokens(self, **environment):
        return subprocess.run(
            [sys.executable, str(self.root / 'scripts/dev.py'), 'tokens'],
            cwd=self.root.parent,
            env=dict(os.environ, **environment),
            capture_output=True,
            text=True,
            timeout=30,
        )

    def test_published_tokens_pass_from_another_directory(self):
        result = self.run_tokens()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('PASS:', result.stdout)
        self.assertIn('contrast pairs. Light theme only.', result.stdout)

    def test_unknown_reference_is_rejected_even_with_optimization_environment(self):
        with (self.design / 'components.css').open('a', encoding='utf-8') as css:
            css.write('\n.probe { color: var(--sys-probe-missing); }\n')
        result = self.run_tokens(PYTHONOPTIMIZE='2')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Unknown token: --sys-probe-missing', result.stderr)

    def test_white_text_on_white_surface_is_rejected(self):
        path = self.design / 'tokens.css'
        source = path.read_text(encoding='utf-8')
        original = '--sys-text-primary: var(--ref-slate-900);'
        self.assertIn(original, source)
        path.write_text(
            source.replace(original, '--sys-text-primary: var(--ref-white);', 1),
            encoding='utf-8',
        )
        result = self.run_tokens()
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('Contrast below target:', result.stderr)
        self.assertIn("('text-primary', 'surface-default', 1.0)", result.stderr)


if __name__ == '__main__':
    unittest.main()
