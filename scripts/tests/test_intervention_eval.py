import importlib.util
from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location('intervention_eval', SCRIPTS / 'intervention_eval.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class SummaryTests(unittest.TestCase):
    def case(self, target='skip', reviewed=False):
        return {'reference': {'productAction': target}, 'review': {'reviewed': reviewed},
                'windowReferences': {'15': {'action': 'intervene'}, '30': {'action': target}},
                'dataOrigin': 'hand-authored-synthetic', 'category': 'refused_ai'}

    def row(self, window=15, action='intervene', status='succeeded', usage=None):
        return {'caseId': 'n', 'window': window, 'status': status, 'decision': {'action': action},
                'modelCalls': [{'promptTokens': usage, 'completionTokens': usage}], 'durationMs': 10}

    def test_missing_refusal_still_counts_product_false_intervention(self):
        result = module.aggregate([self.row()], {'n': self.case()})['15']
        self.assertIsNone(result['reviewedQuality'])
        provisional = result['exploratoryOnly']
        self.assertEqual(provisional['windowActionAgreement'], {'matches': 1, 'executions': 1})
        self.assertEqual(provisional['negativeCases']['falseInterventions'], 1)
        self.assertIsNone(result['promptTokens'])
        self.assertFalse(result['providerUsageComplete'])

    def test_failures_are_visible_and_not_counted_as_success(self):
        result = module.aggregate([self.row(status='failed')], {'n': self.case()})['15']
        self.assertEqual(result['failed'], 1)
        self.assertEqual(result['succeeded'], 0)
        self.assertEqual(result['exploratoryOnly']['negativeCases']['succeeded'], 0)
        self.assertEqual(result['actions'], {})

    def test_verify_is_separate_from_direct_intervention_and_skip(self):
        result = module.aggregate([self.row(action='verify')], {'n': self.case('intervene')})['15']
        positive = result['exploratoryOnly']['positiveCases']
        self.assertEqual(positive, {'succeeded': 1, 'directInterventions': 0, 'verify': 1, 'skip': 0})

    def test_reviewed_scores_and_complete_repair_usage(self):
        row = self.row(window=30, action='skip', usage=4)
        row['modelCalls'].append({'promptTokens': 6, 'completionTokens': 2})
        result = module.aggregate([row], {'n': self.case(reviewed=True)})['30']
        self.assertEqual(result['reviewedQuality'], {'matches': 1, 'executions': 1})
        self.assertEqual(result['modelCalls'], 2)
        self.assertEqual(result['promptTokens'], 10)
        self.assertEqual(result['completionTokens'], 6)


if __name__ == '__main__':
    unittest.main()
