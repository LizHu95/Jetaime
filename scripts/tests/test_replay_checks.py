"""Independent counterexamples for source identity, structured facts and attribution."""
import copy
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from replay_checks import card_evidence, reminder_consistency, replay_checks, tool_policy


def tool(name, args, output, status='succeeded'):
    return {'name': 'tool_result', 'status': status, 'input': {'name': name, 'arguments': args}, 'output': output}


class ReplayChecksTest(unittest.TestCase):
    def setUp(self):
        self.place = {'id': 'BTEST001', 'name': '合成青禾咖啡馆', 'address': '合成路18号', 'city': '上海市',
                      'cost': '35', 'rating': '4.5', 'openingHours': '09:00-21:00'}
        self.card = {'id': 'amap:place:BTEST001', 'type': 'place', 'source': 'amap', 'title': self.place['name'],
                     'place': {'name': self.place['name'], 'address': self.place['address'], 'city': self.place['city'],
                               'costPerPerson': 35, 'rating': 4.5, 'openingHours': '09:00-21:00',
                               'mapUrl': 'https://uri.amap.com/marker?poiid=BTEST001'}}
        self.row = {'run': {'cards': [self.card], 'steps': [
            tool('maps_search_detail', {'id': 'BTEST001'}, {'status': 'ok', 'places': [self.place]})]},
            'simulatedReminderOperations': 0, 'scores': {'reminder_operations': True, 'reminder_result': True}}
        self.case = {'reviewed': True, 'expected': {'reminder': {'operations': 0}, 'forbiddenTools': ['set_reminder']}}

    def test_card_positive_normalization_and_detail_enrichment(self):
        self.card['place']['openingHours'] = '9：00 ～ 21：00'
        self.card['place']['costPerPerson'] = 35.0
        self.row['run']['steps'].append(tool('maps_text_search', {}, {'status': 'ok',
            'places': [{'id': 'BTEST001', 'name': self.place['name'], 'address': '', 'openingHours': ''}]}))
        self.assertEqual(card_evidence(self.row)['score'], 1)

    def test_each_card_fact_conflict_is_rejected(self):
        for key, value in [('openingHours', '00:00-24:00'), ('costPerPerson', 350), ('rating', 4.9),
                           ('address', '其他地址'), ('city', '北京市'), ('name', '其他咖啡馆'),
                           ('mapUrl', 'https://uri.amap.com/marker?poiid=OTHER')]:
            with self.subTest(key=key):
                row = copy.deepcopy(self.row)
                row['run']['cards'][0]['place'][key] = value
                self.assertEqual(card_evidence(row)['score'], 0)

    def test_foreign_place_or_failed_lookup_never_borrows_another_places_facts(self):
        self.row['run']['steps'][0]['status'] = 'failed'
        self.assertEqual(card_evidence(self.row)['score'], 0)
        self.row['run']['steps'][0]['status'] = 'succeeded'
        self.place['id'] = 'OTHER'
        self.assertEqual(card_evidence(self.row)['score'], 0)

    def test_latest_evidence_wins_and_unknown_values_stay_unknown(self):
        latest = {**self.place, 'openingHours': '10:00-20:00', 'cost': ''}
        self.row['run']['steps'].append(tool('maps_search_detail', {}, {'status': 'ok', 'places': [latest]}))
        self.assertEqual(card_evidence(self.row)['score'], 0)
        self.card['place']['openingHours'] = '10:00-20:00'
        self.assertEqual(card_evidence(self.row)['score'], 1)
        self.row['run']['steps'] = [tool('maps_search_detail', {}, {'status': 'ok', 'places': [{**latest, 'rating': ''}]})]
        self.assertEqual(card_evidence(self.row)['score'], 0)
        self.card['place'].update(costPerPerson=None, rating=None)
        self.assertEqual(card_evidence(self.row)['score'], 1)

    def test_route_numbers_mode_url_and_instructions_are_bound_to_the_same_route(self):
        # Golden card ID comes from the real Go replay report, not a Python ID generator.
        route = {'mode': 'walking', 'distanceMeters': 1200, 'durationSeconds': 900,
                 'walkingDistanceMeters': None, 'instructions': ['沿合成路向东步行，到达目的地。'],
                 'mapUrl': 'https://uri.amap.com/navigation?from=121.500000%2C31.300000&to=121.505000%2C31.303000&mode=walk'}
        card = {'id': 'amap:route:5e8c6619f303649492fa239c', 'type': 'route', 'source': 'amap', 'route': route}
        step = tool('maps_direction_walking', {'origin': '121.500000,31.300000', 'destination': '121.505000,31.303000'},
                    {'status': 'ok', 'routes': [{'distanceMeters': '1200', 'durationSeconds': '900',
                                              'instructions': route['instructions']}]})
        row = {'run': {'cards': [card], 'steps': [step]}}
        self.assertEqual(card_evidence(row)['score'], 1)
        for field, value in [('distanceMeters', 120), ('durationSeconds', 15), ('mode', 'driving'),
                             ('instructions', ['另一条路线']), ('mapUrl', 'https://uri.amap.com/navigation?mode=car')]:
            bad = copy.deepcopy(row)
            bad['run']['cards'][0]['route'][field] = value
            with self.subTest(field=field): self.assertEqual(card_evidence(bad)['score'], 0)

    def test_private_origin_missing_from_report_is_unavailable_never_a_pass(self):
        self.row['run']['cards'] = [{'id': 'amap:route:private', 'type': 'route', 'source': 'amap', 'route': {}}]
        self.row['run']['steps'] = [tool('maps_direction_walking', {'origin': 'current'}, {'status': 'ok', 'routes': []})]
        result = card_evidence(self.row)
        self.assertIsNone(result['score'])
        self.assertEqual(result['status'], 'unavailable')

    def reminder_row(self):
        args = {'action': 'save', 'date': '2026-10-23', 'time': '15:30', 'title': 'GoodsNote面试'}
        saved = {'date': '2026-10-23', 'time': '15:30', 'title': 'GoodsNote面试', 'userId': 'actor', 'status': 'pending'}
        case = {'expected': {'reminder': {'operations': 1, **{k: args[k] for k in ('action', 'date', 'time')},
                'titleContainsAll': ['面试', 'goodsnote'], 'resultStatus': 'created', 'reminderStatus': 'pending',
                'userId': 'actor', 'sourceMessageIds': ['request', 'schedule']}}}
        output = {'result': {'status': 'created', 'reminder': saved}, 'sourceMessageIds': ['request', 'schedule']}
        row = {'simulatedReminderOperations': 1, 'run': {'steps': [tool('set_reminder', args, output)]},
               'scores': {'reminder_arguments': True, 'reminder_result': True}}
        return case, row

    def test_saved_reminder_fields_are_checked_even_if_cached_scores_claim_pass(self):
        case, row = self.reminder_row()
        self.assertEqual(reminder_consistency(case, row)['score'], 1)
        for field, value in [('date', '2026-10-24'), ('time', '15:00'), ('userId', 'peer'),
                             ('status', 'draft'), ('title', '吃饭')]:
            bad = copy.deepcopy(row)
            bad['run']['steps'][0]['output']['result']['reminder'][field] = value
            with self.subTest(field=field): self.assertEqual(reminder_consistency(case, bad)['score'], 0)
        row['run']['steps'][0]['output']['result']['reminder'] = None
        self.assertEqual(reminder_consistency(case, row)['score'], 0)

    def test_arguments_sources_result_state_and_duplicate_write_are_independent(self):
        case, row = self.reminder_row()
        mutations = [('args', 'date', '2026-10-24'), ('args', 'time', '15:00'), ('args', 'title', '吃饭'),
                     ('result', 'status', 'needs_clarification'), ('output', 'sourceMessageIds', [])]
        for target, field, value in mutations:
            bad = copy.deepcopy(row)
            step = bad['run']['steps'][0]
            obj = step['input']['arguments'] if target == 'args' else step['output']['result'] if target == 'result' else step['output']
            obj[field] = value
            with self.subTest(target=target, field=field): self.assertEqual(reminder_consistency(case, bad)['score'], 0)
        row['simulatedReminderOperations'] = 2
        self.assertEqual(reminder_consistency(case, row)['score'], 0)

    def test_map_policy_failure_never_changes_reminder_score(self):
        self.case['expected']['forbiddenTools'] = ['maps_search_detail']
        checks = replay_checks(self.case, self.row)
        self.assertEqual(checks['tool_policy']['score'], 0)
        self.assertEqual(checks['reminder_consistency']['score'], 1)
        self.assertEqual(checks['card_evidence']['score'], 1)

    def test_null_optional_fields_and_explicit_empty_date_do_not_collapse(self):
        case, row = self.reminder_row()
        case['expected']['reminder'].update(date=None, time=None, resultStatus='', reminderStatus='')
        self.assertEqual(reminder_consistency(case, row)['score'], 1)
        case['expected']['reminder']['date'] = ''
        self.assertEqual(reminder_consistency(case, row)['score'], 0)

    def test_unreviewed_no_cards_no_expectations_and_missing_counts_do_not_pass(self):
        self.assertIsNone(card_evidence({'run': {}})['score'])
        self.assertIsNone(reminder_consistency({'expected': {}}, self.row)['score'])
        self.assertIsNone(tool_policy({'expected': {}}, self.row)['score'])
        self.row.pop('simulatedReminderOperations')
        self.assertEqual(reminder_consistency(self.case, self.row)['status'], 'unavailable')
        self.case['reviewed'] = False
        self.assertTrue(all(v['score'] is None for v in replay_checks(self.case, self.row).values()))


if __name__ == '__main__':
    unittest.main()
