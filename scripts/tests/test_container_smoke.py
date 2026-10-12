"""验证慢速模拟执行和真实失败的区别，不启动容器或访问网络。"""
import contextlib
import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('smoke', Path(__file__).parents[1] / 'container-smoke.py')
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class Clock:
    def __init__(self):
        self.now = 0

    def sleep(self, seconds):
        self.now += seconds


class SmokeWaitTests(unittest.TestCase):
    def wait_with_clock(self, timeout):
        clock = Clock()
        output = io.StringIO()
        with patch.object(smoke.time, 'monotonic', side_effect=lambda: clock.now), patch.object(smoke.time, 'sleep', side_effect=clock.sleep), patch.object(smoke, 'SMOKE_TIMEOUT', timeout), contextlib.redirect_stdout(output):
            result = smoke.wait_until(lambda: {'status': 'completed' if clock.now >= 45 else 'running'}, lambda value: value['status'] == 'completed', stage='下载')
        return result, output.getvalue()

    def test_emulated_download_can_complete_after_thirty_seconds(self):
        result, output = self.wait_with_clock(180)
        self.assertEqual(result['status'], 'completed')
        self.assertIn('DONE 下载: 45.0s', output)
        self.assertIn('WAIT 下载: 30.0s', output)

    def test_timeout_names_stage_and_last_state(self):
        with self.assertRaisesRegex(AssertionError, '下载超时.*30s.*running'):
            self.wait_with_clock(30)

    def test_failed_task_is_reported_immediately(self):
        with patch.object(smoke, 'request', return_value=[{'status': 'failed', 'error': 'ffmpeg 执行失败'}]):
            with self.assertRaisesRegex(AssertionError, 'ffmpeg 执行失败'):
                smoke.download_state()

    def test_diagnostics_include_independent_download_log(self):
        values = {'/tasks': [], '/contents': [], '/subscriptions': [{'id': 1}], '/subscriptions/1/log': {'log': 'yt-dlp: specific failure'}}
        output = io.StringIO()
        with patch.object(smoke, 'request', side_effect=lambda path: values[path]), patch.object(smoke, 'docker', return_value='server running'), contextlib.redirect_stdout(output):
            smoke.print_diagnostics('test-container')
        self.assertIn('specific failure', output.getvalue())
