"""Versioned, independent checks over saved evidence; no tools or model calls."""
import hashlib
import json
import math
import re
from urllib.parse import parse_qs, urlsplit

VERSION = 'replay-checks-v1'
CHECKS = ('card_evidence', 'reminder_consistency', 'tool_policy')


def check_result(issues, applicable=True, unavailable=False):
    return {'score': (0 if issues else None if unavailable or not applicable else 1),
            'status': 'fail' if issues else 'unavailable' if unavailable else 'pass' if applicable else 'not_applicable',
            'reason': '; '.join(issue['field'] + ': ' + issue['reason'] for issue in issues) or
                      ('缺少可核对证据' if unavailable else '证据核对通过' if applicable else '不适用'),
            'issues': issues}


def tool_results(row, name=None):
    for step in row.get('run', {}).get('steps', []):
        call = step.get('input') or {}
        if (step.get('name') == 'tool_result' and isinstance(call, dict)
                and (name is None or call.get('name') == name)):
            yield call, step.get('output') or {}, step.get('status')


def tool_policy(case, row):
    expected = case.get('expected', {})
    required, forbidden = expected.get('requiredTools', []), expected.get('forbiddenTools', [])
    names = {call.get('name') for call, _, _ in tool_results(row)}
    for step in row.get('run', {}).get('steps', []):
        if step.get('name') == 'tool_call' and isinstance(step.get('output'), dict):
            names.add(step['output'].get('name'))
    issues = [{'field': 'tool.' + name, 'reason': '缺少必需调用'} for name in required if name not in names]
    issues += [{'field': 'tool.' + name, 'reason': '调用了禁止工具'} for name in forbidden if name in names]
    return check_result(issues, applicable=bool(required or forbidden))


def reminder_consistency(case, row):
    expected = case.get('expected', {}).get('reminder')
    if expected is None:
        return check_result([], applicable=False)
    issues = []
    def compare(field, actual, wanted):
        if actual != wanted:
            issues.append({'field': field, 'reason': '与审核期望不一致', 'actual': actual, 'expected': wanted})
    operations = row.get('simulatedReminderOperations')
    if type(operations) is not int:
        return check_result([], unavailable=True)
    compare('reminder.operations', operations, expected['operations'])
    calls = [s['output'].get('arguments', {}) for s in row.get('run', {}).get('steps', [])
             if s.get('name') == 'tool_call' and isinstance(s.get('output'), dict)
             and s['output'].get('name') == 'set_reminder']
    results = [(call, output) for call, output, status in tool_results(row, 'set_reminder') if status == 'succeeded']
    if not calls:
        calls = [call.get('arguments', {}) for call, _ in results]
    argument_keys = ([key for key in ('date', 'time') if expected.get(key) is not None]
                     + (['action'] if expected.get('action') else []))
    terms = expected.get('titleContainsAll', [])
    if argument_keys or terms:
        args = calls[-1] if calls else {}
        for key in argument_keys:
            compare('reminder.arguments.' + key, args.get(key), expected[key])
        for term in terms:
            if term.casefold() not in str(args.get('title', '')).casefold():
                issues.append({'field': 'reminder.arguments.title', 'reason': '缺少必需标题内容', 'expected': term})
    needs_result = (expected['operations'] > 0 or any(expected.get(key) for key in
                    ('resultStatus', 'reminderStatus', 'userId', 'sourceMessageIds')))
    if needs_result:
        compare('reminder.resultCount', len(results), 1)
        output = results[-1][1] if results else {}
        result = output.get('result') or {}
        saved = result.get('reminder') or {}
        for key, source in [('resultStatus', 'status')]:
            if expected.get(key):
                compare('reminder.result.' + source, result.get(source), expected[key])
        for key, source in [('reminderStatus', 'status'), ('userId', 'userId')]:
            if expected.get(key):
                compare('reminder.saved.' + source, saved.get(source), expected[key])
        if expected['operations'] > 0 or saved:
            for key in ('date', 'time'):
                if expected.get(key) is not None:
                    compare('reminder.saved.' + key, saved.get(key), expected[key])
            for term in terms:
                if term.casefold() not in str(saved.get('title', '')).casefold():
                    issues.append({'field': 'reminder.saved.title', 'reason': '保存标题缺少必需内容', 'expected': term})
        for source_id in expected.get('sourceMessageIds', []):
            if source_id not in (output.get('sourceMessageIds') or []):
                issues.append({'field': 'reminder.sources', 'reason': '缺少已验证来源', 'expected': source_id})
    return check_result(issues)


def number(value):
    if value is None or isinstance(value, bool) or not isinstance(value, (str, int, float)):
        return None
    try:
        value = float(value)
        return value if math.isfinite(value) and 0 <= value <= 1e10 else None
    except (ValueError, OverflowError):
        return None


def hours(value):
    """Normalize punctuation and one-digit hours, without changing interval meaning."""
    value = re.sub(r'\s+', '', str(value)).replace('：', ':')
    value = re.sub(r'[–—~～至]', '-', value)
    return re.sub(r'(?<!\d)(\d):(\d\d)', r'0\1:\2', value)


def card_evidence(row):
    cards = row.get('run', {}).get('cards') or []
    if not cards:
        return check_result([], applicable=False)
    places, routes = {}, {}
    private_routes = False
    for call, output, status in tool_results(row):
        name = call.get('name', '')
        if status != 'succeeded' or not name.startswith('maps_') or output.get('status') != 'ok':
            continue
        for place in output.get('places', []):
            identifier = place.get('id')
            if not identifier or not place.get('name'):
                continue
            previous = places.get(identifier, {})
            merged = dict(place)
            for field in ('address', 'city', 'cost', 'rating', 'openingHours'):
                if merged.get(field) in (None, ''):
                    merged[field] = previous.get(field)
            places[identifier] = merged
        mode = name.removeprefix('maps_direction_')
        if mode not in ('walking', 'driving', 'bicycling', 'transit_integrated'):
            continue
        args = call.get('arguments') or {}
        if args.get('origin') == 'current' or args.get('destination') == 'current':
            private_routes = True
            continue
        keys = ('origin', 'destination', 'city', 'cityd') if mode == 'transit_integrated' else ('origin', 'destination')
        canonical = json.dumps({key: args.get(key, '') for key in keys}, ensure_ascii=False, separators=(',', ':'))
        for char, escaped in [('&', '\\u0026'), ('<', '\\u003c'), ('>', '\\u003e'), ('\u2028', '\\u2028'), ('\u2029', '\\u2029')]:
            canonical = canonical.replace(char, escaped)
        for index, route in enumerate(output.get('routes', [])):
            identifier = 'amap:route:' + hashlib.sha256(f'{name}:{index}:{canonical}'.encode()).hexdigest()[:24]
            routes[identifier] = (route, 'transit' if mode == 'transit_integrated' else mode, args)
    issues, unavailable, checked = [], False, 0
    seen = set()
    def mismatch(field, actual, expected, numeric=False, opening=False):
        nonlocal checked
        # Omitted optional facts are a completeness question, not an invented fact.
        if actual in (None, ''):
            return
        checked += 1
        wanted, got = (number(expected), number(actual)) if numeric else (hours(expected), hours(actual)) if opening else (expected, actual)
        equal = (got is not None and wanted is not None and math.isclose(got, wanted, abs_tol=1e-6, rel_tol=1e-9)
                 if numeric else got == wanted)
        if not equal:
            issues.append({'field': field, 'reason': '卡片与本轮工具证据冲突或缺少证据', 'actual': actual, 'expected': expected})
    for index, card in enumerate(cards):
        prefix = f'cards[{index}]'
        identifier, kind = card.get('id', ''), card.get('type')
        if identifier in seen or card.get('source') != 'amap':
            issues.append({'field': prefix + '.id/source', 'reason': '卡片身份或来源无效'})
        seen.add(identifier)
        if kind == 'place':
            place = places.get(identifier.removeprefix('amap:place:')) if identifier.startswith('amap:place:') else None
            if place is None:
                issues.append({'field': prefix + '.id', 'reason': '本轮没有该地点的成功工具证据'})
                continue
            actual = card.get('place') or {}
            if not actual.get('name'):
                issues.append({'field': prefix + '.place.name', 'reason': '缺少地点身份'})
            for field, source in [('name', 'name'), ('address', 'address'), ('city', 'city'),
                                  ('openingHours', 'openingHours'), ('costPerPerson', 'cost'), ('rating', 'rating')]:
                expected = place.get(source)
                if field == 'rating' and number(expected) is not None and number(expected) > 5:
                    expected = None
                mismatch(prefix + '.place.' + field, actual.get(field), expected,
                         numeric=field in ('costPerPerson', 'rating'), opening=field == 'openingHours')
            mismatch(prefix + '.title', card.get('title'), place['name'])
            link = urlsplit(actual.get('mapUrl', ''))
            query = parse_qs(link.query)
            if (link.scheme != 'https' or link.netloc != 'uri.amap.com' or link.path != '/marker'
                    or query.get('poiid') != [place['id']]):
                issues.append({'field': prefix + '.place.mapUrl', 'reason': '地图链接不对应证据地点'})
        elif kind == 'route':
            evidence = routes.get(identifier)
            if evidence is None:
                if private_routes:
                    unavailable = True
                else:
                    issues.append({'field': prefix + '.id', 'reason': '本轮没有该路线的成功工具证据'})
                continue
            route, mode, args = evidence
            actual = card.get('route') or {}
            mismatch(prefix + '.route.mode', actual.get('mode'), mode)
            for field in ('distanceMeters', 'durationSeconds', 'walkingDistanceMeters'):
                mismatch(prefix + '.route.' + field, actual.get(field), route.get(field), numeric=True)
            if actual.get('instructions'):
                mismatch(prefix + '.route.instructions', actual['instructions'], route.get('instructions'))
            link = urlsplit(actual.get('mapUrl', ''))
            query = parse_qs(link.query)
            expected_mode = {'walking': 'walk', 'driving': 'car', 'bicycling': 'ride', 'transit': 'bus'}[mode]
            standard = (link.path == '/navigation' and query.get('from') == [args['origin']]
                        and query.get('to') == [args['destination']] and query.get('mode') == [expected_mode])
            private = link.path == '/marker' and query.get('position') == [args['destination']]
            if link.scheme != 'https' or link.netloc != 'uri.amap.com' or not (standard or private):
                issues.append({'field': prefix + '.route.mapUrl', 'reason': '地图链接不对应证据路线'})
        else:
            issues.append({'field': prefix + '.type', 'reason': '不支持的卡片类型'})
    result = check_result(issues, unavailable=unavailable)
    result['checkedFields'] = checked
    return result


def replay_checks(case, row):
    if case.get('reviewed') is not True:
        return {name: check_result([], applicable=False) for name in CHECKS}
    return {'card_evidence': card_evidence(row), 'reminder_consistency': reminder_consistency(case, row),
            'tool_policy': tool_policy(case, row)}
