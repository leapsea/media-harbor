package engine

import (
	"context"
	"media-harbor/internal/domain"
	"os/exec"
	"testing"
	"time"
)

// TestGalleryRecursiveStream 使用真实 gallery-dl 的递归作业，离线产生媒体与分页错误。
func TestGalleryRecursiveStream(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("未安装 Python")
	}
	if err = exec.Command(python, "-c", "import gallery_dl").Run(); err != nil {
		t.Skip("未安装 gallery-dl")
	}
	fixture := `import re
from gallery_dl import extractor
from gallery_dl.extractor.common import Extractor, Message
class Child(Extractor):
    category = 'instagram'
    subcategory = 'fixture'
    @classmethod
    def from_url(cls, url):
        return cls(re.match('.*',url))
    def items(self):
        yield Message.Directory, None, {}
        yield Message.Url, 'https://cdn.example/1.jpg', {'post_id':'900','num':1,'owner_id':'123','username':'example'}
        raise RuntimeError('fixture pagination error')
class Parent(Child):
    def items(self):
        yield Message.Queue, 'https://example.com/child', {'_extractor':Child}
extractor.find = lambda url: Parent.from_url(url)
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var items []domain.Content
	result, err := scanStream(ctx, python, []string{"-u", "-c", fixture + instagramStream, "--config-ignore", "--resolve-json", "https://example.com/parent"}, domain.Subscription{Platform: "instagram"}, func(string) {}, func(batch domain.ScanResult) error { items = append(items, batch.Items...); return nil })
	if err == nil || len(items) != 1 || items[0].RemoteID != "900-1" || result.StableID != "123" {
		t.Fatalf("result=%+v items=%+v err=%v", result, items, err)
	}
}
