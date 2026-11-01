"""External-run gates, missing-result accounting and comparison provenance."""
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
import tool_search_eval as comparison


class ToolSearchEvaluationTest(unittest.TestCase):
    def test_frozen_repaired_commits_keep_ancestry_and_clean_source_checks(self):
        with tempfile.TemporaryDirectory() as temp:
            roots = {v: (Path(temp) / v).resolve() for v in 'ABC'}
            hashes = {v: v.lower() * 40 for v in 'ABC'}
            inventory = '\n\n'.join('worktree '+str(roots[v])+'\nHEAD '+hashes[v] for v in 'ABC')
            def git(root, *args):
                if args == ('worktree', 'list', '--porcelain'):
                    return inventory
                if args[0] == 'rev-parse':
                    return hashes[Path(root).name] if args[1] == 'HEAD' else args[1]
                if args[0] in ['diff', 'ls-files']:
                    return ''
                self.fail(args)
            with patch.object(comparison, 'git', side_effect=git), patch.object(comparison.subprocess, 'run') as run:
                actual = comparison.source_roots({}, hashes['A'], {v: hashes[v] for v in 'BC'})
            self.assertEqual(actual, roots)
            self.assertEqual(run.call_count, 3)

    def test_ab_subset_does_not_require_or_build_c(self):
        with tempfile.TemporaryDirectory() as temp:
            roots = {v: (Path(temp) / v).resolve() for v in 'AB'}
            hashes = {v: v.lower() * 40 for v in 'AB'}
            inventory = '\n\n'.join('worktree '+str(roots[v])+'\nHEAD '+hashes[v] for v in 'AB')
            def git(root, *args):
                if args == ('worktree', 'list', '--porcelain'): return inventory
                if args[0] == 'rev-parse': return hashes[Path(root).name] if args[1] == 'HEAD' else args[1]
                if args[0] in ('diff', 'ls-files'): return ''
                self.fail(args)
            with patch.object(comparison, 'git', side_effect=git), patch.object(comparison.subprocess, 'run') as run:
                actual = comparison.source_roots({}, hashes['A'], {'B': hashes['B']}, ['A', 'B'])
            self.assertEqual(actual, roots)
            self.assertEqual(run.call_count, 2)

    def test_draft_is_offline_only_and_smoke_covers_history_and_maps(self):
        suite, protocol = comparison.load_inputs(comparison.SUITE / 'cases.draft.json',
            comparison.SUITE / 'messages.synthetic.jsonl', comparison.SUITE / 'protocol.json', 'full', False)
        self.assertEqual(len(suite['cases']), 20)
        self.assertFalse(any(c['reviewed'] for c in suite['cases']))
        self.assertEqual(comparison.Counter(c['category'] for c in protocol['cases']),
                         {'chat': 4, 'history': 4, 'reminder': 4, 'maps': 6, 'boundary': 2})
        with self.assertRaisesRegex(ValueError, 'reviewed=true'):
            comparison.load_inputs(comparison.SUITE / 'cases.draft.json',
                comparison.SUITE / 'messages.synthetic.jsonl', comparison.SUITE / 'protocol.json', 'full', True)
        smoke, _ = comparison.load_inputs(comparison.SUITE / 'cases.draft.json',
            comparison.SUITE / 'messages.synthetic.jsonl', comparison.SUITE / 'protocol.json', 'smoke', False)
        self.assertEqual({c['id'] for c in smoke['cases']}, {'history-meeting', 'maps-weather'})

    def test_online_draft_rejected_before_build_or_external_process(self):
        with patch.object(sys, 'argv', ['tool_search_eval.py', 'run']), \
                patch.object(comparison.subprocess, 'run') as run, \
                patch.object(comparison.subprocess, 'check_output') as output:
            with self.assertRaises(ValueError):
                comparison.main()
        run.assert_not_called()
        output.assert_not_called()

    def test_run_requires_explicit_model_and_sufficient_task_cap(self):
        suite = {'cases': [{'id': str(i)} for i in range(20)]}
        with patch.object(comparison, 'load_inputs', return_value=(suite, {'repetitions': 3})), \
                patch.object(comparison, 'source_roots') as roots, \
                patch.object(sys, 'argv', ['tool_search_eval.py', 'run', '--max-tasks', '179', '--model-profile', 'test']):
            with self.assertRaisesRegex(ValueError, '180'):
                comparison.main()
        roots.assert_not_called()

    def test_reviewed_cases_still_need_replay_state(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            suite = json.loads((comparison.SUITE / 'cases.draft.json').read_text())
            protocol = json.loads((comparison.SUITE / 'protocol.json').read_text())
            suite['version'] = 'reviewed-v1'
            for case in suite['cases']:
                case['reviewed'] = True
            protocol['cases'][0]['readiness'] = 'blocked_missing_state_or_recordings'
            (root / 'cases.json').write_text(json.dumps(suite))
            (root / 'protocol.json').write_text(json.dumps(protocol))
            with self.assertRaisesRegex(ValueError, 'lack replay state'):
                comparison.load_inputs(root / 'cases.json', comparison.SUITE / 'messages.synthetic.jsonl',
                                       root / 'protocol.json', 'full', True)

    def test_missing_results_and_usage_are_not_passes_or_zeroes(self):
        row = {'caseId': 'a', 'status': 'failed', 'scores': {'execution_success': False},
               'metrics': {'promptTokens': None, 'durationMs': 100}}
        result = comparison.aggregate([row], [{'id': 'a'}, {'id': 'b'}], 3)
        self.assertEqual(result['incompleteCases'], {'a': 1, 'b': 0})
        self.assertEqual(result['scores']['execution_success'], {'passed': 0, 'scored': 1})
        self.assertIsNone(result['metrics']['promptTokens']['sum'])
        self.assertEqual(len(result['failures']), 1)
        duplicate = comparison.aggregate([row] * 4, [{'id': 'a'}], 3)
        self.assertEqual(duplicate['incompleteCases'], {'a': 4})

    def test_judge_usage_and_missing_scores_are_separate_from_agent(self):
        row = {'caseId': 'a', 'status': 'succeeded', 'scores': {},
               'metrics': {'promptTokens': 50}, 'judge': {'status': 'scored', 'overall': 0,
               'dimensions': {'grounding': {'score': 0}}, 'metrics': {'promptTokens': 20}}}
        missing = {'caseId': 'b', 'status': 'succeeded', 'scores': {}, 'metrics': {},
                   'judge': {'status': 'error', 'overall': None}}
        result = comparison.aggregate([row, missing], [{'id': 'a'}, {'id': 'b'}], 1)
        self.assertEqual(result['metrics']['promptTokens']['sum'], 50)
        self.assertEqual(result['judge']['metrics']['promptTokens']['sum'], 20)
        self.assertEqual(result['judge']['scores']['quality'], {'passed': 0, 'scored': 1})
        self.assertEqual(result['judge']['statuses'], {'scored': 1, 'error': 1})

    def test_model_pass_and_combined_failure_are_both_visible(self):
        row = {'caseId': 'a', 'status': 'succeeded', 'scores': {}, 'metrics': {},
               'judge': {'status': 'scored', 'overall': 0, 'semanticOverall': 1,
                         'checks': {'card_evidence': {'score': 0}, 'reminder_consistency': {'score': 1}}}}
        result = comparison.aggregate([row], [{'id': 'a'}], 1)['judge']
        self.assertEqual(result['scores']['semantic_quality'], {'passed': 1, 'scored': 1})
        self.assertEqual(result['scores']['quality'], {'passed': 0, 'scored': 1})
        self.assertEqual(result['checks']['card_evidence'], {'passed': 0, 'scored': 1})

    def test_evidence_rule_failure_is_reported_but_semantic_only_failure_is_auxiliary(self):
        row = {'caseId': 'a', 'status': 'succeeded', 'scores': {'execution_success': True}, 'metrics': {},
               'judge': {'overall': 0, 'checks': {'card_evidence': {'score': 0}}}}
        result = comparison.aggregate([row], [{'id': 'a'}], 1)
        self.assertEqual(len(result['failures']), 1)
        self.assertEqual(result['failures'][0]['evidenceChecks']['card_evidence']['score'], 0)
        row['judge'] = {'overall': 0, 'checks': {'card_evidence': {'score': 1}}}
        result = comparison.aggregate([row], [{'id': 'a'}], 1)
        self.assertEqual(result['failures'], [])

    def test_summarize_rejects_judgement_for_modified_original_report(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            report_path, _ = self.make_reports(directory)
            judged = {'reportFile': report_path.name, 'reportSha256': 'wrong',
                      'suiteSha256': 'suite', 'snapshotSha256': 'snapshot', 'judge': {'caseId': 'case'}}
            destination = report_path.parent / 'judgements'
            destination.mkdir()
            (destination / report_path.name).write_text(json.dumps(judged))
            with self.assertRaisesRegex(ValueError, 'provenance'):
                comparison.summarize(directory)

    def test_reminder_write_failure_is_in_summary_and_phoenix_evaluator(self):
        import phoenix_replay
        row = {'caseId': 'a', 'status': 'succeeded',
               'scores': {'execution_success': True, 'reminder_operations': False,
                          'reminder_arguments': False, 'reminder_result': True}, 'metrics': {}}
        result = comparison.aggregate([row], [{'id': 'a'}], 1)
        self.assertEqual(result['scores']['reminder_operations'], {'passed': 0, 'scored': 1})
        self.assertEqual(len(result['failures']), 1)
        self.assertEqual(phoenix_replay.score_evaluator('reminder_operations')(row)['score'], 0)
        self.assertIsNone(phoenix_replay.score_evaluator('reminder_arguments')({'scores': {}})['score'])

    def make_reports(self, directory):
        source = {'commit': 'commit', 'harnessSha256': 'harness'}
        manifest = {'phase': 'smoke', 'repetitions': 1, 'suiteSha256': 'suite',
                    'snapshotSha256': 'snapshot', 'variants': {'A': {'source': source}}}
        (directory / 'comparison.json').write_text(json.dumps(manifest))
        (directory / 'cases.json').write_text(json.dumps({'cases': [{'id': 'case'}]}))
        (directory / 'protocol.json').write_text(json.dumps({'cases': [{'id': 'case', 'category': 'maps'}]}))
        (directory / 'A').mkdir()
        report = {'formatVersion': 'replay-v1', 'suiteSha256': 'suite', 'snapshotSha256': 'snapshot',
                  'source': source, 'results': [{'caseId': 'case', 'status': 'succeeded',
                  'scores': {'execution_success': True, 'map_fixtures_complete': True},
                  'metrics': {'promptTokens': 10, 'durationMs': 100}, 'traceId': 'trace'}]}
        path = directory / 'A' / 'task.json'
        path.write_text(json.dumps(report))
        return path, report

    def test_summarize_preserves_human_notes_and_splits_categories(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            self.make_reports(directory)
            summary = comparison.summarize(directory)
            self.assertEqual(summary['variants']['A']['categories']['maps']['actualRuns'], 1)
            review = directory / 'manual-review.md'
            self.assertIn('trace', review.read_text())
            review.write_text('Human decision: fail')
            comparison.summarize(directory)
            self.assertEqual(review.read_text(), 'Human decision: fail')

    def test_mixed_dataset_or_binary_source_cannot_be_summarized(self):
        for key, value in [('suiteSha256', 'other'), ('snapshotSha256', 'other'),
                           ('source', {'commit': 'other', 'harnessSha256': 'harness'})]:
            with self.subTest(key=key), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                path, report = self.make_reports(directory)
                report[key] = value
                path.write_text(json.dumps(report))
                with self.assertRaises(ValueError):
                    comparison.summarize(directory)

    def test_incomplete_runs_cannot_produce_token_or_latency_reduction(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            _, report = self.make_reports(directory)
            report['results'][0]['metrics']['completionTokens'] = 5
            (directory / 'A' / 'task.json').write_text(json.dumps(report))
            (directory / 'B').mkdir()
            (directory / 'B' / 'task.json').write_text(json.dumps(report))
            path = directory / 'comparison.json'
            manifest = json.loads(path.read_text())
            manifest['variants']['B'] = manifest['variants']['A']
            manifest['repetitions'] = 2
            path.write_text(json.dumps(manifest))
            summary = comparison.summarize(directory)
            self.assertEqual(summary['relativeToA']['B'],
                {'totalTokensChangeFraction': None, 'durationMsChangeFraction': None})
            manifest['repetitions'] = 1
            path.write_text(json.dumps(manifest))
            summary = comparison.summarize(directory)
            self.assertEqual(summary['relativeToA']['B'],
                {'totalTokensChangeFraction': 0, 'durationMsChangeFraction': 0})


if __name__ == '__main__':
    unittest.main()
