#!/usr/bin/env python3
"""Export immutable replay messages over SSH using a read-only DB transaction."""
import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import uuid

from eval_replay import ROOT, private_json


def export_sql(group_ids, maximum):
    # UUID parsing is the only route from user input into the SQL predicate.
    groups = [str(uuid.UUID(value)) for value in group_ids]
    predicate = ('WHERE m.group_id IN (' + ','.join("'" + value + "'::uuid" for value in groups) + ')'
                 if groups else '')
    return f"""BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL TIME ZONE 'Asia/Shanghai';
SELECT json_build_object('exportedAt',transaction_timestamp(),
 'readOnly',current_setting('transaction_read_only'),
 'isolation',current_setting('transaction_isolation'));
SELECT json_build_object('id',m.id::text,'groupId',m.group_id::text,'seq',m.seq,
 'senderType',m.sender_type,'senderId',coalesce(m.sender_user_id,m.sender_assistant_id)::text,
 'senderName',coalesce(u.nickname,a.name),'content',m.content,'mentionsAI',m.mentions_ai,
 'replyToId',m.reply_to_id::text,'createdAt',m.created_at)
 FROM messages m LEFT JOIN users u ON u.id=m.sender_user_id
 LEFT JOIN group_assistants a ON a.id=m.sender_assistant_id
 {predicate} ORDER BY m.group_id,m.seq LIMIT {int(maximum) + 1};
COMMIT;
"""


def remote_program(sql, env_file):
    # Credentials stay in the remote process environment. Neither the URI nor
    # psql diagnostics are returned or placed in command-line arguments.
    return f"""import os,shlex,subprocess,sys,urllib.parse
from pathlib import Path
try:
    url=None
    for line in Path({env_file!r}).expanduser().read_text().splitlines():
        line=line.strip()
        if line.startswith('export '): line=line[7:].strip()
        key,separator,value=line.partition('=')
        if separator and key.strip()=='JETAIME_DATABASE_URL':
            parts=shlex.split(value,comments=True,posix=True)
            if len(parts)!=1: raise ValueError()
            url=parts[0]
    if not url: raise ValueError()
    parsed=urllib.parse.urlsplit(url)
    if parsed.scheme not in ('postgres','postgresql'): raise ValueError()
    options=urllib.parse.parse_qs(parsed.query)
    supported={{'sslmode':'PGSSLMODE','sslrootcert':'PGSSLROOTCERT','sslcert':'PGSSLCERT',
               'sslkey':'PGSSLKEY','connect_timeout':'PGCONNECT_TIMEOUT',
               'application_name':'PGAPPNAME'}}
    if set(options)-set(supported): raise ValueError()
    env={{key:value for key,value in os.environ.items() if not key.startswith('PG')}}
    env.update(PGHOST=parsed.hostname or '',PGPORT=str(parsed.port or 5432),
        PGUSER=urllib.parse.unquote(parsed.username or ''),
        PGPASSWORD=urllib.parse.unquote(parsed.password or ''),
        PGDATABASE=urllib.parse.unquote(parsed.path.lstrip('/')),
        PGCLIENTENCODING='UTF8',PGCONNECT_TIMEOUT='10',
        PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=30000 -c lock_timeout=1000')
    for key,values in options.items(): env[supported[key]]=values[-1]
    result=subprocess.run(['psql','-X','-q','-t','-A','-v','ON_ERROR_STOP=1','-f','-'],
        input={sql!r},text=True,encoding='utf-8',env=env,capture_output=True,timeout=60)
    if result.returncode: raise ValueError()
    sys.stdout.write(result.stdout)
except Exception:
    sys.stderr.write('Read-only export failed; connection details and database diagnostics withheld.\\n')
    sys.exit(1)
"""


def validate_export(raw, maximum, requested_groups):
    lines = raw.splitlines()
    if not lines:
        raise ValueError('empty export')
    metadata = json.loads(lines[0])
    if metadata.get('readOnly') != 'on' or metadata.get('isolation') != 'repeatable read':
        raise ValueError('export did not use a repeatable-read, read-only transaction')
    messages = [json.loads(line) for line in lines[1:] if line.strip()]
    if not messages or len(messages) > maximum:
        raise ValueError('empty export or message cap exceeded; no truncated snapshot was saved')
    groups = {str(uuid.UUID(value)) for value in requested_groups}
    ids, sequences = set(), set()
    for message in messages:
        key = (message['groupId'], message['seq'])
        if (message['id'] in ids or key in sequences or message['seq'] <= 0
                or message['senderType'] not in ('user', 'assistant')
                or not message['senderId'] or not message['content'].strip()
                or (groups and message['groupId'] not in groups)):
            raise ValueError('invalid message identity, scope or sequence in export')
        stamp = message['createdAt'].replace('Z', '+00:00')
        # PostgreSQL omits trailing fractional zeros; Python 3.9 accepts only
        # 3/6 fractional digits here. Normalize for validation, preserve original.
        stamp = re.sub(r'\.(\d+)(?=[+-])', lambda m: '.' + m.group(1).ljust(6, '0')[:6], stamp)
        when = datetime.fromisoformat(stamp)
        if when.tzinfo is None:
            raise ValueError('message timestamp requires timezone')
        ids.add(message['id'])
        sequences.add(key)
    return metadata, messages


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--host', default='jetaime-server')
    parser.add_argument('--remote-env', default='~/api/.env')
    parser.add_argument('--group-id', action='append', default=[], help='repeatable; default all conversations')
    parser.add_argument('--max-messages', type=int, default=50000, help='fail rather than silently truncate')
    parser.add_argument('--output-dir', type=Path)
    args = parser.parse_args()
    if not re.fullmatch(r'[A-Za-z0-9_.@:-]+', args.host) or args.host.startswith('-'):
        parser.error('invalid SSH host')
    if not 1 <= args.max_messages <= 1000000:
        parser.error('max-messages must be 1-1000000')
    sql = export_sql(args.group_id, args.max_messages)
    program = remote_program(sql, args.remote_env)
    completed = subprocess.run(['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10',
                                '--', args.host, 'python3', '-'], input=program,
                               text=True, encoding='utf-8', capture_output=True, timeout=90)
    if completed.returncode:
        raise ValueError('SSH/read-only export failed; remote diagnostics withheld')
    metadata, messages = validate_export(completed.stdout, args.max_messages, args.group_id)
    outdir = args.output_dir.resolve() if args.output_dir else ROOT / 'local/data/evals/snapshots' / (
        datetime.now(timezone.utc).strftime('chat-messages-%Y%m%dT%H%M%SZ-') + uuid.uuid4().hex[:8])
    outdir.mkdir(parents=True, exist_ok=False, mode=0o700)
    raw = ''.join(json.dumps(m, ensure_ascii=False) + '\n' for m in messages).encode('utf-8')
    path = outdir / 'messages.jsonl'
    with path.open('xb') as out:
        path.chmod(0o600)
        out.write(raw)
    path.chmod(0o400)
    counts = Counter(m['groupId'] for m in messages)
    manifest = {**metadata, 'formatVersion': 'chat-snapshot-v1', 'host': args.host,
                'requestedGroups': args.group_id, 'messageCount': len(messages),
                'groupCounts': dict(counts),
                'userMessages': sum(m['senderType'] == 'user' for m in messages),
                'aiRequests': sum(m['senderType'] == 'user' and m['mentionsAI'] for m in messages),
                'snapshotSha256': hashlib.sha256(raw).hexdigest(),
                'querySha256': hashlib.sha256(sql.encode()).hexdigest(),
                'senderIdentity': 'user or group-assistant ID; no account credentials exported',
                'evaluationStatus': 'not run; references require review'}
    private_json(outdir / 'manifest.json', manifest)
    print(json.dumps({'directory': str(outdir), 'messages': len(messages), 'groups': len(counts),
                      'aiRequests': manifest['aiRequests'], 'readOnly': True}, ensure_ascii=False))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        print('Snapshot export failed: ' + str(error), file=sys.stderr)
        sys.exit(1)
