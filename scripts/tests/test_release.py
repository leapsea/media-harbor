"""版本策略测试：依赖更新只增加补丁号，不依赖真实上游服务。"""
import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import json
import os
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('release', Path(__file__).parents[1] / 'release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def test_patch_has_no_decimal_carry(self):
        for before, after in [('1.0.1', '1.0.2'), ('1.0.9', '1.0.10'), ('1.0.99', '1.0.100')]:
            self.assertEqual(release.next_version(before, []), after)

    def test_tags_stay_on_current_version_line(self):
        self.assertEqual(release.next_version('1.0.9', ['v1.0.20', 'v2.0.99', 'v1.0.21-beta']), '1.0.21')

    def test_multiple_updates_have_one_patch(self):
        current = dict(version='1.0.9', alpine='3.23', gallery_dl='1.0', yt_dlp='2026.1.1', ffmpeg='8.0-r1')
        latest = dict(gallery_dl='1.1', yt_dlp='2026.2.1', ffmpeg='8.0-r2')
        result, changed = release.candidate(current, latest, [])
        self.assertEqual(result['version'], '1.0.10')
        self.assertEqual(len(changed), 3)
        self.assertEqual(current['version'], '1.0.9')
        unchanged, changed = release.candidate(result, dict(latest, gallery_dl='1.0'), [])
        self.assertEqual(unchanged, result)
        self.assertEqual(changed, [])

    def test_apk_index_uses_ffmpeg_package(self):
        raw = b'P:ffmpeg\nV:8.0.1-r1\n\nP:ffmpeg-dev\nV:9.0-r0\n\n'
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode='w:gz') as archive:
            entry = tarfile.TarInfo('APKINDEX')
            entry.size = len(raw)
            archive.addfile(entry, io.BytesIO(raw))
        self.assertEqual(release.apk_versions(buffer.getvalue()), {'8.0.1-r1'})

    def test_ffmpeg_must_be_available_for_both_architectures(self):
        with patch.object(release.urllib.request, 'urlopen') as fetch, patch.object(release, 'apk_versions', side_effect=[{'8.0-r1', '9.0-r0'}, {'8.0-r1'}]):
            self.assertEqual(release.ffmpeg_version('3.23'), '8.0-r1')
            self.assertEqual(fetch.call_count, 2)

    def test_apply_writes_only_candidate_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            manifest = Path(directory) / 'release.json'
            manifest.write_text(json.dumps(dict(version='1.0.99', alpine='3.23')))
            values = dict(VERSION='1.0.100', GALLERY_DL='1.2', YT_DLP='2026.1.1', FFMPEG='8.0-r2')
            with patch.object(release, 'MANIFEST', manifest), patch.dict(os.environ, values):
                release.apply(None)
            saved = json.loads(manifest.read_text())
            self.assertEqual(saved['version'], '1.0.100')
            self.assertEqual(saved['alpine'], '3.23')
            self.assertEqual(saved['ffmpeg'], '8.0-r2')

    def test_pypi_ignores_prerelease_and_yanked(self):
        with patch.object(release.urllib.request, 'urlopen'), patch.object(release.json, 'load', return_value={'releases': {'1.0': [{}], '1.1': [{}], '2.0rc1': [{}], '2.0': [{'yanked': True}]}}):
            self.assertEqual(release.pypi_version('gallery-dl'), '1.1')
