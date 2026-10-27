package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 同一个声音在两个夹里各一份、外加单拖进来的一份：只列一条，另两份记在它名下；
// 同大小不同声的不并；给后面那份打过星的，留打星的那份。
func TestDedupSameContent(t *testing.T) {
	d := t.TempDir()
	a, b := filepath.Join(d, "E音效"), filepath.Join(d, "H音效")
	writeWav(t, filepath.Join(a, "雨.wav"), 300, 0.4, 44100, 1, 16, 0.5)
	writeWav(t, filepath.Join(b, "雨.wav"), 300, 0.4, 44100, 1, 16, 0.5)
	writeWav(t, filepath.Join(b, "子夹", "雨 拷贝.wav"), 300, 0.4, 44100, 1, 16, 0.5)
	writeWav(t, filepath.Join(b, "风.wav"), 310, 0.4, 44100, 1, 16, 0.5) // 同大小、不同声
	r := &rec{evs: map[string]int{}}
	l := newLib(filepath.Join(d, "data"), "", r.emit)
	os.MkdirAll(l.data, 0o755)
	t.Cleanup(l.bg.Wait)
	l.Add([]string{a, b})
	waitDone(t, l)
	s := l.State()
	if len(s.Items) != 2 {
		t.Fatalf("items %d, want 2（雨 一条、风 一条）", len(s.Items))
	}
	var rain Item
	for _, it := range s.Items {
		if strings.HasSuffix(it.Rel, "雨.wav") {
			rain = it
		}
	}
	if rain.Root != 0 || len(rain.Dup) != 2 {
		t.Fatalf("rain root %d dup %v", rain.Root, rain.Dup)
	}
	// 再拖一次同一批：不多出东西
	l.Add([]string{a, b, filepath.Join(b, "子夹", "雨 拷贝.wav")})
	waitDone(t, l)
	if n := len(l.State().Items); n != 2 {
		t.Fatalf("re-add items %d", n)
	}
	// 给 H 那份打星（模拟以前打过），重扫后留下的就是它
	l.mu.Lock()
	l.meta[filepath.Join(b, "雨.wav")] = &Meta{Star: 3}
	l.sig = 0
	l.mu.Unlock()
	l.Rescan(true)
	for _, it := range l.State().Items {
		if strings.HasSuffix(it.Rel, "雨.wav") && (it.Root != 1 || it.Star != 3) {
			t.Fatalf("starred copy not kept: root %d star %d", it.Root, it.Star)
		}
	}
}
