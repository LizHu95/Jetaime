"""Verify package selection and checksums without production configuration."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class DeployBuildTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ('deploy/api', 'deploy/phoenix', 'services/api/config', 'services/api/cmd/api', 'local', 'bin'):
            (self.root / name).mkdir(parents=True)
        shutil.copy2(ROOT / 'deploy/api/build.sh', self.root / 'deploy/api/build.sh')
        for name in ('start.sh', 'README.md'):
            (self.root / 'deploy/api' / name).write_text('fixture')
        for name in ('models.example.json', 'memory.example.json'):
            (self.root / 'services/api/config' / name).write_text('{}')
        (self.root / 'deploy/phoenix/install.sh').write_text('fixture')
        (self.root / 'local/.env').write_text('TEST_SECRET=fixture\n')
        (self.root / 'local/memory.json').write_text('{}')
        (self.root / 'local/models.json').write_text('{}')
        go = self.root / 'bin/go'
        go.write_text('''#!/bin/bash
set -eu
printf '%s %s %s\\n' "$GOOS" "$GOARCH" "$*" >> "$GO_CALLS"
while (($#)); do
    if [[ "$1" == -o ]]; then printf '%s' "$GOARCH" > "$2"; chmod +x "$2"; exit 0; fi
    shift
done
exit 1
''')
        go.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.root / 'bin') + os.pathsep + os.environ['PATH'], GO_CALLS=str(self.root / 'go-calls'))
        subprocess.run(['git', 'init', '-q', str(self.root)], check=True, capture_output=True)
        subprocess.run(['git', '-C', str(self.root), '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.com', 'commit', '--allow-empty', '-qm', 'fixture'], check=True, capture_output=True)
        self.output = self.root / 'outputs/api'

    def build(self, *args):
        return subprocess.run(['bash', str(self.root / 'deploy/api/build.sh'), *args], env=self.env, capture_output=True, text=True)

    def verify_manifest(self):
        for line in (self.output / 'SHA256SUMS').read_text().splitlines():
            digest, name = line.split('  ', 1)
            self.assertEqual(hashlib.sha256((self.output / name).read_bytes()).hexdigest(), digest)

    def test_default_archive_retains_both_architectures(self):
        result = self.build()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.verify_manifest()
        with tarfile.open(self.output / 'api-deploy.tar.gz') as archive:
            names = archive.getnames()
            for arch in ('amd64', 'arm64'):
                self.assertIn('api/api-linux-' + arch, names)
            self.assertIn('api/SHA256SUMS', names)

    def test_single_arch_directory_cleans_stale_artifacts(self):
        self.output.mkdir(parents=True)
        for name in ('api-linux-arm64', 'api-deploy.tar.gz', 'models.json'):
            (self.output / name).write_text('stale')
        (self.root / 'local/models.json').unlink()
        result = self.build('--arch', 'amd64', '--no-archive')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.verify_manifest()
        self.assertTrue((self.output / 'api-linux-amd64').exists())
        for name in ('api-linux-arm64', 'api-deploy.tar.gz', 'models.json'):
            self.assertFalse((self.output / name).exists())
        calls = (self.root / 'go-calls').read_text().splitlines()
        self.assertEqual(len(calls), 1)
        self.assertIn('linux amd64 build -trimpath -ldflags=-s -w', calls[0])
        self.assertEqual((self.output / '.env').stat().st_mode & 0o777, 0o600)
        self.assertNotIn('TEST_SECRET', result.stdout + result.stderr)

    def test_invalid_architecture_does_not_build(self):
        result = self.build('--arch', 'invalid')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'go-calls').exists())

    @unittest.skipUnless(shutil.which('rsync'), 'rsync required')
    def test_manifest_sync_excludes_unlisted_files_and_reuses_cache(self):
        result = self.build('--arch', 'arm64', '--no-archive')
        self.assertEqual(result.returncode, 0, result.stderr)
        (self.output / 'api-deploy.tar.gz').write_text('must not transfer')
        names = [line.split('  ', 1)[1] for line in (self.output / 'SHA256SUMS').read_text().splitlines()]
        files = self.root / 'files'
        files.write_text('\n'.join([*names, 'SHA256SUMS']) + '\n')
        cache = self.root / 'cache'
        cache.mkdir()
        args = ['rsync', '-rlptzc', '--partial-dir=.rsync-partial', '--stats', '--files-from=' + str(files), str(self.output) + '/', str(cache) + '/']
        for _ in range(2):
            result = subprocess.run(args, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse((cache / 'api-deploy.tar.gz').exists())
        self.assertTrue((cache / 'api-linux-arm64').exists())
        self.assertTrue((cache / '.env').exists())
        self.assertEqual((cache / '.env').stat().st_mode & 0o777, 0o600)
        self.assertRegex(result.stdout, r'(?:Total transferred file size:|Total transferred file size)\s*0')


if __name__ == '__main__':
    unittest.main()
