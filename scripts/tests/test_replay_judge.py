"""Judge isolation, evidence boundaries, provider failures and score coverage."""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from replay_judge import EVALUATORS, SEMANTIC_DIMENSIONS, JudgeScore, ReplayJudge, evaluation_settings, payload_for, render_input


class ReplayJudgeTest(unittest.TestCase):
    def setUp(self):
        self.profile = {'provider': 'openai-compatible', 'model': 'qwen3.7-flash',
                        'base_url': 'https://example.test/v1', 'api_key_env': 'TEST_KEY',
                        'context_tokens': 32768, 'enable_thinking': False}
        self.config = {'default_profile': 'business-model', 'models': {'flash': self.profile}}
        self.case = {'id': 'c', 'groupId': 'g', 'actorId': 'u', 'cutoffSeq': 1,
                     'at': '2026-11-01T00:00:00Z', 'question': '营业时间？', 'reviewed': True,
                     'expected': {'reference': '卡片中的09:00-21:00即完整回答'}}
        self.messages = [{'id': 'm', 'groupId': 'g', 'seq': 1, 'senderType': 'user',
                          'content': '过去原文', 'createdAt': '2026-10-31T00:00:00Z'},
                         {'id': 'future', 'groupId': 'g', 'seq': 2, 'content': '不应泄漏的未来消息'},
                         {'id': 'other', 'groupId': 'other', 'seq': 1, 'content': '其他群'}]
        self.row = {'caseId': 'c', 'status': 'succeeded', 'answer': {'answer': '请看卡片'},
                    'run': {'cards': [{'id': 'amap:place:p', 'source': 'amap', 'type': 'place', 'title': '咖啡馆',
                                      'place': {'name': '咖啡馆', 'openingHours': '09:00-21:00',
                                                'mapUrl': 'https://uri.amap.com/marker?poiid=p'}}],
                            'steps': [{'name': 'model_call', 'input': 'raw prompt must not be sent'},
                                      {'name': 'tool_result', 'status': 'succeeded',
                                       'input': {'name': 'maps_search_detail', 'arguments': {'id': 'p'}},
                                       'output': {'status': 'ok', 'places': [
                                           {'id': 'p', 'name': '咖啡馆', 'openingHours': '09:00-21:00'}]}}]},
                    'metrics': {'promptTokens': 50}, 'mapFixtureMisses': 0, 'simulatedReminderOperations': 0}
        self.scores = {name: {'score': 1, 'reason': '证据完整'} for name in SEMANTIC_DIMENSIONS}

    def judge(self, scores=None, status=200, finish='stop', usage=None):
        response = Mock(status_code=status)
        response.json.return_value = {'model': 'qwen3.7-flash',
            'choices': [{'finish_reason': finish, 'message': {'content': json.dumps(scores or self.scores)}}],
            'usage': usage}
        post = Mock(return_value=response)
        return ReplayJudge(self.config, 'flash', {'TEST_KEY': 'do-not-persist'}, post=post), post

    def test_only_visible_evidence_actual_tools_and_final_cards_are_sent(self):
        self.messages[0]['createdAt'] = '2026-10-31T00:00:00.175021789Z'
        judge, post = self.judge()
        before = copy.deepcopy(self.row)
        result = judge.evaluate(self.case, self.messages, self.row)
        payload, _ = json.JSONDecoder().raw_decode(post.call_args.kwargs['json']['messages'][1]['content'])
        self.assertEqual([m['id'] for m in payload['visibleMessages']], ['m'])
        self.assertEqual(payload['actual']['cards'], self.row['run']['cards'])
        self.assertEqual(len(payload['actual']['toolResults']), 1)
        self.assertNotIn('raw prompt must not be sent', json.dumps(payload))
        self.assertEqual(self.row, before)
        self.assertEqual(result['status'], 'scored')
        self.assertEqual(result['overall'], 1)
        self.assertIsNone(result['metrics']['promptTokens'])
        self.assertNotIn('do-not-persist', json.dumps(result))
        self.assertEqual(self.config['default_profile'], 'business-model')
        self.assertEqual(post.call_count, 1)

    def test_unreviewed_failed_and_missing_recordings_never_call_or_pass(self):
        for field, value in [('reviewed', False), ('reference', ''), ('status', 'failed'), ('mapFixtureMisses', 1)]:
            with self.subTest(field=field):
                case, row = copy.deepcopy(self.case), copy.deepcopy(self.row)
                if field == 'reviewed': case[field] = value
                elif field == 'reference': case['expected'][field] = value
                else: row[field] = value
                judge, post = self.judge()
                result = judge.evaluate(case, self.messages, row)
                self.assertEqual(result['status'], 'not_scored')
                self.assertIsNone(result['overall'])
                post.assert_not_called()

    def test_oversized_input_is_not_silently_truncated(self):
        judge, post = self.judge()
        self.messages[0]['content'] = '大' * 32768
        result = judge.evaluate(self.case, self.messages, self.row)
        self.assertEqual(result['reason'], 'judge_input_exceeds_budget')
        self.assertIsNone(result['overall'])
        post.assert_not_called()

    def test_retrieved_and_reference_evidence_includes_neighbours_outside_recent_window(self):
        self.messages = [{'id': f'm{i}', 'groupId': 'g', 'seq': i, 'senderType': 'user',
                          'createdAt': '2026-10-31T00:00:00Z', 'content': '历史'} for i in range(1, 101)]
        self.case.update(cutoffSeq=90, retrievedMessageIds=['m10'])
        self.case['expected']['requiredSourceIds'] = ['m30']
        payload = payload_for(self.case, self.messages, self.row)
        ids = {m['id'] for m in payload['visibleMessages']}
        self.assertTrue({'m8', 'm10', 'm12', 'm28', 'm30', 'm32', 'm90'} <= ids)
        self.assertFalse({'m1', 'm91', 'm100'} & ids)

    def test_rendered_target_keeps_paragraphs_readable_after_json_evidence(self):
        self.row['answer']['answer'] = '暂缓决定。\n\n如果日程允许且愿意自费，可以同行。'
        payload = payload_for(self.case, self.messages, self.row)
        content = render_input(payload)
        self.assertIn('暂缓决定。\n\n如果日程允许且愿意自费，可以同行。', content)
        self.assertGreater(content.index('最终回答全文'), content.index('visibleMessages'))

    def test_invalid_provider_output_is_error_with_known_usage_retained(self):
        invalid = []
        for value in (True, '1', 2, None):
            scores = copy.deepcopy(self.scores)
            scores['correctness']['score'] = value
            invalid.append(scores)
        invalid += [{'correctness': self.scores['correctness']}]
        for scores in invalid:
            judge, _ = self.judge(scores=scores, usage={'prompt_tokens': 20, 'completion_tokens': 10})
            result = judge.evaluate(self.case, self.messages, self.row)
            self.assertEqual(result['status'], 'error')
            self.assertIsNone(result['overall'])
            self.assertEqual(result['metrics']['promptTokens'], 20)
        for status, finish in [(403, 'stop'), (200, 'length')]:
            judge, post = self.judge(status=status, finish=finish)
            result = judge.evaluate(self.case, self.messages, self.row)
            self.assertEqual(result['status'], 'error')
            self.assertEqual(post.call_count, 1)

    def test_reminder_requires_independent_score_and_saved_result_evidence(self):
        self.case['expected']['reminder'] = {'operations': 1, 'date': '2026-11-02', 'userId': 'u'}
        self.row['simulatedReminderOperations'] = 1
        self.row['run']['steps'].append({'name': 'tool_result', 'status': 'succeeded',
            'input': {'name': 'set_reminder', 'arguments': {'date': '2026-11-02'}},
            'output': {'result': {'reminder': {'date': '2026-11-03', 'userId': 'u'}}}})
        judge, _ = self.judge()
        result = judge.evaluate(self.case, self.messages, self.row)
        self.assertEqual(result['overall'], 0)
        self.assertEqual(JudgeScore('reminder').evaluate(output={'judge': result})['score'], 0)
        payload = payload_for(self.case, self.messages, self.row)
        self.assertEqual(payload['actual']['toolResults'][-1]['result']['result']['reminder']['date'], '2026-11-03')
        self.assertEqual(result['semanticOverall'], 1)
        self.assertEqual(result['checks']['reminder_consistency']['score'], 0)
        self.assertEqual(JudgeScore('reminder_consistency').kind, 'CODE')
        self.scores['reminder'] = {'score': 0, 'reason': 'LLM不得改写规则提醒维度'}
        judge, _ = self.judge()
        self.assertEqual(judge.evaluate(self.case, self.messages, self.row)['status'], 'error')

    def test_provider_exceptions_never_echo_credentials_or_input(self):
        judge, post = self.judge()
        post.side_effect = RuntimeError('do-not-persist private source content')
        result = judge.evaluate(self.case, self.messages, self.row)
        self.assertEqual(result['status'], 'error')
        self.assertNotIn('do-not-persist', json.dumps(result))
        self.assertNotIn('private source content', json.dumps(result))

    def test_missing_judgement_is_not_a_pass(self):
        result = JudgeScore('quality').evaluate(output={})
        self.assertIsNone(result['score'])
        self.assertEqual(result['label'], 'not_scored')
        self.assertEqual(JudgeScore('grounding').kind, 'LLM')
        self.assertEqual(JudgeScore('quality').kind, 'CODE')

    def test_llm_pass_cannot_override_bad_card_facts_or_hide_raw_scores(self):
        self.row['run']['cards'][0]['place']['openingHours'] = '00:00-24:00'
        judge, _ = self.judge()
        result = judge.evaluate(self.case, self.messages, self.row)
        self.assertEqual(result['modelDimensions']['grounding']['score'], 1)
        self.assertEqual(result['semanticOverall'], 1)
        self.assertEqual(result['checks']['card_evidence']['score'], 0)
        self.assertEqual(result['overall'], 0)
        self.assertEqual(JudgeScore('semantic_quality').evaluate(output={'judge': result})['score'], 1)
        self.assertEqual(JudgeScore('quality').evaluate(output={'judge': result})['score'], 0)

    def test_map_policy_failure_cannot_be_attributed_to_reminder(self):
        self.case['expected'].update(reminder={'operations': 0}, forbiddenTools=['maps_search_detail'])
        judge, _ = self.judge()
        result = judge.evaluate(self.case, self.messages, self.row)
        self.assertEqual(result['checks']['tool_policy']['score'], 0)
        self.assertEqual(result['dimensions']['reminder']['score'], 1)
        self.assertEqual(result['overall'], 0)

    def test_phoenix_distinguishes_model_and_code_annotations(self):
        evaluators = {JudgeScore(name).name: JudgeScore(name) for name in EVALUATORS}
        self.assertEqual(len(evaluators), 8)
        self.assertEqual(evaluators['semantic_quality'].kind, 'LLM')
        self.assertEqual(evaluators['card_evidence'].kind, 'CODE')
        self.assertEqual(evaluators['reminder_consistency'].kind, 'CODE')
        self.assertEqual(evaluators['calibrated_quality'].kind, 'CODE')

    def test_settings_are_separate_and_optional(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.assertEqual(evaluation_settings(root), {})
            (root / 'local').mkdir()
            path = root / 'local/eval-settings.json'
            path.write_text('{"modelProfile":"flash","judgeProfile":"flash"}')
            self.assertEqual(evaluation_settings(root)['modelProfile'], 'flash')
            path.write_text('{"default_profile":"flash"}')
            with self.assertRaises(ValueError): evaluation_settings(root)


if __name__ == '__main__':
    unittest.main()
