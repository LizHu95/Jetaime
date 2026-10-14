"""Verify shared startup configuration without model calls or Phoenix uploads."""
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from runner import model_arguments, preflight


class ModelConfigurationTests(unittest.TestCase):
    def args(self):
        return SimpleNamespace(model="ignored-local-default", ollama_url="http://local:11434",
                               timeout=30, context_tokens=8192, provider="openai-compatible",
                               model_url="https://cloud.example/v1", api_key_env="TEST_KEY",
                               output_mode="json_object", model_config=None, model_profile=None,
                               judge_model=None, phoenix_url="http://phoenix:6006")

    def test_shared_cli_profile(self):
        args = self.args()
        args.model_config = Path("/tmp/models.json")
        args.model_profile = "cloud"
        flags = model_arguments(args)
        self.assertEqual(flags[flags.index("-model-config") + 1], "/tmp/models.json")
        self.assertEqual(flags[flags.index("-model-profile") + 1], "cloud")
        self.assertIn("-api-key-env", flags)

    @patch("runner.httpx.Client")
    @patch("runner.subprocess.run")
    def test_cloud_preflight_does_not_require_ollama(self, run, client):
        config = {"provider":"openai-compatible", "base_url":"https://cloud.example/v1",
                  "model":"cloud-test", "context_tokens":16384, "api_key_env":"TEST_KEY"}
        run.return_value = SimpleNamespace(returncode=0, stdout=json.dumps(config), stderr="")
        response = MagicMock(text="test-version")
        http = client.return_value.__enter__.return_value
        http.get.return_value = response
        args = self.args()
        result = preflight(args)
        http.get.assert_called_once_with("http://phoenix:6006/arize_phoenix_version")
        self.assertIn("-print-model-config", run.call_args.args[0])
        self.assertEqual(args.model, "cloud-test")
        self.assertEqual(args.context_tokens, 16384)
        self.assertFalse(result["cloudInferenceVerified"])

    @patch("runner.httpx.Client")
    @patch("runner.subprocess.run")
    def test_missing_credentials_fail_before_upload(self, run, client):
        run.return_value = SimpleNamespace(returncode=1, stdout="", stderr="missing key environment variable TEST_KEY")
        with self.assertRaises(ValueError):
            preflight(self.args())
        client.assert_not_called()
