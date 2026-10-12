# gallery-dl 的递归 DataJob 默认缓存全部结果；此适配器逐条输出并保留提取错误。
import json
import sys
import gallery_dl
from gallery_dl import job


class DiscardList:
    def append(self, value):
        pass


class StreamingJob(job.DataJob):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.data = self.data_urls = self.data_post = self.data_meta = DiscardList()
        self.jsonl = True
        self.out = self.emit

    def emit(self, message):
        print(json.dumps(message, default=str), flush=True)

    def run(self):
        result = super().run()
        if self.exception:
            self.emit([-1, {"error": type(self.exception).__name__, "message": str(self.exception)}])
        return result


job.DataJob = StreamingJob
gallery_dl.main()
