import pathlib
import unittest


class CrossRunnerWorkflowTests(unittest.TestCase):
    def test_two_runners_are_sequential_and_artifact_has_one_day_retention(self):
        root = pathlib.Path('.github/workflows')
        workflow = (root / 'main-timeout-cross-runner.yml').read_text(encoding='utf-8')
        worker = (root / 'main-timeout-cross-worker.yml').read_text(encoding='utf-8')
        self.assertIn('retention-days: 1', workflow)
        self.assertIn('needs: [prepare, runner-one]', workflow)
        self.assertIn("needs.runner-one.outputs.ok == 'true'", workflow)
        self.assertIn('9000827ccba6bdab643e8b6fd33ac0654aef8333', worker)
        self.assertIn('artifact-ids:', worker)
        self.assertIn('foreach ($trial in 1..2)', worker)
        self.assertIn("$row.cleanup -ne 'confirmed'", worker)
        self.assertNotIn('Remove-Item -LiteralPath $expected -Recurse', worker)

    def test_missing_evidence_never_starts_the_next_runner_and_all_rows_are_reported(self):
        root = pathlib.Path('.github/workflows')
        workflow = (root / 'main-timeout-cross-runner.yml').read_text(encoding='utf-8')
        worker = (root / 'main-timeout-cross-worker.yml').read_text(encoding='utf-8')
        self.assertIn('test_cross_runner_workflow.py', workflow)
        self.assertIn('name: Summarize all four planned trials', workflow)
        self.assertIn("fallback = 'not-started' if result == 'skipped' else 'unknown'", workflow)
        self.assertIn("EVIDENCE_CHECK: ${{ steps.verify_evidence.outcome }}", worker)
        self.assertIn("if: ${{ always() && steps.verify_evidence.outcome == 'success' }}", worker)
        self.assertIn("$names += 'webview-result.json'", worker)
        self.assertIn("($summary.trials.trial -join ',') -cne '1,2'", worker)
        self.assertIn("$defaultOutcome = if ($env:TRIAL_RESULT -eq 'skipped') { 'not-started' } else { 'unknown' }", worker)


if __name__ == '__main__':
    unittest.main()
