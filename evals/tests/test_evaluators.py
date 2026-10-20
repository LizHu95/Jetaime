import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

import httpx

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from evaluators import OllamaJudge, execution_success, source_integrity, reference_rules
from runner import load_cases

# Minimal protocol-only data, isolated from the debug console and real evaluation cases.
FIXTURE = {
    'users': [{'id': 'lin', 'nickname': '测试成员'}, {'id': 'chen', 'nickname': '隔离成员'}],
    'groups': [{'id': 'daily', 'name': '测试群'}, {'id': 'other', 'name': '隔离群'}],
    'members': [{'groupId': 'daily', 'userId': 'lin'}, {'groupId': 'other', 'userId': 'chen'}],
    'messages': [
        {'id': 'm2', 'groupId': 'daily', 'senderId': 'lin', 'sentAt': '2026-10-10T10:00:00+08:00', 'text': '测试内容'},
        {'id': 'm8', 'groupId': 'other', 'senderId': 'chen', 'sentAt': '2026-10-10T10:01:00+08:00', 'text': '隔离内容'},
    ],
}
INPUT = {'fixture': FIXTURE, 'groupId': 'daily', 'actorId': 'lin', 'kind': 'qa', 'question': '预算？'}
OUTPUT = {'run': {'status': 'succeeded', 'answer': {'status': 'answered', 'answer': '200元以内', 'sourceMessageIds': ['m2']},
                   'steps': [{'name': 'build_context', 'output': {'groupId': 'daily', 'messages': [m for m in FIXTURE['messages'] if m['groupId']=='daily']}}]}}

class EvaluatorsTests(unittest.TestCase):
    def test_reference_absent_is_not_pass(self):
        for reference in [{}, {'reviewed': False, 'suggestionCount': 1}]:
            result = reference_rules(INPUT, OUTPUT, reference)
            self.assertEqual(result['label'], 'pending_reference')
            self.assertNotIn('score', result)

    def test_source_id_validity_not_semantic_support(self):
        out = copy.deepcopy(OUTPUT)
        out['run']['answer']['answer'] = '编造的内容'
        self.assertEqual(source_integrity(INPUT, out)['score'], 1)
        out['run']['answer']['sourceMessageIds'] = ['m8']
        self.assertEqual(source_integrity(INPUT, out)['score'], 0)
        out['run']['answer']['sourceMessageIds'] = ['m2', 'm2']
        self.assertEqual(source_integrity(INPUT, out)['score'], 0)
        self.assertEqual(source_integrity({**INPUT, 'kind': 'main'}, out)['score'], 0)
        out['run']['basis'] = 'general'
        out['run']['answer']['sourceMessageIds'] = []
        self.assertEqual(source_integrity({**INPUT, 'kind': 'main'}, out)['score'], 1)

    def test_failure_and_reference_rules(self):
        self.assertEqual(execution_success({'run': {'status': 'failed'}, 'error': 'timeout'})['score'], 0)
        expected = {'reviewed': True, 'answerStatus': 'answered', 'requiredSourceMessageIds': ['m2'], 'forbiddenSourceMessageIds': ['m8']}
        self.assertEqual(reference_rules(INPUT, OUTPUT, expected)['score'], 1)
        expected['requiredSourceMessageIds'] = ['m6']
        self.assertEqual(reference_rules(INPUT, OUTPUT, expected)['score'], 0)

    def test_action_assertions_catch_omission_substitution_and_wrong_fields(self):
        input = {**INPUT, 'kind': 'extract'}
        required = {'titleContains': ['回家'], 'assigneeId': 'lin', 'timeExpression': '晚上', 'requiredSourceMessageIds': ['m2']}
        expected = {'reviewed': True, 'suggestionCount': 1, 'requiredSuggestions': [required], 'forbiddenSuggestions': [{'titleContains': ['教材']}]}
        valid = {'title': '骑车回家', 'assigneeId': 'lin', 'timeExpression': '晚上', 'sourceMessageIds': ['m2']}
        output = {'run': {'status': 'succeeded', 'suggestions': [valid]}}
        self.assertEqual(reference_rules(input, output, expected)['score'], 1)
        for name, changes in {
            'wrong action with same count': {'title': '买教材'},
            'wrong owner': {'assigneeId': 'chen'},
            'wrong time': {'timeExpression': '明天'},
            'wrong source': {'sourceMessageIds': ['m8']},
        }.items():
            with self.subTest(name=name):
                out = {'run': {'status': 'succeeded', 'suggestions': [{**valid, **changes}]}}
                result = reference_rules(input, out, expected)
                self.assertEqual(result['score'], 0)
                self.assertIn('缺失或字段不符', result['explanation'])
        self.assertEqual(reference_rules(input, {'run': {'status': 'succeeded', 'suggestions': []}}, expected)['score'], 0)

    def test_one_merged_suggestion_cannot_satisfy_two_actions(self):
        expected = {'reviewed': True, 'requiredSuggestions': [{'titleContains': ['健身']}, {'titleContains': ['理发']}]}
        input = {**INPUT, 'kind': 'extract'}
        merged = {'title': '健身并理发', 'sourceMessageIds': ['m2']}
        result = reference_rules(input, {'run': {'status': 'succeeded', 'suggestions': [merged]}}, expected)
        self.assertEqual(result['score'], 0)
        separate = [{'title': '理发'}, {'title': '健身'}]
        self.assertEqual(reference_rules(input, {'run': {'status': 'succeeded', 'suggestions': separate}}, expected)['score'], 1)

    def test_forbidden_action_is_checked_without_required_actions(self):
        expected = {'reviewed': True, 'forbiddenSuggestions': [{'titleContains': ['教材']}]}
        input = {**INPUT, 'kind': 'extract'}
        output = {'run': {'status': 'succeeded', 'suggestions': [{'title': '购买教材'}]}}
        result = reference_rules(input, output, expected)
        self.assertEqual(result['score'], 0)
        self.assertIn('出现禁止行动', result['explanation'])

    def test_judge_requires_reviewed_reference_and_isolates_group(self):
        judge = OllamaJudge('http://test.invalid', 'judge')
        with patch('evaluators.httpx.post') as post:
            self.assertNotIn('score', judge.evaluate(INPUT, OUTPUT, {'reviewed': False}))
            post.assert_not_called()
        seen = {}
        def respond(url, **kw):
            seen.update(kw['json'])
            return httpx.Response(200, request=httpx.Request('POST', url), json={'done': True, 'message': {'content': json.dumps({'label': 'insufficient_evidence', 'explanation': '参考有歧义'})}})
        with patch('evaluators.httpx.post', side_effect=respond):
            tool_output = copy.deepcopy(OUTPUT)
            tool_output['run']['steps'].insert(0, {'name': 'build_context', 'output': {'groupId':'daily','messages':[]}})
            result = judge.evaluate(INPUT, tool_output, {'reviewed': True, 'reference': {'answer': '200元'}})
        self.assertNotIn('score', result)
        self.assertNotIn('"m8"', seen['messages'][1]['content'])
        self.assertNotIn('fixture', json.loads(seen['messages'][1]['content']))
        self.assertIn('m2', [m['id'] for m in json.loads(seen['messages'][1]['content'])['context']['messages']])

    def test_truncated_or_invalid_judge_is_error_not_fail_score(self):
        judge = OllamaJudge('http://test.invalid', 'judge')
        for body in [{'done': True, 'done_reason': 'length'}, {'done': True, 'message': {'content': '{"label":"pass","explanation":""}'}}]:
            response = httpx.Response(200, request=httpx.Request('POST', 'http://test.invalid'), json=body)
            with patch('evaluators.httpx.post', return_value=response), self.assertRaises((ValueError, KeyError)):
                judge.evaluate(INPUT, OUTPUT, {'reviewed': True, 'reference': {'answer': '200元'}})

    def test_schema_and_unique_case_ids(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/'cases.json'
            path.write_text('[]'); self.assertEqual(load_cases(path)[0], [])
            case = {'id': 'test-only', 'input': INPUT}
            path.write_text(json.dumps([case, case]))
            with self.assertRaises(ValueError): load_cases(path)
            case['input'] = copy.deepcopy(INPUT); case['input']['fixture']['messages'][0]['sentAt'] = 'bad timestamp'
            path.write_text(json.dumps([case]))
            with self.assertRaises(Exception): load_cases(path)

if __name__ == '__main__': unittest.main()
