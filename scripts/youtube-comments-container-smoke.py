"""正式镜像、真实 yt-dlp 与本地分页素材：验证评论上限、继承和阶段超时。"""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('source', ROOT / 'scripts/incremental-container-smoke.py')
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
api = source.fixture
api.BASE = 'http://127.0.0.1:18886/api'
RUN = ROOT / '.test-runtime/youtube-comments' / str(time.time_ns())
NAME = 'media-harbor-comments-' + RUN.name

HTTP = source.SOURCE.replace('import json,threading', 'import json,threading,time')
HTTP = HTTP.replace("        if self.path.startswith('/profile/'):", """        if self.path.startswith('/comments/slow/'):
            time.sleep(4)
        if self.path.startswith('/profile/'):""")
HTTP = HTTP.replace("self.path.startswith('/visit/')", "self.path.startswith('/visit/') or self.path.startswith('/comments/')")
HTTP = HTTP.replace("elif self.path.endswith('.png'):", "elif self.path.endswith('.vtt'):\n            body=b'WEBVTT\\n\\n00:00.000 --> 00:00.200\\nfixture\\n'\n        elif self.path.endswith('.png'):")

# 复用 YoutubeIE 自身的 _get_comments/islice 上限逻辑，仅替换上游分页内容。
# 每十条触发一次真实 HTTP，请求计数能证明达到上限后没有继续枚举。
SDK = source.YOUTUBE_SDK.replace('import yt_dlp', '''import yt_dlp,types
from yt_dlp.extractor.youtube import YoutubeIE
def fixture_comments(self, renderer, cfg, video_id, **kwargs):
    assert self._configuration_arg('comment_sort') == ['new'],self._configuration_arg('comment_sort')
    assert self._configuration_arg('lang',casesense=True) == ['zh-CN'],self._configuration_arg('lang',casesense=True)
    for i in range(1000):
        if i % 10 == 0:
            self._download_json('http://127.0.0.1:8001/comments/'+video_id+'/'+str(i//10),video_id)
        yield {'id':str(i),'text':'comment '+str(i),'timestamp':1792000000-i}
''')
SDK = SDK.replace("        return {'id':code", """        comment_ie=YoutubeIE(self._downloader)
        comment_ie._comment_entries=types.MethodType(fixture_comments,comment_ie)
        return {'__post_extractor':comment_ie.extract_comments({},code,[],''), 'id':code""")
SDK = SDK.replace("'upload_date':'20261014','availability':'public',", """'upload_date':'20261014','availability':'public',
                'subtitles':{'en':[{'url':'http://127.0.0.1:8001/fixture.vtt','ext':'vtt'}]},""")
SDK = SDK.replace("'channel_id':'author'", "'channel_id':code")
SDK = SDK.replace("self.playlist_result(entries(),'author','author',channel='author',channel_id='author')",
                  "self.playlist_result(entries(),self._match_id(url),self._match_id(url),channel=self._match_id(url),channel_id=self._match_id(url))")
WRAPPER = '#!/opt/engines/bin/python3\n' + SDK + """
if sys.argv[1:3]==['-u','-c']:
    code=sys.argv[3];sys.argv=['yt-dlp']+sys.argv[4:];exec(code)
else:
    yt_dlp.main(sys.argv[1:])
"""


def run():
    for name in ('data', 'downloads', 'archive', 'logs', 'fixture/bin'):
        (RUN/name).mkdir(parents=True)
    (RUN/'fixture/source.py').write_text(HTTP)
    for name in ('python3', 'yt-dlp'):
        path = RUN/'fixture/bin'/name
        path.write_text(WRAPPER)
        path.chmod(0o755)
    args = ['docker','run','-d','--rm','--name',NAME,'--user',f'{os.getuid()}:{os.getgid()}',
            '-p','127.0.0.1:18886:8080','-e','PATH=/fixture/bin:/opt/engines/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin']
    for name in ('data','downloads','archive','logs','fixture'):
        args += ['-v',f'{RUN/name}:/{name}']
    startup = '''set -e
ffmpeg -v error -f lavfi -i color=s=32x32:d=0.2 -f lavfi -i anullsrc -shortest -c:v libx264 -c:a aac -y /fixture/media.mp4
/opt/engines/bin/python3 /fixture/source.py &
exec /app/server
'''
    subprocess.check_output(args+['--entrypoint','/bin/sh',api.IMAGE,'-c',startup])
    report = {}
    try:
        settings = api.wait(lambda:api.request('/settings'),bool)
        assert settings['youtube']['comment_limit'] == 100 and settings['youtube']['comment_timeout_seconds'] == 180,settings
        settings['debug']['test_item_limit'] = 10
        settings['pipeline']['youtube'].update(min_interval_seconds=.1,max_requests=1000)
        settings['ui']['task_log_lines'] = 1000
        settings['youtube'].update(video=True,format='b',metadata=True,comments=True,subtitles=True,subtitle_languages='en',
                                   auto_subtitles=False,thumbnail=False,extra_audio=True,extract_audio=False)
        api.request('/settings','PUT',settings)
        for code,limit in (('default',100),('custom',35),('inherited',23),('slow',None),('after',23)):
            if code == 'inherited':
                settings['youtube'].update(comment_limit=23,comment_timeout_seconds=10)
                api.request('/settings','PUT',settings)
            (RUN/'data/fixture-items.json').write_text(json.dumps([code]))
            options = dict(settings['youtube'])
            # 模拟旧订阅保存了完整下载覆盖，但不存在新增评论字段。
            options.pop('comment_limit');options.pop('comment_timeout_seconds')
            if code == 'custom':options.update(comment_limit=35,comment_timeout_seconds=10)
            if code == 'slow':options.update(comment_limit=100,comment_timeout_seconds=1)
            sub = api.request('/subscriptions','POST',{'platform':'youtube','url':'https://www.youtube.com/@'+code,
                              'display_name':code,'enabled':False,'youtube':options})
            started=time.monotonic()
            api.request(f"/subscriptions/{sub['id']}/scan?kind=full",'POST')
            tasks=api.wait(lambda:[t for t in api.request('/tasks') if t['subscription_id']==sub['id']],
                           lambda ts:any(t['content_id'] for t in ts) and all(t['status'] not in ('queued','running') for t in ts))
            task=next(t for t in tasks if t['content_id'])
            logs=api.request(f"/subscriptions/{sub['id']}/log")["log"]
            (RUN/(code+'.log')).write_text(logs)
            log_text=logs
            assert '[评论进度]' in log_text and '媒体 '+code in log_text,logs
            content=next(c for c in api.request('/contents') if c['subscription_id']==sub['id'])
            assert any(p.endswith('.en.vtt') for p in content['paths']),content
            assert any('.audio.' in p for p in content['paths']),content
            counts=json.loads((RUN/'data/requests.json').read_text())
            pages=sum(v for k,v in counts.items() if k.startswith('/comments/'+code+'/'))
            if limit is None:
                assert task['status']=='failed' and '评论抓取超时' in task['error'],task
                assert time.monotonic()-started<10,task
                report[code]={'timeout':True,'attachments_completed':True,'seconds':round(time.monotonic()-started,2)}
            else:
                assert task['status']=='completed',task
                metadata=next(p for p in content['paths'] if p.endswith('.info.json'))
                saved=json.loads((RUN/metadata.lstrip('/')).read_text())
                assert len(saved['comments'])==limit,saved
                assert pages==(limit+9)//10,counts
                assert [c['id'] for c in saved['comments']]==list(map(str,range(limit))),saved
                assert f'已采集 {limit}/{limit} 条' in log_text and f'已采集评论 {limit}/{limit} 条' in log_text,logs
                report[code]={'comments':limit,'pages':pages,'attachments_completed':True}
            print('PASS '+code+': '+json.dumps(report[code],ensure_ascii=False),flush=True)
        # 恢复产品默认值，便于界面验证数量、超时的回显和保存。
        settings['youtube'].update(comment_limit=100,comment_timeout_seconds=180)
        api.request('/settings','PUT',settings)
        (RUN/'report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
        print('容器验收结果：'+str(RUN/'report.json'),flush=True)
        if os.environ.get('SMOKE_UI_HOLD')=='1':
            print('UI_HOLD '+str(RUN),flush=True)
            deadline=time.monotonic()+180
            while time.monotonic()<deadline and not (RUN/'continue-ui').exists():time.sleep(.2)
    finally:
        subprocess.run(['docker','rm','-f',NAME],stdout=subprocess.DEVNULL,check=False)


if __name__=='__main__':run()
