"""正式镜像、SQLite 与 SDK 的 A/B/C 验收；素材最多五条，不访问外部平台。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('incremental', ROOT / 'scripts/incremental-container-smoke.py')
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api = source.fixture
RUN = ROOT / '.test-runtime/posts' / str(time.time_ns())
NAME = 'media-harbor-posts-' + RUN.name
api.BASE = 'http://127.0.0.1:18883/api'

# 替换上游素材，仍使用真实 SDK 的提取作业、Go 控制许可与下载器。
# 与正式 Instagram API/_parse_post 钩子相同：先交付身份，再请求或解析资源。
IG = source.INSTAGRAM.replace("        for code in posts:\n", """        for code in posts:
            from gallery_dl import exception
            if code != 'avatar':
                action = control({'Kind':'post-check','Shortcode':code,'URL':'https://www.instagram.com/p/'+code+'/'})['action']
                if action == 'stop': raise exception.StopExtraction()
                if action == 'skip': continue
""")
IG = IG.replace("data={'post_id':code,", "data={'count':2 if code!='avatar' else 1,'post_id':code,")


def counts():
    return json.loads((RUN / 'data/requests.json').read_text())


def finished(sub):
    def settled(tasks):
        selected = [t for t in tasks if t['subscription_id'] == sub['id']]
        return selected and not any(t['status'] in ('queued', 'running') for t in selected)
    tasks = api.wait(lambda: api.request('/tasks'), settled)
    assert all(t['status'] == 'completed' for t in tasks if t['subscription_id'] == sub['id']), tasks
    return api.request(f"/subscriptions/{sub['id']}/posts")


def scan(sub, kind):
    task = api.request(f"/subscriptions/{sub['id']}/scan?kind={kind}", 'POST')
    api.wait(lambda: api.request('/tasks'), lambda ts: next(t for t in ts if t['id'] == task['id'])['status'] not in ('queued','running'))
    return finished(sub)


def run():
    for name in ('data','downloads','logs','archive','fixture/bin'):
        (RUN / name).mkdir(parents=True)
    (RUN / 'fixture/source.py').write_text(source.SOURCE)
    (RUN / 'data/fixture-items.json').write_text('["cached"]')
    for name, script in (('python3',IG),('gallery-dl',IG),('yt-dlp',source.YOUTUBE)):
        path = RUN / 'fixture/bin' / name
        path.write_text(script); path.chmod(0o755)
    args = ['docker','run','-d','--rm','--name',NAME,'--user',f'{os.getuid()}:{os.getgid()}',
            '-p','127.0.0.1:18883:8080','-e','PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for name in ('data','downloads','logs','archive','fixture'):
        args += ['-v',f'{RUN/name}:/{name}']
    args += ['--entrypoint','/bin/sh',api.IMAGE,'-c',
             'ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4 && ffmpeg -v error -i /fixture/media.mp4 -frames:v 1 -threads 1 -y /fixture/media.png && /opt/engines/bin/python3 /fixture/source.py & exec /app/server']
    subprocess.check_output(args)
    report = {}
    try:
        settings = api.wait(lambda: api.request('/settings'), bool)
        settings['debug']['test_item_limit'] = 10
        settings['ui']['task_log_lines'] = 1000
        for platform in ('instagram','youtube'):
            settings['pipeline'][platform].update(min_interval_seconds=.02,max_requests=1000)
        settings['youtube'].update(video=True,format='best',metadata=True,comments=False,
                                  subtitles=False,auto_subtitles=False,thumbnail=False,extra_audio=False,extract_audio=False,fragments=1)
        api.request('/settings','PUT',settings)
        api.request('/cookies/instagram','PUT',{'content':'sessionid=offline-fixture'})
        ig = api.request('/subscriptions','POST',{'platform':'instagram','url':'https://www.instagram.com/author/','display_name':'A-B-C Instagram','enabled':False})
        posts = scan(ig,'full')
        assert len(posts)==1 and posts[0]['status']=='completed' and posts[0]['expected']==posts[0]['completed']==2, posts
        children = api.request(f"/posts/{posts[0]['id']}/downloads")
        assert len(children)==2 and all(c['status']=='completed' for c in children), children
        assert all(c['progress']['bytes_total'] > 0 and c['progress']['bytes_done'] == c['progress']['bytes_total'] for c in children), children
        before = counts()
        scan(ig,'full')
        assert counts().get('/post/instagram/cached',0)==before.get('/post/instagram/cached',0), counts()
        (RUN/'data/fixture-items.json').write_text('["new","cached","older"]')
        before = counts()
        scan(ig,'scan')
        after = counts()
        assert after.get('/post/instagram/new',0)-before.get('/post/instagram/new',0)==1, after
        assert after.get('/post/instagram/cached',0)==before.get('/post/instagram/cached',0), after
        assert '/post/instagram/older' not in after, after
        report['instagram']={'post_expected_resources':2,'completed_post_detail_requests':0,'incremental_boundary_detail_requests':0}
        print('PASS Instagram：一个B对应两个C；完成帖子不解析；增量在完成B之前停止',flush=True)
        # 单视频创建立即执行，整个扫描/准备/下载只能请求一次视频详情。
        before = counts()
        yt = api.request('/subscriptions','POST',{'platform':'youtube','url':'https://www.youtube.com/watch?v=single','display_name':'A-B-C YouTube','enabled':False})
        posts = finished(yt)
        assert len(posts)==1 and posts[0]['status']=='completed' and posts[0]['expected']==2, posts
        after = counts()
        assert after.get('/post/youtube/single',0)-before.get('/post/youtube/single',0)==1, after
        children = api.request(f"/posts/{posts[0]['id']}/downloads")
        assert {c['kind'] for c in children}=={'video','metadata'} and all(c['status']=='completed' for c in children), children
        video = next(c for c in children if c['kind']=='video')
        assert video['progress']['bytes_total'] > 0 and video['progress']['bytes_done'] == video['progress']['bytes_total'], children
        before = counts()
        scan(yt,'full')
        assert counts().get('/post/youtube/single',0)==before.get('/post/youtube/single',0), counts()
        # GUI/CLI 同用的配置保存 API 修改 A；下一次执行应从数据库取得新期望集合。
        yt['youtube'] = dict(settings['youtube'],thumbnail=True)
        api.request(f"/subscriptions/{yt['id']}",'PUT',yt)
        posts = scan(yt,'full')
        assert posts[0]['expected']==3 and posts[0]['status']=='completed', posts
        children = api.request(f"/posts/{posts[0]['id']}/downloads")
        thumb = next(c for c in children if c['kind']=='thumbnail')
        assert thumb['status']=='completed' and thumb['outcome']=='unavailable', children
        report['youtube']={'single_video_parse_count':1,'completed_video_parse_count':0,'persisted_kinds':['video','metadata','thumbnail'],'configuration_reload':True,'missing_optional_outcome':'unavailable'}
        # 服务重启保留 B/C 主键、完成状态和资源数量。
        ids = [(p['id'],p['remote_id']) for p in posts]
        subprocess.check_output(['docker','restart',NAME])
        restored = api.wait(lambda:api.request(f"/subscriptions/{yt['id']}/posts"),lambda ps:all(p['status']=='completed' for p in ps))
        assert ids==[(p['id'],p['remote_id']) for p in restored], restored
        report['restart_preserves_identities']=True
        # 新数据目录直接初始化当前表集合，重启不引入迁移版本记录。
        tables = json.loads(subprocess.check_output(['docker','exec',NAME,'/opt/engines/bin/python3','-c',
            "import sqlite3,json; db=sqlite3.connect('file:/data/media.db?mode=ro',uri=True); print(json.dumps([r[0] for r in db.execute(\"SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name\")]))"],text=True))
        assert tables == ['app_settings','content_items','download_history_cleared','download_items','engine_versions','platform_runs','post_tasks','subscriptions','tasks','youtube_paid_blocks'], tables
        report['current_schema_initialization'] = tables

        (RUN/'report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
        print('PASS YouTube：单视频一次解析；完成B跳过；更新A加载新配置；重启保持B/C；报告：'+str(RUN/'report.json'),flush=True)
        if os.environ.get('SMOKE_UI_HOLD')=='1':
            print('UI_HOLD '+str(RUN),flush=True)
            deadline=time.monotonic()+60
            while time.monotonic()<deadline and not (RUN/'continue-ui').exists():time.sleep(.2)
    except Exception:
        print(subprocess.check_output(['docker','logs',NAME],text=True))
        raise
    finally:
        subprocess.run(['docker','rm','-f',NAME],stdout=subprocess.DEVNULL,check=False)

if __name__=='__main__':run()
