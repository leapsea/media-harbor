"""正式镜像与真实 SDK：验证清空详情、重新采集、覆盖原文件及平台隔离。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time
from datetime import datetime, timedelta, timezone

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('posts', ROOT / 'scripts/post-model-container-smoke.py')
model = importlib.util.module_from_spec(spec)
spec.loader.exec_module(model)
source, api = model.source, model.api
RUN = ROOT / '.test-runtime/full-initialization' / str(time.time_ns())
NAME = 'media-harbor-full-initialization-' + RUN.name
api.BASE = 'http://127.0.0.1:18886/api'

# 只替换上游提取器；更改标题验证新文件仍覆盖旧位置，而非另存一份。
SDK = source.YOUTUBE_SDK.replace('import yt_dlp', 'from pathlib import Path\nimport yt_dlp')
SDK = SDK.replace("'title':code,'channel':", "'title':code+' '+Path('/data/generation').read_text().strip(),'channel':")
IG = model.IG.replace(repr(source.YOUTUBE_SDK), repr(SDK))
YT = '#!/opt/engines/bin/python3\n' + SDK + '\nyt_dlp.main(sys.argv[1:])\n'


def counts():
    return json.loads((RUN / 'data/requests.json').read_text())


def settled(sub, root):
    def read():
        return [t for t in api.request('/tasks') if t['subscription_id'] == sub['id'] and t['id'] >= root['id']]
    tasks = api.wait(read, lambda ts: bool(ts) and all(t['status'] not in ('queued', 'running') for t in ts))
    assert all(t['status'] == 'completed' for t in tasks), tasks
    return api.request(f"/subscriptions/{sub['id']}/posts")


def relation_snapshot(sub):
    posts = api.request(f"/subscriptions/{sub['id']}/posts")
    return {'posts': posts, 'downloads': [api.request(f"/posts/{p['id']}/downloads") for p in posts]}


def ui_hold(marker):
    if os.environ.get('SMOKE_UI_HOLD') != '1':
        return
    print('UI_' + marker.upper() + ' ' + str(RUN), flush=True)
    deadline = time.monotonic() + 180
    while time.monotonic() < deadline and not (RUN / ('continue-' + marker)).exists():
        time.sleep(.2)


def run():
    for directory in ('data', 'downloads', 'archive', 'logs', 'fixture/bin'):
        (RUN / directory).mkdir(parents=True)
    (RUN / 'fixture/source.py').write_text(source.SOURCE)
    (RUN / 'data/fixture-items.json').write_text('["first","second","orphan"]')
    (RUN / 'data/generation').write_text('original')
    for name, script in (('python3', IG), ('gallery-dl', IG), ('yt-dlp', YT)):
        path = RUN / 'fixture/bin' / name
        path.write_text(script)
        path.chmod(0o755)
    args = ['docker', 'run', '-d', '--rm', '--name', NAME, '--user', f'{os.getuid()}:{os.getgid()}',
            '-p', '127.0.0.1:18886:8080', '-e', 'PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for directory in ('data', 'downloads', 'archive', 'logs', 'fixture'):
        args += ['-v', f'{RUN / directory}:/{directory}']
    startup = '''set -e
ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4
ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png
/opt/engines/bin/python3 /fixture/source.py &
exec /app/server
'''
    subprocess.check_output(args + ['--entrypoint', '/bin/sh', api.IMAGE, '-c', startup])
    try:
        settings = api.wait(lambda: api.request('/settings'), bool)
        settings['debug']['test_item_limit'] = 10
        for platform in ('instagram', 'youtube'):
            settings['pipeline'][platform].update(min_interval_seconds=.02, max_requests=1000)
        settings['youtube'].update(video=True, format='b', metadata=True, fragments=1, comments=False,
                                   subtitles=False, auto_subtitles=False, thumbnail=False,
                                   extra_audio=False, extract_audio=False)
        api.request('/settings', 'PUT', settings)
        api.request('/cookies/instagram', 'PUT', {'content': 'sessionid=offline-fixture'})
        subs = {}
        for platform, url in (('instagram', 'https://www.instagram.com/author/'), ('youtube', 'https://www.youtube.com/@author')):
            sub = api.request('/subscriptions', 'POST', {'platform': platform, 'url': url,
                              'display_name': '全量初始化验收 ' + platform, 'enabled': False})
            subs[platform] = sub
            root = api.request(f"/subscriptions/{sub['id']}/scan?kind=full", 'POST')
            assert root.get('overwrite') is True, root
            assert len(settled(sub, root)) == 3
        report = {}
        (RUN / 'data/fixture-items.json').write_text('["first","second"]')
        (RUN / 'data/generation').write_text('updated')
        for platform, sub in subs.items():
            other = subs['youtube' if platform == 'instagram' else 'instagram']
            baseline_other = relation_snapshot(other)
            before = counts()
            media = [c for c in api.request('/contents') if c['subscription_id'] == sub['id'] and not c['remote_id'].startswith('orphan')]
            paths = [p for c in media for p in c['paths'] if p.endswith(('.mp4', '.png'))]
            # 旧文件故意写成不同内容，重新下载后的文件字节必须等于本地媒体源。
            for path in paths:
                (RUN / path.lstrip('/')).write_bytes(b'OLD-CACHE')
            now = datetime.now(timezone.utc)
            settings['sleep'][platform].update(enabled=True, start=(now-timedelta(hours=1)).strftime('%H:%M'),
                                               end=(now+timedelta(hours=1)).strftime('%H:%M'), timezone='UTC')
            api.request('/settings', 'PUT', settings)
            root = api.request(f"/subscriptions/{sub['id']}/scan?kind=full", 'POST')
            assert root.get('overwrite') is True, root
            assert api.request(f"/subscriptions/{sub['id']}/details")['items'] == []
            assert api.request(f"/subscriptions/{sub['id']}/posts") == []
            # 直接只读数据库核对两张表，而非只验证接口隐藏旧记录。
            sql = "import sqlite3,json; db=sqlite3.connect('file:/data/media.db?mode=ro',uri=True); print(json.dumps([db.execute('SELECT COUNT(*) FROM post_tasks WHERE subscription_id=?',(" + str(sub['id']) + ",)).fetchone()[0],db.execute('SELECT COUNT(*) FROM download_items WHERE content_id IN (SELECT id FROM content_items WHERE subscription_id=?)',(" + str(sub['id']) + ",)).fetchone()[0]]))"
            table_counts = json.loads(subprocess.check_output(['docker', 'exec', NAME, '/opt/engines/bin/python3', '-c', sql], text=True))
            assert table_counts == [0, 0], table_counts
            assert relation_snapshot(other) == baseline_other
            assert all((RUN / p.lstrip('/')).read_bytes() == b'OLD-CACHE' for p in paths)
            if platform == 'youtube':
                ui_hold('empty')
                # 排队状态重启后仍保留清空结果与覆盖意图，旧文件不会被当成已完成。
                subprocess.check_output(['docker', 'restart', NAME])
                api.wait(lambda: api.request('/settings'), bool)
                assert api.request(f"/subscriptions/{sub['id']}/details")['items'] == []
                # 本地 HTTP 素材进程重启后计数器归零，统计新进程实际请求。
                before = {}
            settings['sleep'][platform]['enabled'] = False
            api.request('/settings', 'PUT', settings)
            posts = settled(sub, root)
            assert len(posts) == 2 and all(p['status'] == 'completed' for p in posts), posts
            children = [d for p in posts for d in api.request(f"/posts/{p['id']}/downloads")]
            assert len(children) == 4 and all(d['status'] == 'completed' for d in children), children
            # 新标题仍覆盖旧文件名和原路径；不会保留一份新标题的重复视频。
            expected = (RUN / ('fixture/media.png' if platform == 'instagram' else 'fixture/media.mp4')).read_bytes()
            assert all((RUN / p.lstrip('/')).read_bytes() == expected for p in paths)
            refreshed = [c for c in api.request('/contents') if c['id'] in {m['id'] for m in media}]
            assert sorted(p for c in refreshed for p in c['paths'] if p.endswith(('.mp4', '.png'))) == sorted(paths)
            after = counts()
            for code in ('first', 'second'):
                key = '/post/' + platform + '/' + code
                assert after.get(key, 0) - before.get(key, 0) == 1, after
            assert after.get('/post/' + platform + '/orphan', 0) == before.get('/post/' + platform + '/orphan', 0)
            assert relation_snapshot(other) == baseline_other
            group = next(g for g in api.request('/task-groups') if g['subscription_id'] == sub['id'])
            assert group['total'] == len(media) and group['counts']['completed'] == len(media), group
            log = api.request(f"/subscriptions/{sub['id']}/log")['log']
            (RUN / (platform + '.log')).write_text(log)
            report[platform] = {'cleared_tables': table_counts, 'rebuilt_posts': 2, 'rebuilt_downloads': 4,
                                'overwritten_original_files': len(paths), 'other_subscription_unchanged': True,
                                'fresh_post_requests': 2, 'restart_while_queued': platform == 'youtube'}
            if platform == 'youtube':
                # 缓存仍在有效期内立即再次全量，必须重新解析而非重用上一轮的视频信息。
                before_repeat = counts()
                for path in paths:
                    (RUN / path.lstrip('/')).write_bytes(b'OLD-CACHE')
                (RUN / 'data/generation').write_text('updated-again')
                repeated = api.request(f"/subscriptions/{sub['id']}/scan?kind=full", 'POST')
                settled(sub, repeated)
                after_repeat = counts()
                for code in ('first', 'second'):
                    assert after_repeat['/post/youtube/' + code] - before_repeat['/post/youtube/' + code] == 1
                assert all((RUN / p.lstrip('/')).read_bytes() == expected for p in paths)
                report[platform]['full_refreshes_live_metadata_cache'] = True
            print('PASS ' + platform + ': ' + json.dumps(report[platform], ensure_ascii=False), flush=True)
        (RUN / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print('验收报告：' + str(RUN / 'report.json'), flush=True)
        ui_hold('complete')
    except Exception:
        print(subprocess.check_output(['docker', 'logs', NAME], text=True))
        raise
    finally:
        subprocess.run(['docker', 'rm', '-f', NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    run()
