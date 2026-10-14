"""正式镜像验证任务详情大账本读取；仅构造数据库，不扫描或下载历史媒体。"""
import json
import os
from pathlib import Path
import sqlite3
import statistics
import subprocess
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
RUN = ROOT / '.test-runtime/details' / str(time.time_ns())
NAME = 'media-harbor-details-' + RUN.name
BASE = 'http://127.0.0.1:18884/api'
STAMP = '2026-10-14T12:00:00Z'
KINDS = ('video', 'metadata', 'comments', 'subtitles', 'thumbnail')


def seed():
    # 四种挂载全部隔离，测试不写入用户的数据库、下载目录或设置。
    for folder in ('data', 'downloads', 'archive', 'logs'):
        (RUN / folder).mkdir(parents=True)
    db = sqlite3.connect(RUN / 'data/media.db')
    db.executescript((ROOT / 'internal/storage/schema.sql').read_text())
    settings = json.loads((ROOT / 'internal/config/initial-settings.json').read_text())
    settings['debug']['test_item_limit'] = 10
    db.execute('INSERT INTO app_settings VALUES(1,?)', (json.dumps(settings),))
    for sid in (1, 2):
        sub = dict(id=sid, platform='youtube', identity=f'details-{sid}',
                   url=f'https://www.youtube.com/@details-fixture-{sid}',
                   display_name=f'详情性能样本 {sid}', enabled=False, created_at=STAMP)
        db.execute('INSERT INTO subscriptions VALUES(?,?,?,?)',
                   (sid, 'youtube', sub['identity'], json.dumps(sub)))
    for n in range(1, 1001):
        remote = f'video-{n}'
        post = dict(id=n, subscription_id=1, remote_id=remote, title=remote,
                    status='completed', manifest_complete=True, expected=5, completed=5, updated_at=STAMP)
        content = dict(id=n, subscription_id=1, remote_id=remote, media_type='video', status='completed')
        task = dict(id=n, subscription_id=1, content_id=n, kind='download', status='completed', created_at=STAMP)
        db.execute('INSERT INTO post_tasks VALUES(?,?,?,?)', (n, 1, remote, json.dumps(post)))
        db.execute('INSERT INTO content_items VALUES(?,?,?,?)', (n, 1, remote, json.dumps(content)))
        db.execute('INSERT INTO tasks VALUES(?,?,?,?,?,?)', (n, 1, n, 'download', 'completed', json.dumps(task)))
        for part, kind in enumerate(KINDS):
            rid = (n - 1) * len(KINDS) + part + 1
            child = dict(id=rid, post_id=n, content_id=n, kind=kind, required=True,
                         status='completed', progress=dict(kind=kind, status='completed'), updated_at=STAMP)
            db.execute('INSERT INTO download_items VALUES(?,?,?,?,?)', (rid, n, n, kind, json.dumps(child)))
    # 其他配置的执行历史没有下载消费者；只用于检查详情查询是否扫描全站。
    for n in range(1001, 11001):
        task = dict(id=n, subscription_id=2, kind='scan', status='completed', created_at=STAMP)
        db.execute('INSERT INTO tasks VALUES(?,?,?,?,?,?)', (n, 2, 0, 'scan', 'completed', json.dumps(task)))
    db.commit()
    db.close()


def run():
    seed()
    args = ['docker', 'run', '-d', '--rm', '--name', NAME, '--user', f'{os.getuid()}:{os.getgid()}',
            '-p', '127.0.0.1:18884:8080']
    for folder in ('data', 'downloads', 'archive', 'logs'):
        args += ['-v', f'{RUN / folder}:/{folder}']
    args += ['media-harbor:local']
    subprocess.check_output(args)
    try:
        for _ in range(100):
            try:
                with urllib.request.urlopen(BASE + '/settings', timeout=1) as response:
                    assert json.load(response)['debug']['test_item_limit'] == 10
                break
            except OSError:
                time.sleep(.1)
        else:
            raise RuntimeError('隔离容器未就绪')
        durations, server = [], []
        for _ in range(10):
            start = time.perf_counter()
            with urllib.request.urlopen(BASE + '/subscriptions/1/details', timeout=5) as response:
                data = json.load(response)
                server.append(response.headers['Server-Timing'])
            durations.append((time.perf_counter() - start) * 1000)
            # 只显示十条，但五个子项、帖子任务完成计数与稳定主键必须完整。
            assert [p['key'] for p in data['items']] == [f'video-{n}' for n in range(1, 11)]
            assert all(p['expected'] == p['completed'] == len(p['parts']) == 5 for p in data['items'])
            assert all(part['task_id'] == p['post_id'] for p in data['items'] for part in p['parts'])
        report = dict(posts=1000, execution_records=11000, visible_posts=10, visible_resources=50,
                      median_ms=round(statistics.median(durations), 2), max_ms=round(max(durations), 2), server_timing=server)
        assert max(durations) < 1000, report
        (RUN / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
        print(json.dumps(report, ensure_ascii=False), flush=True)
    finally:
        # 只停止本脚本创建的隔离容器，数据样本和报告留在项目的测试目录。
        subprocess.run(['docker', 'stop', NAME], stdout=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    run()
