"""正式镜像与真实 SDK：十条范围内验证休眠排队、在途媒体收尾与恢复。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time
from datetime import datetime, timedelta, timezone

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('pipeline', ROOT / 'scripts/pipeline-container-smoke.py')
base = importlib.util.module_from_spec(spec)
spec.loader.exec_module(base)
api = base.api
RUN = ROOT / '.test-runtime/sleep' / str(time.time_ns())
NAME = 'media-harbor-sleep-' + RUN.name


def tasks_for(sub):
    return [t for t in api.request('/tasks') if t['subscription_id'] == sub['id']]


def counts():
    return json.loads((RUN / 'data/requests.json').read_text())


def run():
    for name in ('data', 'downloads', 'logs', 'archive', 'fixture/bin'):
        (RUN / name).mkdir(parents=True)
    # 复用离线平台与真实下载器，媒体响应由文件释放，避免访问真实账号。
    (RUN / 'fixture/source.py').write_text(base.HTTP)
    (RUN / 'data/fixture-items.json').write_text(json.dumps(['p5', 'p4', 'p3', 'p2', 'p1']))
    for name, script in (('python3', base.source.INSTAGRAM), ('gallery-dl', base.source.INSTAGRAM), ('yt-dlp', base.source.YOUTUBE)):
        path = RUN / 'fixture/bin' / name
        # 每个博主使用独立素材身份，避免把两个主页模拟成同一个账号。
        if name != 'yt-dlp':
            script = script.replace("'owner_id':'author'", "'owner_id':('waiting' if '/waiting/' in self.url else 'author')")
        path.write_text(script)
        path.chmod(0o755)
    args = ['docker', 'run', '-d', '--rm', '--name', NAME, '--user', f'{os.getuid()}:{os.getgid()}',
            '-p', '127.0.0.1:18883:8080', '-e', 'TZ=Asia/Singapore',
            '-e', 'PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for name in ('data', 'downloads', 'logs', 'archive', 'fixture'):
        args += ['-v', f'{RUN/name}:/{name}']
    args += ['--entrypoint', '/bin/sh', api.IMAGE, '-c',
             'ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4 && ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png && /opt/engines/bin/python3 /fixture/source.py & exec /app/server']
    subprocess.check_output(args)
    try:
        # 读取实际容器默认设置，避免只验证宿主侧配置。
        settings = api.wait(lambda: api.request('/settings'), bool)
        # 自动恢复不依靠关闭开关，原窗口仍然持久保存。
        assert settings['sleep'] == dict(enabled=False, start='00:00', end='06:00', timezone='Asia/Singapore'), settings
        settings['debug']['test_item_limit'] = 10
        settings['instagram_concurrent'] = settings['youtube_concurrent'] = 1
        settings['pipeline']['queue_capacity'] = 2
        for platform in ('instagram', 'youtube'):
            settings['pipeline'][platform].update(min_interval_seconds=.02, max_requests=1000)
        settings['youtube'].update(video=True, format='best', fragments=1, comments=False, metadata=True,
                                  subtitles=False, auto_subtitles=False, thumbnail=False, extra_audio=False, extract_audio=False)
        api.request('/settings', 'PUT', settings)
        subs = {}
        for platform, url in (('instagram', 'https://www.instagram.com/author/'), ('youtube', 'https://www.youtube.com/@author')):
            subs[platform] = api.request('/subscriptions', 'POST', {'platform': platform, 'url': url, 'enabled': False})
            api.request(f"/subscriptions/{subs[platform]['id']}/scan?kind=full", 'POST')
        for sub in subs.values():
            api.wait(lambda: tasks_for(sub), lambda ts: sum(t['content_id'] > 0 and t['status'] == 'running' for t in ts) == 1
                     and sum(t['content_id'] > 0 and t['status'] == 'queued' for t in ts) == 2)
        # 热更新到当前窗口，运行中的媒体没有被取消，新解析和新下载不能开始。
        now = datetime.now(timezone.utc)
        end = now + timedelta(minutes=1)
        if now.second > 45:
            end += timedelta(minutes=1)
        settings['sleep'] = dict(enabled=True, start=(now-timedelta(hours=1)).strftime('%H:%M'), end=end.strftime('%H:%M'), timezone='UTC')
        api.request('/settings', 'PUT', settings)
        # 比较请求计数证明平台无新流量，而不只比较数据库状态。
        baseline = counts()
        (RUN / 'data/release-media').touch()
        for sub in subs.values():
            api.wait(lambda: tasks_for(sub), lambda ts: sum(t['content_id'] > 0 and t['status'] == 'completed' for t in ts) == 1)
        # 让调度器经历多个周期，稳定证明消费者完成后没有继续认领。
        time.sleep(2)
        assert counts() == baseline, (counts(), baseline)
        # 汇总应显示休眠，已完成媒体不能再显示传输中。
        groups = api.request('/task-groups')
        assert all(g.get('sleep_until') and g['counts']['running'] == 0 for g in groups), groups
        print('PASS 两个平台当前媒体完成；新请求停止；原队列与休眠恢复时间保留', flush=True)
        # 休眠期间手动启动只入队，不触发头像或帖子请求。
        other = api.request('/subscriptions', 'POST', {'platform': 'instagram', 'url': 'https://www.instagram.com/waiting/', 'enabled': False})
        waiting = api.request(f"/subscriptions/{other['id']}/scan?kind=full", 'POST')
        assert next(t for t in tasks_for(other) if t['id'] == waiting['id'])['status'] == 'queued'
        time.sleep(1)
        assert counts() == baseline
        # 到点恢复无需保存设置；等待只发生在离线容器，最长约一分钟。
        print('等待真实分钟边界，验证自动恢复：' + settings['sleep']['end'] + ' UTC', flush=True)
        deadline = time.monotonic() + 95
        while time.monotonic() < deadline:
            ts = api.request('/tasks')
            if all(t['status'] == 'completed' for t in ts):
                break
            time.sleep(.2)
        else:
            raise AssertionError(ts)
        assert settings['sleep'] == api.request('/settings')['sleep']
        assert all(t['status'] == 'completed' for t in api.request('/tasks'))
        # 重启保留窗口，新任务在当前已结束的窗口外正常调度。
        subprocess.check_output(['docker', 'restart', NAME])
        api.wait(lambda: api.request('/settings'), lambda s: s['sleep'] == settings['sleep'])
        report = dict(current_media_completed=True, no_new_requests_during_sleep=True,
                      manual_tasks_queued=True, automatic_resume=True, settings_survive_restart=True, debug_limit=10)
        (RUN / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print('PASS 自动恢复与重启持久化；报告：' + str(RUN / 'report.json'), flush=True)
    except Exception:
        # 日志仅来自隔离素材容器，不读取真实凭据。
        print(subprocess.check_output(['docker', 'logs', NAME], text=True))
        raise
    finally:
        subprocess.run(['docker', 'rm', '-f', NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    run()
