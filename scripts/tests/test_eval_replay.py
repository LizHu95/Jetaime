"""Replay tooling invariants; does not import Phoenix or call a model."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import eval_replay
import phoenix_replay


class ReplayToolingTest(unittest.TestCase):
    def test_prepare_freezes_history_before_question_and_leaves_labels_unreviewed(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            snapshot = root / 'messages.jsonl'
            messages = [
                {'id': 'past', 'groupId': 'g', 'seq': 1, 'senderType': 'user', 'senderId': 'u',
                 'content': '原始约定', 'createdAt': '2026-10-29T01:00:00Z'},
                {'id': 'question', 'groupId': 'g', 'seq': 2, 'senderType': 'user', 'senderId': 'u',
                 'content': '约好几点？', 'createdAt': '2026-10-29T02:00:00Z', 'mentionsAI': True},
                {'id': 'answer', 'groupId': 'g', 'seq': 3, 'senderType': 'assistant', 'senderId': 'ai',
                 'content': '历史AI回复不能视为已审核真值', 'createdAt': '2026-10-29T03:00:00Z',
                 'replyToId': 'question'},
            ]
            raw = ''.join(json.dumps(m, ensure_ascii=False) + '\n' for m in messages).encode()
            snapshot.write_bytes(raw)
            out = root / 'cases.json'
            eval_replay.prepare(snapshot, out, 20)
            result = json.loads(out.read_text())
            case = result['cases'][0]
            self.assertEqual(result['snapshotSha256'], hashlib.sha256(raw).hexdigest())
            self.assertEqual(case['cutoffSeq'], 1)
            self.assertFalse(case['reviewed'])
            self.assertEqual(case['retrievedMessageIds'], [])
            self.assertIn('不能视为', case['expected']['reference'])
            self.assertEqual(out.stat().st_mode & 0o777, 0o600)
            self.assertEqual(snapshot.read_bytes(), raw)
            with self.assertRaises(FileExistsError):
                eval_replay.prepare(snapshot, out, 20)

    def test_business_credentials_are_removed_from_subprocess_environment(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'local').mkdir()
            (root / 'local/.env').write_text('JETAIME_DATABASE_URL=secret\nJETAIME_GAODE_API_KEY=secret\nMODEL_KEY=keep\n')
            with patch.dict(os.environ, {'JETAIME_TEST_DATABASE_URL': 'also-secret'}):
                env = eval_replay.model_environment(root)
            for key in ('JETAIME_DATABASE_URL', 'JETAIME_TEST_DATABASE_URL', 'JETAIME_GAODE_API_KEY'):
                self.assertNotIn(key, env)
            self.assertEqual(env['MODEL_KEY'], 'keep')

    def test_phoenix_inputs_do_not_include_recordings_or_reference_answers(self):
        suite = {'cases': [{'id': 'c', 'question': '问题', 'groupId': 'g', 'cutoffSeq': 5,
                           'at': '2026-10-29T00:00:00Z', 'reviewed': False,
                           'mapFixtures': [{'response': 'private fixture'}],
                           'expected': {'reference': 'reference only'}}]}
        row = phoenix_replay.examples_for(suite, 'suite', 'snapshot')[0]
        self.assertNotIn('reference', row['input'])
        self.assertNotIn('mapFixtures', row['input'])
        self.assertEqual(row['metadata']['suiteSha256'], 'suite')
        self.assertFalse(row['output']['reviewed'])

    def test_registration_bridge_rejects_unknown_discovery_policy(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            app = root / 'services/api/internal/app/modules.go'
            app.parent.mkdir(parents=True)
            for call, expected in [('search.New(catalog)', 'amap'), ('search.New(entries)', 'minimal')]:
                app.write_text(call)
                policy, code = eval_replay.registration_source(root)
                self.assertEqual(policy, expected)
                self.assertIn('search.New(', code)
            app.write_text('search.New(unknown_arguments)')
            with self.assertRaises(ValueError):
                eval_replay.registration_source(root)

    def test_missing_scores_and_provider_usage_remain_null(self):
        self.assertIsNone(phoenix_replay.score_evaluator('required_sources')({'scores': {}})['score'])
        self.assertIsNone(phoenix_replay.metric_evaluator('promptTokens')({'metrics': {}})['score'])
        self.assertEqual(phoenix_replay.score_evaluator('required_sources')(
            {'scores': {'required_sources': False}})['score'], 0)


if __name__ == '__main__':
    unittest.main()
