# gallery-dl 的递归 DataJob 默认缓存全部结果；此适配器逐条输出并保留提取错误。
import json
import os
import sys
import gallery_dl
from gallery_dl import exception, job

acknowledge = os.environ.get("MEDIA_HARBOR_STREAM_ACK") == "1"


class DiscardList:
    def append(self, value):
        pass


class StreamingJob(job.DataJob):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.data = self.data_urls = self.data_post = self.data_meta = DiscardList()
        self.jsonl = True
        self.out = self.emit
        self.metadata_filter = self.filter
        self.filter = self.media_filter

    def emit(self, message):
        print(json.dumps(message, default=str), flush=True)
        # 先让服务确认当前媒体；遇到缓存或取消时不再请求下一条历史。
        if acknowledge and sys.stdin.readline() != "continue\n":
            raise exception.StopExtraction()

    def media_filter(self, metadata):
        # DataJob 默认移除内部字段；媒体复用必须保留原下载器的请求头和视频清单。
        result = self.metadata_filter(metadata)
        for key in ("_http_headers", "_ytdl_manifest", "_ytdl_manifest_data", "_fallback"):
            if key in metadata:
                result[key] = metadata[key]
        return result

    def run(self):
        result = super().run()
        if self.exception:
            self.emit([-1, {"error": type(self.exception).__name__, "message": str(self.exception)}])
        return result


job.DataJob = StreamingJob
gallery_dl.main()
