"""Historical paths remain usable after moving generated artifacts."""
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts"))
from artifact_paths import relocated


class ArtifactPathTests(unittest.TestCase):
    def test_both_historical_output_locations_resolve(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "outputs/evals/snapshot-run/cases.json"
            target.parent.mkdir(parents=True)
            target.write_text("[]")
            for prefix in ["evals/output", "local/data/evals/output"]:
                for old in [root / prefix / "snapshot-run/cases.json", Path(prefix) / "snapshot-run/cases.json"]:
                    self.assertEqual(relocated(old, root), target)
                    self.assertEqual(relocated(old, root).read_text(), "[]")

    def test_reports_and_existing_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "outputs/evals/reports/report.json"
            target.parent.mkdir(parents=True)
            target.write_text("{}")
            self.assertEqual(relocated(root / "local/data/chat-eval/report.json", root), target)
            self.assertEqual(relocated(target, root), target)
