"""Local evaluations retain results and snapshots without a tracing backend."""
import io
import json
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

from test_evaluators import INPUT, OUTPUT
from runner import load_cases, run_experiment


class LocalRunnerTests(unittest.TestCase):
    def run_case(self, directory, outputs):
        path = Path(directory) / "cases.json"
        path.write_text(json.dumps([{"id":"local-only", "input":INPUT, "expected":{"reviewed":True,"answerStatus":"answered"}}]))
        cases, digest = load_cases(path)
        args = SimpleNamespace(cases=path, output=Path(directory)/"results", dataset="local-tests",
            name="local-test", suite_version="test", model="fake", resolved_model_config={},
            context_tokens=8192, timeout=10, repetitions=2, judge_model=None, ollama_url="http://unused")
        with patch("runner.preflight", return_value={}), patch("runner.build_binary"), \
                patch("runner.run_workflow", side_effect=outputs), redirect_stdout(io.StringIO()):
            result = run_experiment(args, cases, digest)
        summary = json.loads(next(args.output.glob("summary-*.json")).read_text())
        records = [json.loads(line) for line in Path(summary["results"]).read_text().splitlines()]
        snapshot = Path(summary["snapshot"])
        self.assertEqual((snapshot/"cases.json").read_bytes(), path.read_bytes())
        self.assertEqual(json.loads((snapshot/"manifest.json").read_text())["experimentName"], "local-test")
        self.assertEqual(len(records), 2)
        self.assertNotEqual(records[0]["experimentRunId"], records[1]["experimentRunId"])
        return result, summary, records

    def test_repetitions_and_pending_judge(self):
        with tempfile.TemporaryDirectory() as directory:
            result, summary, records = self.run_case(directory, [OUTPUT, OUTPUT])
            self.assertEqual(result, 0)
            self.assertEqual(summary["metrics"]["execution_success"]["passed"], 2)
            self.assertEqual(summary["metrics"]["semantic_quality"]["pending"], 2)
            self.assertNotIn("url", summary)
            self.assertEqual(records[0]["output"], OUTPUT)

    def test_task_error_does_not_discard_other_results(self):
        with tempfile.TemporaryDirectory() as directory:
            result, summary, records = self.run_case(directory, [RuntimeError("test timeout"), OUTPUT])
            self.assertEqual(result, 1)
            self.assertEqual(summary["errors"], 1)
            self.assertEqual(records[0]["taskError"], "test timeout")
            self.assertEqual(records[1]["evaluations"]["execution_success"]["score"], 1)
