"""Exercise deployment orchestration without touching a remote server."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class DeployPushTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='deploy test ')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ('deploy/nginx', 'outputs/api', 'outputs/web', 'bin'):
            (self.root / name).mkdir(parents=True)
        shutil.copy2(ROOT / 'deploy/push.sh', self.root / 'deploy/push.sh')
        (self.root / 'deploy/nginx/default.conf').write_text('alias /var/www/jetaime/;\n')
        (self.root / 'deploy/nginx/install.sh').write_text('exit 0\n')
        (self.root / 'outputs/web/index.html').write_text('web')
        self.log = self.root / 'commands.jsonl'
        self.env = dict(os.environ, PATH=str(self.root / 'bin') + os.pathsep + os.environ['PATH'], DEPLOY_TEST_LOG=str(self.log), DEPLOY_TEST_HOME=str(self.root / 'remote home'))
        mock = '''import json, os
from pathlib import Path
import sys
name = Path(sys.argv[0]).name
args = sys.argv[1:]
entry = {'name': name, 'args': args}
if name == 'rsync':
    for arg in args:
        if arg.startswith('--files-from='):
            entry['files'] = Path(arg.split('=', 1)[1]).read_text().splitlines()
with open(os.environ['DEPLOY_TEST_LOG'], 'a') as log:
    log.write(json.dumps(entry) + '\\n')
if name == 'ssh':
    command = args[-1]
    if 'command -v rsync' in command:
        if os.environ.get('DEPLOY_TEST_MISSING_TOOL'): sys.exit(1)
        print(os.environ.get('DEPLOY_TEST_ARCH', 'x86_64'))
    elif command == 'printf "%s" "$HOME"':
        print(os.environ['DEPLOY_TEST_HOME'], end='')
if name == 'rsync' and os.environ.get('DEPLOY_TEST_UPLOAD_FAILURE'):
    sys.exit(20)
'''
        for name in ('ssh', 'rsync', 'make'):
            path = self.root / 'bin' / name
            path.write_text('#!' + sys.executable + '\n' + mock)
            path.chmod(0o755)
        self.manifest('amd64')

    def manifest(self, arch):
        binary = self.root / 'outputs/api' / ('api-linux-' + arch)
        binary.write_text('binary')
        (self.root / 'outputs/api/.env').write_text('TEST_SECRET=fixture')
        (self.root / 'outputs/api/SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256(path.read_bytes()).hexdigest()}  {path.name}\n'
            for path in (binary, self.root / 'outputs/api/.env')
        ))

    def deploy(self):
        return subprocess.run(['bash', str(self.root / 'deploy/push.sh'), 'fixture-host:~/deploy parent', '/var/www/web site'], env=self.env, capture_output=True, text=True)

    def commands(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()]

    def test_architecture_selects_build_and_manifest_only_sync(self):
        for remote_arch, arch in (('x86_64', 'amd64'), ('aarch64', 'arm64')):
            with self.subTest(arch=arch):
                self.log.unlink(missing_ok=True)
                self.env['DEPLOY_TEST_ARCH'] = remote_arch
                self.manifest(arch)
                result = self.deploy()
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                commands = self.commands()
                build = next(c for c in commands if c['name'] == 'make')
                self.assertIn(f'API_BUILD_ARGS=--arch {arch} --no-archive', build['args'])
                syncs = [c for c in commands if c['name'] == 'rsync']
                self.assertEqual(len(syncs), 2)
                self.assertEqual(syncs[0]['files'], ['api-linux-' + arch, '.env', 'SHA256SUMS'])
                self.assertIn('--partial-dir=.rsync-partial', syncs[0]['args'])
                self.assertTrue(any(a.startswith('--rsync-path=flock ') for a in syncs[0]['args']))
                self.assertIn('.jetaime-api-upload-' + arch, syncs[0]['args'][-1])
                self.assertNotIn('TEST_SECRET', result.stdout + result.stderr)

    def test_upload_failure_never_installs_or_restarts(self):
        self.env['DEPLOY_TEST_UPLOAD_FAILURE'] = '1'
        result = self.deploy()
        self.assertNotEqual(result.returncode, 0)
        commands = self.commands()
        self.assertEqual(len([c for c in commands if c['name'] == 'rsync']), 1)
        self.assertFalse(any('api/start.sh restart' in ' '.join(c['args']) for c in commands))
        self.assertFalse(any('manifest_hash=$3' in ' '.join(c['args']) for c in commands))

    def test_missing_remote_tool_stops_before_build(self):
        self.env['DEPLOY_TEST_MISSING_TOOL'] = '1'
        result = self.deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual([c['name'] for c in self.commands()], ['ssh'])


if __name__ == '__main__':
    unittest.main()
