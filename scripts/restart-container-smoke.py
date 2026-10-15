"""正式镜像与真实 yt-dlp：中断单个媒体后，恢复原任务且详情不残留停止说明。"""
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('source', ROOT / 'scripts/incremental-container-smoke.py')
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api = source.fixture
api.BASE = 'http://127.0.0.1:18885/api'
RUN = ROOT / '.test-runtime/restart' / str(time.time_ns())
NAME = 'media-harbor-restart-' + RUN.name

# 只阻塞本地媒体响应；真实 Go 生命周期、SDK 请求和下载器仍参与验收。
HTTP = source.SOURCE.replace('import json,threading', 'import json,threading,time')
HTTP = HTTP.replace("        if self.path.startswith('/profile/'):", """        if self.path.startswith('/media/'):
            Path('/data/media-waiting').touch()
            while not Path('/data/release-media').exists(): time.sleep(.05)
        if self.path.startswith('/profile/'):""")
WRAPPER = '#!/opt/engines/bin/python3\n' + source.YOUTUBE_SDK + """
if sys.argv[1:3]==['-u','-c']:
    code=sys.argv[3];sys.argv=['yt-dlp']+sys.argv[4:];exec(code)
else:
    yt_dlp.main(sys.argv[1:])
"""


def media_task(sid):
    return max((t for t in api.request('/tasks') if t['subscription_id'] == sid and t['content_id']),
               key=lambda task: task['id'], default=None)


def run():
    for name in ('data', 'downloads', 'archive', 'logs', 'fixture/bin'):
        (RUN / name).mkdir(parents=True)
    (RUN / 'fixture/source.py').write_text(HTTP)
    (RUN / 'data/fixture-items.json').write_text('["resume"]')
    for name in ('python3', 'yt-dlp'):
        path = RUN / 'fixture/bin' / name
        path.write_text(WRAPPER)
        path.chmod(0o755)
    # 保留容器用于停止后检查持久状态；最终只删除本脚本创建的容器。
    args = ['docker', 'run', '-d', '--name', NAME, '--user', f'{os.getuid()}:{os.getgid()}',
            '-p', '127.0.0.1:18885:8080', '-e',
            'PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for name in ('data', 'downloads', 'archive', 'logs', 'fixture'):
        args += ['-v', f'{RUN/name}:/{name}']
    startup = '''set -e
ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4
/opt/engines/bin/python3 /fixture/source.py &
exec /app/server
'''
    subprocess.check_output(args + ['--entrypoint', '/bin/sh', api.IMAGE, '-c', startup])
    report = {}
    try:
        settings = api.wait(lambda: api.request('/settings'), bool)
        settings['debug']['test_item_limit'] = 10
        settings['pipeline']['youtube'].update(min_interval_seconds=.01, max_requests=1000)
        settings['youtube'].update(video=True, format='best', metadata=True, comments=True, subtitles=True,
                                   auto_subtitles=False, thumbnail=False, extra_audio=False, extract_audio=False)
        api.request('/settings', 'PUT', settings)
        sub = api.request('/subscriptions', 'POST', {'platform': 'youtube', 'url': 'https://www.youtube.com/@resume',
                          'display_name': '重启恢复样本', 'enabled': False})
        for mode in ('graceful', 'abrupt'):
            (RUN / 'data/media-waiting').unlink(missing_ok=True)
            (RUN / 'data/release-media').unlink(missing_ok=True)
            api.request(f"/subscriptions/{sub['id']}/scan?kind=full", 'POST')
            before = api.wait(lambda: media_task(sub['id']), lambda t: t and t['status'] == 'running'
                              and (RUN / 'data/media-waiting').exists())
            work = sorted((RUN / 'downloads/.work').iterdir())
            command = ['docker', 'stop', '--time', '10', NAME] if mode == 'graceful' else ['docker', 'kill', NAME]
            subprocess.check_output(command)
            with sqlite3.connect(f'file:{RUN / "data/media.db"}?mode=ro', uri=True) as db:
                stopped = json.loads(db.execute('SELECT payload FROM tasks WHERE id=?', (before['id'],)).fetchone()[0])
            assert stopped['status'] == ('queued' if mode == 'graceful' else 'running'), stopped
            if mode == 'graceful':
                assert stopped['error'] == '服务停止，等待继续执行', stopped
            subprocess.check_output(['docker', 'start', NAME])
            resumed = api.wait(lambda: media_task(sub['id']), lambda t: t and t['status'] == 'running'
                               and t['attempts'] == before['attempts'] + 1)
            assert resumed['id'] == before['id'] and resumed['content_id'] == before['content_id'], resumed
            assert sorted((RUN / 'downloads/.work').iterdir()) == work, work
            details = api.request(f"/subscriptions/{sub['id']}/details")
            assert len(details['items']) == 1, details
            item = details['items'][0]
            assert item['status'] == 'running' and not item.get('error'), item
            assert all(not part.get('error') for part in item['parts']), item
            assert any(part['kind'] == 'comments' and part['status'] == 'queued' for part in item['parts']), item
            (RUN / f'{mode}-details.json').write_text(json.dumps(details, ensure_ascii=False, indent=2))
            # 与已有容器验收一样，必要时短暂保留进行中的真实界面供渲染检查。
            if mode == 'graceful' and os.environ.get('SMOKE_UI_HOLD') == '1':
                print('UI_HOLD ' + str(RUN), flush=True)
                deadline = time.monotonic() + 60
                while time.monotonic() < deadline and not (RUN / 'continue-ui').exists():
                    time.sleep(.2)
            (RUN / 'data/release-media').touch()
            finished = api.wait(lambda: media_task(sub['id']), lambda t: t and t['status'] in ('completed', 'failed'))
            assert finished['status'] == 'completed', finished
            report[mode] = {'same_task_and_content': True, 'same_work_directory': True,
                            'details_error_cleared': True, 'pending_comments_preserved': True,
                            'final_status': finished['status']}
            print('PASS ' + mode + ': ' + json.dumps(report[mode], ensure_ascii=False), flush=True)
        (RUN / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print('容器验收结果：' + str(RUN / 'report.json'), flush=True)
    finally:
        subprocess.run(['docker', 'rm', '-f', NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    run()
