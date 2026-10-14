"""正式镜像与真实SDK：验证Instagram认证停用、YouTube匿名回退与更新恢复。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('incremental', ROOT / 'scripts/incremental-container-smoke.py')
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api = source.fixture
RUN = ROOT / '.test-runtime/cookies' / str(time.time_ns())
NAME = 'media-harbor-cookies-' + RUN.name
api.BASE = 'http://127.0.0.1:18883/api'

# 真正运行SDK认证错误与警告，外部平台仅替换成离线素材。
IG = source.INSTAGRAM.replace('    def items(self):', '''    def items(self):
        if self.cookies.get('sessionid') == 'revoked':
            self.request_json('http://127.0.0.1:8001/post/instagram/auth-check')
            self.log.error('HTTP redirect to login page')
            from gallery_dl.exception import AuthRequired
            raise AuthRequired()
''', 1)
YT = source.YOUTUBE.replace('    def _real_extract(self,url):', '''    def _real_extract(self,url):
        cookies = self._get_cookies('https://www.youtube.com')
        if cookies.get('SAPISID') and cookies['SAPISID'].value == 'revoked':
            self._download_json('http://127.0.0.1:8001/post/youtube/auth-check', 'auth')
            self.report_warning('The provided YouTube account cookies are no longer valid.')
            from yt_dlp.utils import ExtractorError
            raise ExtractorError('Offline authentication rejected', expected=True)
''', 1)
# python3入口嵌入了YouTube素材注册，必须同步替换该注册而非只修改独立命令。
IG = IG.replace(repr(source.YOUTUBE_SDK), repr(YT.split("\n", 1)[1].replace("yt_dlp.main(sys.argv[1:])", "")))


def tasks(sub):
    return [t for t in api.request('/tasks') if t['subscription_id'] == sub['id']]


def run():
    for name in ('data', 'downloads', 'logs', 'archive', 'fixture/bin'):
        (RUN / name).mkdir(parents=True)
    (RUN / 'fixture/source.py').write_text(source.SOURCE)
    # 一条帖子与两个图片，始终在前十条验收范围内。
    (RUN / 'data/fixture-items.json').write_text('["p1"]')
    for name, content in (('python3', IG), ('gallery-dl', IG), ('yt-dlp', YT)):
        path = RUN / 'fixture/bin' / name
        path.write_text(content)
        path.chmod(0o755)
    args = ['docker', 'run', '-d', '--rm', '--name', NAME, '--user', f'{os.getuid()}:{os.getgid()}',
            '-p', '127.0.0.1:18883:8080', '-e', 'PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for name in ('data', 'downloads', 'logs', 'archive', 'fixture'):
        args += ['-v', f'{RUN/name}:/{name}']
    args += ['--entrypoint', '/bin/sh', api.IMAGE, '-c',
             'ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4 && ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png && /opt/engines/bin/python3 /fixture/source.py & exec /app/server']
    subprocess.check_output(args)
    try:
        settings = api.wait(lambda: api.request('/settings'), bool)
        # 复用系统设置中的十条开关，不枚举历史媒体。
        settings['debug']['test_item_limit'] = 10
        for platform in ('instagram', 'youtube'):
            settings['pipeline'][platform].update(min_interval_seconds=.02, max_requests=1000)
        settings['youtube'].update(video=True, format='best', fragments=1, comments=False, metadata=True,
                                  subtitles=False, auto_subtitles=False, thumbnail=False, extra_audio=False, extract_audio=False)
        api.request('/settings', 'PUT', settings)
        assert not api.request('/cookies/instagram')['platform_enabled']
        assert api.request('/cookies/youtube')['platform_enabled']
        ig = api.request('/subscriptions', 'POST', {'platform': 'instagram', 'url': 'https://www.instagram.com/author/', 'enabled': False})
        task = api.request(f"/subscriptions/{ig['id']}/scan?kind=full", 'POST')
        time.sleep(.4)
        assert tasks(ig)[0]['status'] == 'queued'
        assert not (RUN / 'data/requests.json').exists()
        # 保存格式正确但服务端已撤销的会话，下一次尝试明确拒绝并停用。
        api.request('/cookies/instagram', 'PUT', {'content': 'sessionid=revoked'})
        status = api.wait(lambda: api.request('/cookies/instagram'), lambda v: v['state'] == 'disabled')
        assert status['configured'] and not status['usable'] and not status['platform_enabled']
        api.wait(lambda: tasks(ig), lambda ts: all(t['status'] == 'queued' for t in ts))
        # 请求计数稳定证明已停止平台重试，不能只检查UI标签。
        baseline = json.loads((RUN / 'data/requests.json').read_text())
        time.sleep(1)
        assert json.loads((RUN / 'data/requests.json').read_text()) == baseline
        print('PASS Instagram未配置时不请求；认证失败后停用Cookie并停止重试，原任务保留', flush=True)
        # 状态重启仍保留，不能重新加载已失效Cookie再尝试。
        subprocess.check_output(['docker', 'restart', NAME])
        api.wait(lambda: api.request('/cookies/instagram'), lambda v: v['state'] == 'disabled')
        assert api.request('/cookies/instagram/content')['content'].find('revoked') >= 0
        # 可选停留供浏览器验收；普通功能检查不等待，最长一分钟。
        if os.environ.get('SMOKE_UI_HOLD') == '1':
            print('UI_HOLD ' + str(RUN), flush=True)
            deadline = time.monotonic() + 60
            while not (RUN / 'continue-ui').exists() and time.monotonic() < deadline:
                time.sleep(.1)
        # YouTube失效Cookie停用后重新运行原ID，匿名请求不携带坏Cookie。
        api.request('/cookies/youtube', 'PUT', {'content': 'SAPISID=revoked; LOGIN_INFO=fixture'})
        yt = api.request('/subscriptions', 'POST', {'platform': 'youtube', 'url': 'https://www.youtube.com/@author', 'enabled': False})
        api.request(f"/subscriptions/{yt['id']}/scan?kind=full", 'POST')
        api.wait(lambda: tasks(yt), lambda ts: len(ts) >= 2 and all(t['status'] == 'completed' for t in ts))
        status = api.request('/cookies/youtube')
        assert status['state'] == 'disabled' and not status['usable'] and status['platform_enabled'], status
        assert api.request('/cookies/instagram')['state'] == 'disabled'
        # 只读验证也必须匿名回退，不能通过独立Cookie路径加载已停用凭据。
        cookie = RUN / 'data/cookies/youtube.txt'
        before = (cookie.read_bytes(), cookie.stat().st_mtime_ns)
        before_tasks = len(api.request('/tasks'))
        request = urllib.request.Request(api.BASE+'/subscriptions/dryrun', json.dumps({'platform':'youtube','url':yt['url']}).encode(), {'Content-Type':'application/json'})
        with urllib.request.urlopen(request, timeout=30) as response:
            result = [json.loads(line) for line in response]
        assert result[-1]['ok'], result
        assert before == (cookie.read_bytes(), cookie.stat().st_mtime_ns)
        assert len(api.request('/tasks')) == before_tasks
        print('PASS YouTube坏Cookie停用后匿名完成，不解除Instagram停用', flush=True)
        # 保存新Cookie恢复原扫描任务；没有手工清队列或另建任务。
        api.request('/cookies/instagram', 'PUT', {'content': 'sessionid=fresh'})
        api.wait(lambda: tasks(ig), lambda ts: len(ts) == 3 and all(t['status'] == 'completed' for t in ts))
        # 成功恢复必须对应最初入队的扫描ID。
        assert next(t for t in tasks(ig) if t['id'] == task['id'])['status'] == 'completed'
        assert api.request('/cookies/instagram')['usable']
        report = dict(instagram_requires_cookie=True, invalid_cookie_disabled=True, queue_preserved=True,
                      persists_restart=True, youtube_anonymous_fallback=True, youtube_dryrun_anonymous=True, new_cookie_resumes_original_task=True)
        (RUN / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print('PASS 新Cookie恢复原任务；报告：' + str(RUN / 'report.json'), flush=True)
    except Exception:
        # 只输出隔离素材日志，测试凭据均为固定的无效字符串。
        print(subprocess.check_output(['docker', 'logs', NAME], text=True))
        raise
    finally:
        subprocess.run(['docker', 'rm', '-f', NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    run()
