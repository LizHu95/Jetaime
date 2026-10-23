"""Unified startup loads literals without shell evaluation or secret logging."""
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from dev import command, load_env


class StartupTests(unittest.TestCase):
    def test_environment_precedence_and_literal_values(self):
        with tempfile.TemporaryDirectory() as directory:
            shared = Path(directory) / ".env"
            service = Path(directory) / "feishu.env"
            shared.write_text("KEY=shared\nexport EXTRA='literal $HOME $(whoami)'\nEMPTY=\n")
            service.write_text("KEY=service\nFEISHU_APP_ID=test\n")
            env = {"KEY":"inherited"}
            inherited = set(env)
            load_env(shared, env, inherited)
            load_env(service, env, inherited)
            self.assertEqual(env["KEY"], "inherited")
            self.assertEqual(env["EXTRA"], "literal $HOME $(whoami)")
            self.assertEqual(env["EMPTY"], "")
            self.assertEqual(env["FEISHU_APP_ID"], "test")
            env = {}
            load_env(shared, env, set())
            load_env(service, env, set())
            self.assertEqual(env["KEY"], "service")

    def test_invalid_file_does_not_echo_secret(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".env"
            path.write_text("KEY='secret-value\n")
            with self.assertRaises(ValueError) as caught:
                load_env(path, {}, set())
            self.assertNotIn("secret-value", str(caught.exception))

    def test_command_forwarding(self):
        cwd, argv = command("api", ["-addr", "127.0.0.1:8082"], {})
        self.assertEqual(cwd.name, "api")
        self.assertEqual(argv, ["go", "run", "./cmd/api", "-addr", "127.0.0.1:8082"])
        _, argv = command("model-check", ["-model-profile", "local-qwen"], {})
        self.assertEqual(argv, ["go", "run", "./cmd/model-check", "-model-profile", "local-qwen"])

    def test_retired_commands_are_rejected(self):
        for action in ["assistant", "assistant-check", "eval", "eval-check", "eval-services"]:
            with self.subTest(action=action), self.assertRaises(ValueError):
                command(action, [], {})
