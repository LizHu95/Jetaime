"""Exercise deployment failure paths with an isolated fake API binary."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class DeployPreflightTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.api = self.root / 'installed' / 'api'
        self.api.mkdir(parents=True)
        self.env = dict(os.environ)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        # Supply Linux's flock semantics on macOS too. The inherited descriptor
        # shares the lock with the parent shell after this helper exits.
        if not shutil.which('flock'):
            lock = self.bin / 'flock'
            lock.write_text('#!' + sys.executable + '\nimport fcntl, sys\nfcntl.flock(int(sys.argv[-1]), fcntl.LOCK_EX)\n')
            lock.chmod(0o755)
        self.arch = 'amd64'
        uname = self.bin / 'uname'
        uname.write_text('#!/bin/sh\necho x86_64\n')
        uname.chmod(0o755)
        self.env['PATH'] = str(self.bin) + os.pathsep + self.env['PATH']
        for key in ('JETAIME_MEMORY_CONFIG', 'JETAIME_PHOENIX_TRACES_ENDPOINT'):
            self.env.pop(key, None)

    def package(self, directory, check_exit):
        directory.mkdir(parents=True, exist_ok=True)
        shutil.copy2(ROOT / 'deploy/api/start.sh', directory / 'start.sh')
        binary = '#!/bin/bash\ncase "$1" in\n-migrate) exit 0;;\n-memory-check) echo "pipeline incompatible"; exit ' + str(check_exit) + ';;\n*) exit 99;;\nesac\n'
        for arch in (self.arch,):
            path = directory / ('api-linux-' + arch)
            path.write_text(binary)
            path.chmod(0o755)
        for name in ('models.example.json', 'memory.example.json', 'README.md', '.env', 'models.json', 'memory.json'):
            (directory / name).write_text('' if name == '.env' else 'incoming')
        (directory / 'phoenix').mkdir(exist_ok=True)
        (directory / 'phoenix/install.sh').write_text('incoming')
        files = [p for p in directory.rglob('*') if p.is_file() and p.name != 'SHA256SUMS']
        (directory / 'SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.relative_to(directory)}\n' for p in files
        ))

    def test_restart_rejects_mismatch_before_removing_pid(self):
        self.package(self.api, 1)
        (self.api / 'runtime').mkdir()
        pid = self.api / 'runtime/api.pid'
        pid.write_text('123456789\n')
        result = subprocess.run(['bash', str(self.api / 'start.sh'), 'restart'], env=self.env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('未停止 API', result.stderr)
        self.assertEqual(pid.read_text(), '123456789\n')

    def test_installer_checks_before_replacing_files(self):
        source = (ROOT / 'deploy/push.sh').read_text()
        installer = source.split("api_installer=$(cat <<'SH'\n", 1)[1].split('\nSH\n)', 1)[0]
        for check_exit in (1, 0):
            with self.subTest(check_exit=check_exit):
                staging = self.root / f'upload-{check_exit}'
                package = self.root / f'package-{check_exit}' / 'api'
                self.package(package, check_exit)
                staging.mkdir()
                shutil.copytree(package, staging / 'api')
                manifest_hash = hashlib.sha256((staging / 'api/SHA256SUMS').read_bytes()).hexdigest()
                (self.api / 'memory.json').write_text('original')
                (self.api / 'api-linux-amd64').write_text('original-binary')
                result = subprocess.run(['bash', '-c', installer, '--', str(staging), str(self.api.parent), manifest_hash, self.arch], env=self.env, capture_output=True, text=True)
                if check_exit:
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual((self.api / 'memory.json').read_text(), 'original')
                    self.assertEqual((self.api / 'api-linux-amd64').read_text(), 'original-binary')
                else:
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertEqual((self.api / 'memory.json').read_text(), 'incoming')
                    self.assertTrue((staging / 'api/api-linux-amd64').exists(), 'sync cache must survive installation')
                    self.assertFalse((self.api / 'api-linux-arm64').exists())

    def install(self, cache, manifest_hash):
        source = (ROOT / 'deploy/push.sh').read_text()
        installer = source.split("api_installer=$(cat <<'SH'\n", 1)[1].split('\nSH\n)', 1)[0]
        return subprocess.run(['bash', '-c', installer, '--', str(cache), str(self.api.parent), manifest_hash, self.arch], env=self.env, capture_output=True, text=True)

    def test_checksum_and_intervening_upload_preserve_installed_files(self):
        cache = self.root / 'cache'
        self.package(cache / 'api', 0)
        manifest_hash = hashlib.sha256((cache / 'api/SHA256SUMS').read_bytes()).hexdigest()
        (self.api / 'api-linux-amd64').write_text('original')
        (cache / 'api/api-linux-amd64').write_text('corrupt')
        result = self.install(cache, manifest_hash)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.api / 'api-linux-amd64').read_text(), 'original')
        self.package(cache / 'api', 0)
        result = self.install(cache, '0' * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.api / 'api-linux-amd64').read_text(), 'original')

    def test_stale_cache_files_are_not_installed_and_binary_is_renamed(self):
        cache = self.root / 'cache'
        self.package(cache / 'api', 0)
        (cache / 'api/models.json').unlink()
        # Rebuild the manifest without the removed optional config.
        files = [p for p in (cache / 'api').rglob('*') if p.is_file() and p.name != 'SHA256SUMS']
        (cache / 'api/SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.relative_to(cache / "api")}\n' for p in files
        ))
        manifest_hash = hashlib.sha256((cache / 'api/SHA256SUMS').read_bytes()).hexdigest()
        (cache / 'api/models.json').write_text('stale-cache-config')
        (cache / 'api/.rsync-partial').mkdir()
        (cache / 'api/.rsync-partial/old').write_text('partial')
        (self.api / 'models.json').write_text('installed-config')
        (self.api / 'api-linux-amd64').write_text('running-binary')
        with (self.api / 'api-linux-amd64').open() as old:
            result = self.install(cache, manifest_hash)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertEqual(old.read(), 'running-binary')
        self.assertEqual((self.api / 'models.json').read_text(), 'installed-config')
        self.assertNotEqual((self.api / 'api-linux-amd64').read_text(), 'running-binary')
        self.assertTrue((cache / 'api/api-linux-amd64').exists())
        self.assertFalse((self.api / '.rsync-partial').exists())

    def test_single_arm64_package_installs_on_arm64_target(self):
        self.arch = 'arm64'
        (self.bin / 'uname').write_text('#!/bin/sh\necho aarch64\n')
        cache = self.root / 'cache'
        self.package(cache / 'api', 0)
        manifest_hash = hashlib.sha256((cache / 'api/SHA256SUMS').read_bytes()).hexdigest()
        result = self.install(cache, manifest_hash)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue((self.api / 'api-linux-arm64').exists())
        self.assertFalse((self.api / 'api-linux-amd64').exists())


if __name__ == '__main__':
    unittest.main()
