package engine

import (
	"context"
	"encoding/json"
	"media-harbor/internal/domain"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 使用真实 gallery-dl 的 HTTP 下载器读取离线大文件，验证进度钩子而非模拟打印。
func TestInstagramHTTPProgress(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil || exec.Command(python, "-c", "import gallery_dl").Run() != nil {
		t.Skip("未安装 gallery-dl")
	}
	fixture := `
import re, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from gallery_dl import extractor
from gallery_dl.extractor.common import Extractor, Message
body = b'GIF89a' + b'x'*(6*1024*1024-6)
class Source(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header('Content-Length', str(len(body)))
        self.send_header('Content-Type', 'image/gif')
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args): pass
server = ThreadingHTTPServer(('127.0.0.1',0),Source)
threading.Thread(target=server.serve_forever,daemon=True).start()
url = 'http://127.0.0.1:%d/image.gif' % server.server_port
class Fixture(Extractor):
    category='instagram'
    subcategory='progress-test'
    directory_fmt=()
    filename_fmt='{post_id}_{num}.{extension}'
    def items(self):
        yield Message.Directory, None, {}
        yield Message.Url, url, {'post_id':'fixture','num':1,'extension':'gif'}
extractor.find=lambda url: Fixture(re.match('.*',url))
sys.argv=['gallery-dl','--config-ignore','-D',sys.argv[1],url]
install_instagram_progress()
import gallery_dl
gallery_dl.main()
`
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var events []domain.TransferProgress
	var output strings.Builder
	_, err = run(ctx, python, []string{"-u", "-c", instagramScript(instagramProgress + fixture), root}, func(line string) {
		output.WriteString(line + "\n")
		if strings.HasPrefix(line, ProgressPrefix) {
			var p domain.TransferProgress
			if e := json.Unmarshal([]byte(strings.TrimPrefix(line, ProgressPrefix)), &p); e != nil {
				t.Error(e)
			}
			events = append(events, p)
		}
	}, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, output.String())
	}
	// 响应头阶段允许零字节；随后必须观察到实际传输字节，且不能超过响应总量。
	var received bool
	for _, event := range events {
		if event.BytesTotal == 6*1024*1024 && event.BytesDone > 0 && event.BytesDone <= event.BytesTotal {
			received = true
		}
	}
	if !received {
		t.Fatalf("%+v", events)
	}
	info, err := os.Stat(filepath.Join(root, "fixture_1.gif"))
	if err != nil || info.Size() != 6*1024*1024 {
		t.Fatalf("file=%v err=%v", info, err)
	}
}
