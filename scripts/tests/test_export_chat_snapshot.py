"""Read-only export, credential containment, scope and truncation guarantees."""
import contextlib
import io
import json
from pathlib import Path
import sys
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'scripts'))
from export_chat_snapshot import export_sql, remote_program, validate_export


class SnapshotExportTest(unittest.TestCase):
    def payload(self, **metadata):
        row = {'id': 'message', 'groupId': '10000000-0000-4000-8000-000000000001',
               'seq': 1, 'senderType': 'user', 'senderId': 'actor', 'content': 'hello\nworld',
               'createdAt': '2026-11-01T10:00:00.17502+08:00'}
        return '\n'.join([json.dumps({'readOnly': 'on', 'isolation': 'repeatable read', **metadata}),
                          json.dumps(row)])

    def test_query_is_read_only_and_rejects_unvalidated_scope(self):
        sql = export_sql(['10000000-0000-4000-8000-000000000001'], 100)
        self.assertIn('REPEATABLE READ READ ONLY', sql)
        self.assertIn('LIMIT 101', sql)
        self.assertNotIn('password', sql.lower())
        self.assertNotIn('user_credentials', sql)
        with self.assertRaises(ValueError):
            export_sql(["'; DROP TABLE messages; --"], 100)

    def test_remote_program_does_not_put_db_uri_in_psql_arguments(self):
        with tempfile.TemporaryDirectory() as temporary:
            env = Path(temporary) / '.env'
            env.write_text('JETAIME_DATABASE_URL="postgresql://eval:p%40ss-secret@localhost/db?sslmode=disable"\n')
            program = remote_program('SELECT 1;', str(env))
            for status in [0, 1]:
                stdout, stderr = io.StringIO(), io.StringIO()
                result = subprocess.CompletedProcess([], status, 'snapshot', 'p@ss-secret provider diagnostics')
                with patch('subprocess.run', return_value=result) as run, \
                        contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                    if status:
                        with self.assertRaises(SystemExit):
                            exec(program, {})
                    else:
                        exec(program, {})
                args, kwargs = run.call_args
                self.assertNotIn('p@ss-secret', str(args))
                self.assertEqual(kwargs['env']['PGPASSWORD'], 'p@ss-secret')
                self.assertIn('default_transaction_read_only=on', kwargs['env']['PGOPTIONS'])
                self.assertNotIn('p@ss-secret', stdout.getvalue() + stderr.getvalue())

    def test_wrong_transaction_or_truncated_export_is_rejected(self):
        for values in [{'readOnly': 'off'}, {'isolation': 'read committed'}]:
            with self.assertRaises(ValueError):
                validate_export(self.payload(**values), 100, [])
        raw = self.payload()
        with self.assertRaisesRegex(ValueError, 'cap exceeded'):
            validate_export(raw + '\n' + raw.splitlines()[1], 1, [])

    def test_scope_duplicates_and_content_newlines(self):
        raw = self.payload()
        _, messages = validate_export(raw, 100, [])
        self.assertEqual(messages[0]['content'], 'hello\nworld')
        with self.assertRaises(ValueError):
            validate_export(raw + '\n' + raw.splitlines()[1], 100, [])
        with self.assertRaises(ValueError):
            validate_export(raw, 100, ['20000000-0000-4000-8000-000000000001'])


if __name__ == '__main__':
    unittest.main()
