package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeWav 写一段正弦（hz=0 时写白噪），bits 为 16／24／32(float)。
func writeWav(t *testing.T, p string, hz float64, sec float64, sr, ch, bits int, amp float64) {
	t.Helper()
	n := int(sec * float64(sr))
	bps := bits / 8
	data := make([]byte, n*ch*bps)
	seed := uint32(1)
	for i := 0; i < n; i++ {
		var v float64
		if hz > 0 {
			v = amp * math.Sin(2*math.Pi*hz*float64(i)/float64(sr))
		} else {
			seed = seed*1664525 + 1013904223
			v = amp * (float64(seed)/float64(math.MaxUint32)*2 - 1)
		}
		for c := 0; c < ch; c++ {
			o := (i*ch + c) * bps
			switch bits {
			case 16:
				binary.LittleEndian.PutUint16(data[o:], uint16(int16(v*32767)))
			case 24:
				x := int32(v * 8388607)
				data[o], data[o+1], data[o+2] = byte(x), byte(x>>8), byte(x>>16)
			case 32:
				binary.LittleEndian.PutUint32(data[o:], math.Float32bits(float32(v)))
			}
		}
	}
	tag := uint16(1)
	if bits == 32 {
		tag = 3
	}
	h := make([]byte, 0, 44)
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(36+len(data)+12))
	h = append(h, "WAVE"...)
	h = append(h, "LIST"...) // 一个无关的块，验跳块
	h = binary.LittleEndian.AppendUint32(h, 4)
	h = append(h, "INFO"...)
	h = append(h, "fmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, tag)
	h = binary.LittleEndian.AppendUint16(h, uint16(ch))
	h = binary.LittleEndian.AppendUint32(h, uint32(sr))
	h = binary.LittleEndian.AppendUint32(h, uint32(sr*ch*bps))
	h = binary.LittleEndian.AppendUint16(h, uint16(ch*bps))
	h = binary.LittleEndian.AppendUint16(h, uint16(bits))
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(len(data)))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, append(h, data...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ffOrSkip(t *testing.T) string {
	wd, _ := os.Getwd()
	ff := findFF(filepath.Join(wd, "resources"))
	if ff == "" {
		t.Skip("no ffmpeg for " + runtime.GOOS + "-" + runtime.GOARCH)
	}
	return ff
}

type rec struct {
	mu  sync.Mutex
	evs map[string]int
}

func (r *rec) emit(n string, v any) {
	r.mu.Lock()
	r.evs[n]++
	r.mu.Unlock()
}

func waitDone(t *testing.T, l *Lib) {
	t.Helper()
	for i := 0; i < 400; i++ {
		l.mu.RLock()
		all := true
		for _, it := range l.items {
			if !it.Done {
				all = false
			}
		}
		l.mu.RUnlock()
		if all {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("analysis never finished")
}

func TestWavFormatsAndFeatures(t *testing.T) {
	d := t.TempDir()
	for _, c := range []struct {
		bits, ch, sr int
	}{{16, 1, 44100}, {24, 2, 48000}, {32, 2, 96000}} {
		p := filepath.Join(d, "s"+strconv.Itoa(c.bits)+".wav")
		writeWav(t, p, 1000, 1.5, c.sr, c.ch, c.bits, 0.5)
		f, err := probeNative(p, "wav")
		if err != nil {
			t.Fatal(c, err)
		}
		if f.SR != c.sr || f.Ch != c.ch || f.Bits != c.bits || math.Abs(f.dur()-1.5) > 0.001 {
			t.Fatalf("%v: got %+v dur %v", c, f, f.dur())
		}
		x, err := readNative(p, f, 0)
		if err != nil {
			t.Fatal(err)
		}
		ft := features(x, f.SR)
		if math.Abs(float64(ft.Peak)-(-6.02)) > 0.1 {
			t.Errorf("%v peak %v", c, ft.Peak)
		}
		if math.Abs(float64(ft.RMS)-(-9.03)) > 0.1 {
			t.Errorf("%v rms %v", c, ft.RMS)
		}
		if math.Abs(float64(ft.Cen)-1000) > 120 {
			t.Errorf("%v centroid %v want ~1000", c, ft.Cen)
		}
	}
	// 低沉与明亮分得开
	writeWav(t, filepath.Join(d, "lo.wav"), 120, 1, 44100, 1, 16, 0.5)
	writeWav(t, filepath.Join(d, "hi.wav"), 0, 1, 44100, 1, 16, 0.5)
	cen := func(n string) float32 {
		f, _ := probeNative(filepath.Join(d, n), "wav")
		x, _ := readNative(filepath.Join(d, n), f, 0)
		return features(x, f.SR).Cen
	}
	if c := cen("lo.wav"); c > 500 {
		t.Errorf("120 Hz sine centroid %v", c)
	}
	if c := cen("hi.wav"); c < 3500 {
		t.Errorf("white noise centroid %v", c)
	}
}

func TestDetail(t *testing.T) {
	x := make([]float32, 44100)
	for i := range x {
		x[i] = float32(0.8 * math.Sin(2*math.Pi*2000*float64(i)/44100))
	}
	d := detail(x, 44100, 1)
	if len(d.Wave) != waveW || len(d.Spec) != specCols*specRows {
		t.Fatal(len(d.Wave), len(d.Spec))
	}
	if d.Wave[waveW/2] < 250 {
		t.Errorf("wave peak %d", d.Wave[waveW/2])
	}
	// 2 kHz 那一行最亮
	col := d.Spec[(specCols/2)*specRows : (specCols/2+1)*specRows]
	best := 0
	for r := range col {
		if col[r] > col[best] {
			best = r
		}
	}
	f := d.Lo * math.Pow(d.Hi/d.Lo, (float64(best)+0.5)/specRows)
	if f < 1700 || f > 2400 {
		t.Errorf("brightest row at %v Hz", f)
	}
}

func TestClassify(t *testing.T) {
	name := func(is []int) string {
		var s []string
		for _, i := range is {
			s = append(s, cats[i].Name)
		}
		return strings.Join(s, ",")
	}
	for in, want := range map[string]string{
		"SFX/Doors/DoorSlam_Wood_03.wav":  "门",
		"Footsteps Gravel 01.wav":         "脚步",
		"环境/夜晚 森林 虫鸣.wav":                 "环境",
		"WHOOSH_fast.wav":                 "呼啸",
		"glassBreak.wav":                  "玻璃",
		"metallicClank.wav":               "金属",
		"ドア 開く.wav":                       "门",
		"Explosions/big_explosion_02.wav": "爆炸",
	} {
		got := name(classify(in))
		if !strings.Contains(got, want) {
			t.Errorf("%s → %q, want %s", in, got, want)
		}
	}
	if got := classify("声00001.wav"); len(got) != 0 {
		t.Errorf("plain name got %v", name(got))
	}
	if got := name(classify("cartoon.wav")); strings.Contains(got, "交通") {
		t.Errorf("cartoon should not be a car: %s", got)
	}
}

func TestLibScanRateRemove(t *testing.T) {
	d := t.TempDir()
	lib := filepath.Join(d, "库")
	writeWav(t, filepath.Join(lib, "门", "door_close.wav"), 300, 0.4, 44100, 1, 16, 0.5)
	writeWav(t, filepath.Join(lib, "门", "door_open.wav"), 320, 0.4, 44100, 1, 16, 0.5) // 与 door_close 不同声，不然按去重会并成一条
	writeWav(t, filepath.Join(lib, "曲", "theme.wav"), 220, 31, 22050, 1, 16, 0.3)
	os.WriteFile(filepath.Join(lib, "notes.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(lib, "._door.wav"), []byte("junk"), 0o644)
	loose := filepath.Join(d, "别处", "one.wav")
	writeWav(t, loose, 440, 0.2, 44100, 1, 16, 0.5)
	r := &rec{evs: map[string]int{}}
	l := newLib(filepath.Join(d, "data"), "", r.emit)
	os.MkdirAll(l.data, 0o755)
	if n := l.Add([]string{lib, loose, filepath.Join(lib, "门")}); n != 2 {
		t.Fatalf("roots added %d", n) // 门 已在 库 里，不重收
	}
	waitDone(t, l)
	s := l.State()
	if len(s.Items) != 4 || len(s.Roots) != 2 || !s.Roots[1].File {
		t.Fatalf("items %d roots %+v", len(s.Items), s.Roots)
	}
	var door, theme Item
	for _, it := range s.Items {
		switch {
		case strings.HasSuffix(it.Rel, "door_close.wav"):
			door = it
		case strings.HasSuffix(it.Rel, "theme.wav"):
			theme = it
		}
	}
	if math.Abs(theme.Dur-31) > 0.01 || door.Dur > 0.5 || !door.Done {
		t.Fatalf("durs %v %v", theme.Dur, door.Dur)
	}
	if !strings.Contains(strings.Join(func() []string {
		var o []string
		for _, i := range door.Cats {
			o = append(o, cats[i].Name)
		}
		return o
	}(), ","), "门") {
		t.Errorf("door cats %v", door.Cats)
	}
	l.Rate([]int{door.ID}, 4)
	l.SetNote(door.ID, "  重的木门  ")
	// 重开：星、批注、算好的特征都在；不重算
	l2 := newLib(l.data, "", r.emit)
	l2.scan()
	s2 := l2.State()
	for _, it := range s2.Items {
		if strings.HasSuffix(it.Rel, "door_close.wav") {
			if it.Star != 4 || it.Note != "重的木门" || !it.Done {
				t.Fatalf("after reopen %+v", it)
			}
		}
	}
	// 新根包住旧根：旧根换掉
	if n := l2.Add([]string{d}); n != 1 {
		t.Fatal("parent root not added")
	}
	if s := l2.State(); len(s.Roots) != 1 || len(s.Items) != 4 {
		t.Fatalf("after parent add roots %d items %d", len(s.Roots), len(s.Items))
	}
	l2.Remove(d)
	if s := l2.State(); len(s.Items) != 0 {
		t.Fatal("remove left items")
	}
	if _, err := os.Stat(filepath.Join(lib, "门", "door_close.wav")); err != nil {
		t.Fatal("remove touched files")
	}
}

func TestSndServesOnlyLibrary(t *testing.T) {
	d := t.TempDir()
	writeWav(t, filepath.Join(d, "a", "x.wav"), 440, 0.1, 44100, 1, 16, 0.5)
	os.WriteFile(filepath.Join(d, "secret.txt"), []byte("no"), 0o644)
	l := newLib(t.TempDir(), "", func(string, any) {})
	l.Add([]string{filepath.Join(d, "a")})
	id := l.State().Items[0].ID
	for path, code := range map[string]int{
		"/f/" + strconv.Itoa(id): 200,
		"/f/999":                 404,
		"/f/../../secret.txt":    404,
		"/x/" + strconv.Itoa(id): 404,
		"/t/" + strconv.Itoa(id): 404, // 没 ffmpeg 时不转
	} {
		rw := httptest.NewRecorder()
		l.serveSnd(rw, httptest.NewRequest("GET", "http://snd.localhost"+path, nil))
		if rw.Code != code {
			t.Errorf("%s → %d want %d", path, rw.Code, code)
		}
	}
	rw := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/f/"+strconv.Itoa(id), nil)
	req.Header.Set("Range", "bytes=0-3")
	l.serveSnd(rw, req)
	if rw.Code != http.StatusPartialContent || rw.Body.String() != "RIFF" {
		t.Errorf("range %d %q", rw.Code, rw.Body.String())
	}
}

func TestConvertAllFormats(t *testing.T) {
	ff := ffOrSkip(t)
	d := t.TempDir()
	src := filepath.Join(d, "库", "hit.wav")
	writeWav(t, src, 880, 0.5, 44100, 2, 32, 0.5) // float 源：flac／alac 要自动转位深
	l := newLib(filepath.Join(d, "data"), ff, func(string, any) {})
	os.MkdirAll(l.data, 0o755)
	l.Add([]string{filepath.Join(d, "库")})
	id := l.State().Items[0].ID
	want := map[string]string{"wav16": "hit.wav", "wav24": "hit 2.wav", "wav48": "hit 3.wav", "aiff": "hit.aiff", "flac": "hit.flac", "alac": "hit.m4a", "aac": "hit 2.m4a"}
	for _, f := range formats {
		r, err := l.Convert([]int{id}, f.ID)
		if err != nil || r.Done != 1 || len(r.Failed) != 0 {
			t.Fatalf("%s: %v %+v", f.ID, err, r)
		}
		if filepath.Dir(r.Out[0]) != l.outDir() || filepath.Base(r.Out[0]) != want[f.ID] {
			t.Errorf("%s → %s want %s", f.ID, filepath.Base(r.Out[0]), want[f.ID])
		}
		// 产物能被同一个 ffmpeg 读回，长度对
		x, info, err := ffRead(ff, r.Out[0], 22050, 0)
		if err != nil || len(x) < 10000 || len(x) > 12000 {
			t.Errorf("%s readback: %v len %d", f.ID, err, len(x))
		}
		if f.ID == "wav48" && info.SR != 48000 {
			t.Errorf("wav48 sr %d", info.SR)
		}
		if f.ID == "aiff" { // Go 自己的 AIFF 读头也对得上
			pf, err := probeNative(r.Out[0], "aiff")
			if err != nil || pf.Bits != 24 || pf.SR != 44100 || pf.Ch != 2 || math.Abs(pf.dur()-0.5) > 0.001 {
				t.Errorf("aiff probe %+v %v", pf, err)
			}
			if x, err := readNative(r.Out[0], pf, 0); err != nil || math.Abs(float64(features(x, pf.SR).Peak)+6.02) > 0.2 {
				t.Errorf("aiff read %v", err)
			}
		}
		if f.ID == "wav24" {
			pf, _ := probeNative(r.Out[0], "wav")
			if pf.Bits != 24 {
				t.Errorf("wav24 bits %d", pf.Bits)
			}
		}
	}
	// 原文件没被动
	if f, _ := probeNative(src, "wav"); f.Bits != 32 || !f.Float {
		t.Fatal("source changed")
	}
	// 产物进了库
	if n := len(l.State().Items); n != 1+len(formats) {
		t.Errorf("library has %d items", n)
	}
	// 坏文件：报失败、不留占位
	bad := filepath.Join(d, "库", "bad.wav")
	os.WriteFile(bad, []byte("RIFFxxxxWAVEjunk"), 0o644)
	l.scan()
	var bid int
	for _, it := range l.State().Items {
		if it.Rel == "bad.wav" {
			bid = it.ID
		}
	}
	r, _ := l.Convert([]int{bid}, "flac")
	if r.Done != 0 || len(r.Failed) != 1 {
		t.Errorf("bad convert %+v", r)
	}
	if _, err := os.Stat(filepath.Join(l.outDir(), "bad.flac")); !os.IsNotExist(err) {
		t.Error("placeholder left behind")
	}
}

func TestFFAnalysisOfCompressed(t *testing.T) {
	ff := ffOrSkip(t)
	d := t.TempDir()
	src := filepath.Join(d, "库", "tone.wav")
	writeWav(t, src, 1000, 2, 44100, 1, 16, 0.5)
	l := newLib(filepath.Join(d, "data"), ff, func(string, any) {})
	os.MkdirAll(l.data, 0o755)
	l.Add([]string{filepath.Join(d, "库")})
	id := l.State().Items[0].ID
	l.Convert([]int{id}, "flac")
	l.Convert([]int{id}, "aac")
	waitDone(t, l)
	for _, it := range l.State().Items {
		if it.Ext == "wav" {
			continue
		}
		if math.Abs(it.Dur-2) > 0.1 || it.SR != 44100 || it.Ch != 1 || math.Abs(float64(it.Cen)-1000) > 150 {
			t.Errorf("%s: %+v", it.Rel, it)
		}
		dt, err := l.Detail(it.ID)
		if err != nil || dt.Wave[waveW/2] < 200 {
			t.Errorf("%s detail %v", it.Rel, err)
		}
	}
}

func TestMenu(t *testing.T) {
	d := t.TempDir()
	writeWav(t, filepath.Join(d, "a", "x.wav"), 440, 0.1, 44100, 1, 16, 0.5)
	l := newLib(t.TempDir(), "/nonexistent/ffmpeg", func(string, any) {})
	l.Add([]string{filepath.Join(d, "a")})
	id := l.State().Items[0].ID
	m := l.menu([]int{id}, func(string) {})
	var labels []string
	for _, it := range m {
		labels = append(labels, it.Label)
	}
	got := strings.Join(labels, "|")
	if !strings.HasPrefix(got, "打分|转成||") || !strings.Contains(got, "拷贝文件|拷贝路径") {
		t.Fatalf("menu %q", got)
	}
	if len(m[0].Submenu) != 7 || len(m[1].Submenu) != len(formats) {
		t.Fatalf("submenus %d %d", len(m[0].Submenu), len(m[1].Submenu))
	}
	m[0].Submenu[1].Click(nil, nil) // ★★★★☆
	time.Sleep(50 * time.Millisecond)
	if st := l.State().Items[0].Star; st != 4 {
		t.Fatalf("star %d", st)
	}
	if mm := l.menu([]int{id, id}, func(string) {}); mm[1].Label != "这 2 个转成" {
		t.Errorf("multi label %q", mm[1].Label)
	}
	l.ff = ""
	if mm := l.menu([]int{id}, func(string) {}); mm[1].Label == "转成" {
		t.Error("convert shown without ffmpeg")
	}
}

func TestOutPathNeverOverwrites(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "a.wav")
	os.WriteFile(src, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(d, "a.flac"), []byte("keep"), 0o644)
	p, _ := outPath(d, "a", "flac")
	if filepath.Base(p) != "a 2.flac" {
		t.Fatal(p)
	}
	if b, _ := os.ReadFile(filepath.Join(d, "a.flac")); string(b) != "keep" {
		t.Fatal("overwrote")
	}
	p, _ = outPath(d, "a", "wav")
	if filepath.Base(p) != "a 2.wav" {
		t.Fatal(p)
	}
}

func TestNatLess(t *testing.T) {
	if !natLess("声2.wav", "声10.wav") || natLess("b", "A") || !natLess("a/1", "a/01x") {
		t.Fatal("natLess")
	}
}

func TestClipFadesAndLandsInOut(t *testing.T) {
	ff := ffOrSkip(t)
	d := t.TempDir()
	src := filepath.Join(d, "库", "长 声.wav")
	writeWav(t, src, 0, 3, 48000, 2, 16, 0.8) // 噪声：首尾样本本来不是 0
	l := newLib(filepath.Join(d, "data"), ff, func(string, any) {})
	t.Cleanup(l.bg.Wait)
	l.Add([]string{filepath.Join(d, "库")})
	id := l.State().Items[0].ID
	p, err := l.Clip(id, 1, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != filepath.Join(l.outDir(), "片段") || filepath.Base(p) != "长 声 0m01.00s-0m01.50s.wav" {
		t.Fatal(p)
	}
	pf, err := probeNative(p, "wav")
	if err != nil || pf.Bits != 24 || pf.SR != 48000 || pf.Ch != 2 || math.Abs(pf.dur()-0.5) > 0.002 {
		t.Fatalf("%+v %v", pf, err)
	}
	x, err := readNative(p, pf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if x[0] != 0 || math.Abs(float64(x[len(x)-1])) > 0.01 {
		t.Errorf("edges not faded: %v %v", x[0], x[len(x)-1])
	}
	mid := float32(0)
	for _, v := range x[len(x)/2-200 : len(x)/2+200] {
		mid = max(mid, v, -v)
	}
	if mid < 0.5 {
		t.Errorf("middle got faded too: %v", mid)
	}
	if p2, _ := l.Clip(id, 1, 1.5); filepath.Base(p2) != "长 声 0m01.00s-0m01.50s 2.wav" {
		t.Errorf("second clip %s", p2)
	}
	if _, err := l.Clip(id, 1, 1.005); err == nil {
		t.Error("too short accepted")
	}
	l.bg.Wait()
	l.scan()
	found := false
	for _, it := range l.State().Items {
		if strings.HasPrefix(it.Rel, "片段/") {
			found = true
		}
	}
	if !found {
		t.Error("clip not in library")
	}
}

func TestOutDirConfig(t *testing.T) {
	d := t.TempDir()
	l := newLib(d, "", func(string, any) {})
	if l.outDir() != filepath.Join(d, "导出") {
		t.Fatal(l.outDir())
	}
	l.SetOut(filepath.Join(d, "别处"))
	l2 := newLib(d, "", func(string, any) {})
	if l2.outDir() != filepath.Join(d, "别处") || l2.State().Out != l2.outDir() {
		t.Fatal(l2.outDir())
	}
	home, _ := os.UserHomeDir()
	if (&Lib{}).outDir() != filepath.Join(home, "Music", "FREEEFX") {
		t.Fatal("default")
	}
}

// 造一个带 bext、iXML、LIST/INFO 的 WAV，看读出来的说明与据此认的类目
func TestWavMeta(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "x.wav")
	writeWav(t, src, 440, 0.2, 44100, 1, 16, 0.5)
	b, _ := os.ReadFile(src)
	chunk := func(id string, body []byte) []byte {
		h := make([]byte, 8)
		copy(h, id)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(body)))
		out := append(h, body...)
		if len(body)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}
	bext := make([]byte, 602)
	copy(bext, "Heavy door slam, wooden")
	ixml := []byte("<BWFXML><USER><CATID>GLASBrk</CATID><FXNAME>Bottle Smash</FXNAME><KEYWORDS>shatter</KEYWORDS></USER></BWFXML>")
	info := append([]byte("INFO"), chunk("ICMT", []byte("雷声远\x00"))...)
	extra := append(append(chunk("bext", bext), chunk("iXML", ixml)...), chunk("LIST", info)...)
	nb := append(append(append([]byte{}, b[:12]...), extra...), b[12:]...)
	binary.LittleEndian.PutUint32(nb[4:], uint32(len(nb)-8))
	os.WriteFile(src, nb, 0o644)
	got := wavMeta(src)
	for _, w := range []string{"Heavy door slam, wooden", "GLASBrk", "Bottle Smash", "shatter", "雷声远"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	if strings.ContainsRune(got, 0) {
		t.Error("NUL leaked")
	}
	names := func(ks []int) string {
		var s []string
		for _, k := range ks {
			s = append(s, cats[k].Name)
		}
		return strings.Join(s, ",")
	}
	if c := names(classify("x " + got)); !strings.Contains(c, "门") || !strings.Contains(c, "玻璃") || !strings.Contains(c, "雷") {
		t.Errorf("classify from meta: %s", c)
	}
	// 读出来还是一条好 WAV
	if pf, err := probeNative(src, "wav"); err != nil || math.Abs(pf.dur()-0.2) > 0.001 {
		t.Errorf("probe after meta: %v", err)
	}
}

func TestUCSWords(t *testing.T) {
	cases := map[string]string{
		"DOORWood_Creak Slow_JD_ABC": "door wood creak slow jd abc",
		"WATRDrip":                   "watr drip",
		"GUNAuto":                    "gun auto",
		"UIClick":                    "ui click",
		"ABC":                        "abc",
		"footstep01":                 "footstep 01",
	}
	for in, want := range cases {
		if got := strings.Join(words(in), " "); got != want {
			t.Errorf("%s → %s want %s", in, got, want)
		}
	}
}

// 起音、持续度、低高频占比各自分得开
func TestShapeFeatures(t *testing.T) {
	sr := 44100
	mk := func(sec float64, f func(t float64) float64) []float32 {
		x := make([]float32, int(sec*float64(sr)))
		for i := range x {
			x[i] = float32(f(float64(i) / float64(sr)))
		}
		return x
	}
	kick := features(mk(0.5, func(t float64) float64 { return math.Exp(-9*t) * math.Sin(2*math.Pi*55*t) }), sr)
	pad := features(mk(4, func(t float64) float64 { return math.Min(1, t/1.5) * 0.5 * math.Sin(2*math.Pi*330*t) }), sr)
	hiss := features(mk(1, func(t float64) float64 { return 0.3 * math.Sin(2*math.Pi*9000*t) }), sr)
	if kick.At > 0.03 || kick.Ds*0.5 > 0.4 || kick.Lo < 0.9 {
		t.Errorf("kick %+v", kick)
	}
	if pad.At < 1 || pad.Ds < 0.7 || pad.Lo > 0.2 || pad.Hi > 0.1 {
		t.Errorf("pad %+v", pad)
	}
	if hiss.Hi < 0.9 {
		t.Errorf("hiss %+v", hiss)
	}
	if len(kick.Env) != envN || kick.Env[0] < 200 || kick.Env[envN-1] > 10 {
		t.Errorf("env %v", kick.Env)
	}
}

func TestOnlyCopiesCanBeTrashed(t *testing.T) {
	ff := ffOrSkip(t)
	d := t.TempDir()
	writeWav(t, filepath.Join(d, "库", "原.wav"), 0, 2, 48000, 1, 16, 0.5)
	l := newLib(filepath.Join(d, "data"), ff, func(string, any) {})
	l.SetOut(filepath.Join(d, "导出"))
	t.Cleanup(l.bg.Wait)
	l.Add([]string{filepath.Join(d, "库")})
	orig := l.State().Items[0].ID
	c, err := l.cut(orig, 0.5, 1)
	if err != nil {
		t.Fatal(err)
	}
	l.bg.Wait()
	l.scan()
	if n := len(l.State().Items); n != 1 { // 只截不进库：拖出没放下前不该出现在库里
		t.Fatalf("cut joined the library: %d items", n)
	}
	os.Remove(c) // 反悔：DragClip 在没放下时就这样删
	if _, err := l.Clip(orig, 0.5, 1); err != nil {
		t.Fatal(err)
	}
	l.bg.Wait()
	l.scan()
	var cp int
	for _, it := range l.State().Items {
		if it.ID != orig {
			cp = it.ID
		}
	}
	if cp == 0 {
		t.Fatal("clip not in library")
	}
	if l.copies([]int{orig}) != nil || l.copies([]int{orig, cp}) != nil {
		t.Fatal("original offered for trash")
	}
	ps := l.copies([]int{cp})
	if len(ps) != 1 {
		t.Fatal(ps)
	}
	if n := l.trash(ps, os.Remove); n != 1 {
		t.Fatal(n)
	}
	if _, err := os.Stat(filepath.Join(d, "库", "原.wav")); err != nil {
		t.Fatal("original gone")
	}
	if n := len(l.State().Items); n != 1 {
		t.Fatalf("trashed copy still listed: %d", n)
	}
}

func TestBadHeadersDoNotPanic(t *testing.T) {
	// 位深 4、单声道：每帧 0 字节，算帧数不能除零
	if n := (format{Ch: 1, Bits: 4, Len: 100}).frames(); n != 0 {
		t.Fatalf("frames=%d", n)
	}
	// fmt 块报 4GB：应判坏文件，不照着分配
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVEfmt \xff\xff\xff\xff")
	if _, err := probeWav(bytes.NewReader(b.Bytes())); err == nil {
		t.Fatal("want errFormat")
	}
}
