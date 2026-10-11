"""Run from the repository root with make eval-test."""

import json
import csv
import io
from pathlib import Path
import unittest
from unittest.mock import patch

import httpx

from ollama_evaluator import OllamaJudge, decode_csv_row, input_rows


class JudgeTests(unittest.TestCase):
    def test_phoenix_csv_actual_output_and_multiline_fields(self):
        stream = io.StringIO()
        writer = csv.DictWriter(stream, fieldnames=["example_id", "input", "output", "reference_output", "metadata"])
        writer.writeheader()
        writer.writerow({
            "example_id": "example-1",
            "input": json.dumps({"input.request.query": "晚餐\n两个人", "input.request.conditions.noteTypes": '["restaurant"]',
                                 "input.request.conditions.budgetMaxCents": "8000"}),
            "output": json.dumps({"decision": {"result": {"outcome": "insufficient_information"}}}),
            "reference_output": json.dumps({"output.decision.result.outcome": "recommended"}),
            "metadata": json.dumps({"facts_overrides": {"note-1": {"verified": True, "totalCostCents": 9000}}})
        })
        stream.seek(0)
        rows = list(input_rows(Path("baseline.csv"), stream))
        self.assertEqual(len(rows), 1)
        example = decode_csv_row(rows[0][1])
        self.assertEqual(example["output"]["decision"]["result"]["outcome"], "insufficient_information")
        self.assertEqual(example["input"]["request"]["conditions"]["budgetMaxCents"], 8000)
        self.assertEqual(example["metadata"]["evaluation_context"]["verifiedFacts"]["note-1"]["totalCostCents"], 9000)
        self.assertNotIn("reference_reviewed", example["metadata"])

    def test_dataset_csv_is_rejected(self):
        with self.assertRaisesRegex(ValueError, "实验导出"):
            list(input_rows(Path("dataset.csv"), io.StringIO("input.request.query,output.decision.result.outcome\n晚餐,recommended\n")))

    def call_judge(self, judgment, **response_overrides):
        captured = {}

        def respond(request):
            captured.update(json.loads(request.content))
            return httpx.Response(200, json={
                "done": True, "done_reason": "stop",
                "message": {"content": json.dumps(judgment)},
                **response_overrides,
            })

        real_client = httpx.Client
        with patch("ollama_evaluator.httpx.Client", side_effect=lambda **kw: real_client(
                transport=httpx.MockTransport(respond), **kw)):
            result = OllamaJudge().evaluate(
                {"query": "晚餐"}, {"decision": {}}, {"private": "unreviewed answer"},
                {"reference_reviewed": "true", "evaluation_context": {"facts": "证据"}})
        return result, captured

    def test_score_mapping_and_unreviewed_reference(self):
        for label, score in [("pass", 1.0), ("fail", 0.0), ("insufficient_evidence", None)]:
            with self.subTest(label=label):
                result, payload = self.call_judge({"label": label, "explanation": "依据"})
                self.assertEqual(result["score"], score)
                evidence = json.loads(payload["messages"][1]["content"])
                self.assertIsNone(evidence["expected"])
                self.assertEqual(evidence["evaluation_context"], {"facts": "证据"})
                self.assertFalse(payload["stream"])
                self.assertFalse(payload["think"])

    def test_rejects_invalid_judgments(self):
        for value in [[], {"label": "unknown", "explanation": "依据"},
                      {"label": "pass", "explanation": " "},
                      {"label": "pass", "explanation": "依据", "score": 42}]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                self.call_judge(value)

    def test_rejects_truncated_or_incomplete_response(self):
        for overrides in [{"done": False}, {"done_reason": "length"},
                          {"message": {"content": "not JSON"}}]:
            with self.subTest(overrides=overrides), self.assertRaises(ValueError):
                self.call_judge({"label": "pass", "explanation": "依据"}, **overrides)

    def test_http_failure_is_not_a_quality_score(self):
        real_client = httpx.Client
        with patch("ollama_evaluator.httpx.Client", side_effect=lambda **kw: real_client(
                transport=httpx.MockTransport(lambda req: httpx.Response(500)), **kw)):
            with self.assertRaises(httpx.HTTPStatusError):
                OllamaJudge().evaluate({}, {})


if __name__ == "__main__":
    unittest.main()
