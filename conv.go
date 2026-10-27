package main

// 转格式与截段：用随包的 ffmpeg（LGPL，只编了音频），产物一律放进导出夹（默认 音乐/FREEEFX），同名就加编号，绝不覆盖；导出夹收进库。
// 播放：snd 协议只供库里条目本身（按 id 取），别的路径一概不给。

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

type Fmt struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	ext   string
	args  []string
}

var formats = []Fmt{
	{"wav16", "WAV 16-bit", "wav", []string{"-c:a", "pcm_s16le"}},
	{"wav24", "WAV 24-bit", "wav", []string{"-c:a", "pcm_s24le"}},
	{"wav48", "WAV 24-bit · 48 kHz", "wav", []string{"-c:a", "pcm_s24le", "-ar", "48000"}},
	{"aiff", "AIFF 24-bit", "aiff", []string{"-c:a", "pcm_s24be"}},
	{"flac", "FLAC", "flac", []string{"-c:a", "flac"}},
	{"alac", "ALAC（m4a 无损）", "m4a", []string{"-c:a", "alac"}},
	{"aac", "AAC 256k（m4a）", "m4a", []string{"-c:a", "aac", "-b:a", "256k"}},
}

func fmtByID(id string) (Fmt, bool) {
	for _, f := range formats {
		if f.ID == id {
			return f, true
		}
	}
	return Fmt{}, false
}

var outMu sync.Mutex

// outPath 在 dir 里找一个空名字：先 名.扩展名，占了就 名 2.扩展名、名 3……
// 占位靠先建一个空文件（O_EXCL），两路同时转同名也不会撞。
func outPath(dir, base, ext string) (string, error) {
	outMu.Lock()
	defer outMu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for i := 1; i < 1000; i++ {
		name := base + "." + ext
		if i > 1 {
			name = base + " " + strconv.Itoa(i) + "." + ext
		}
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			f.Close()
			return p, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("no free name for %s", base)
}

func convertOne(ff, dir, src string, f Fmt) (string, error) {
	out, err := outPath(dir, strings.TrimSuffix(filepath.Base(src), filepath.Ext(src)), f.ext)
	if err != nil {
		return "", err
	}
	// 占位的空文件由 ffmpeg 覆写（-y 只对这个刚建的空文件生效）；失败就删掉占位
	args := append([]string{"-hide_banner", "-nostdin", "-v", "error", "-y", "-i", src, "-vn", "-map", "0:a:0", "-map_metadata", "0"}, f.args...)
	if f.ext == "m4a" {
		args = append(args, "-f", "ipod")
	}
	args = append(args, out)
	cmd := exec.Command(ff, args...)
	hideWindow(cmd)
	if b, err := cmd.CombinedOutput(); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("%s: %s", filepath.Base(src), strings.TrimSpace(lastLine(string(b))))
	}
	return out, nil
}

type ConvResult struct {
	Done   int      `json:"done"`
	Failed []string `json:"failed"`
	Out    []string `json:"out"`
}

// Convert 把若干条转成某格式，三路并行。完了重扫，新文件就出现在库里。
func (l *Lib) Convert(ids []int, fmtID string) (ConvResult, error) {
	var res ConvResult
	f, ok := fmtByID(fmtID)
	if !ok {
		return res, fmt.Errorf("unknown format %q", fmtID)
	}
	if l.ff == "" {
		return res, fmt.Errorf("这一版没带 ffmpeg，转不了格式")
	}
	srcs := l.paths(ids)
	dir := l.outDir()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for _, s := range srcs {
		wg.Add(1)
		sem <- struct{}{}
		go func(s string) {
			defer wg.Done()
			defer func() { <-sem }()
			out, err := convertOne(l.ff, dir, s, f)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Failed = append(res.Failed, err.Error())
				return
			}
			res.Done++
			res.Out = append(res.Out, out)
			l.emit("progress", Progress{"转成 " + f.Label, res.Done + len(res.Failed), len(srcs)})
		}(s)
	}
	wg.Wait()
	if res.Done > 0 {
		l.addRoots([]string{dir})
	}
	l.scan()
	if res.Failed == nil {
		res.Failed = []string{}
	}
	if res.Out == nil {
		res.Out = []string{}
	}
	return res, nil
}

// serveSnd 供 /f/<id>：原文件；/t/<id>：页面放不了的格式，ffmpeg 现转成 WAV 流。
func (l *Lib) serveSnd(rw http.ResponseWriter, req *http.Request) {
	p := strings.Trim(req.URL.Path, "/")
	kind, idStr, _ := strings.Cut(p, "/")
	id, err := strconv.Atoi(idStr)
	it := l.item(id)
	if err != nil || it == nil {
		http.NotFound(rw, req)
		return
	}
	switch kind {
	case "f":
		fh, err := os.Open(it.abs)
		if err != nil {
			http.NotFound(rw, req)
			return
		}
		defer fh.Close()
		st, err := fh.Stat()
		if err != nil {
			http.NotFound(rw, req)
			return
		}
		http.ServeContent(rw, req, "a."+it.Ext, st.ModTime(), fh)
	case "t":
		if l.ff == "" {
			http.NotFound(rw, req)
			return
		}
		cmd := exec.Command(l.ff, "-hide_banner", "-nostdin", "-v", "error", "-i", it.abs, "-vn", "-map", "0:a:0", "-c:a", "pcm_s16le", "-f", "wav", "-")
		hideWindow(cmd)
		rw.Header().Set("Content-Type", "audio/wav")
		cmd.Stdout = rw
		cmd.Run()
	default:
		http.NotFound(rw, req)
	}
}

// outDir 导出夹；没设过就是 音乐/FREEEFX。
func (l *Lib) outDir() string {
	l.mu.RLock()
	d := l.out
	l.mu.RUnlock()
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, "Music", "FREEEFX")
	}
	return d
}

// SetOut 换导出夹。已在库里的旧导出夹不动。
func (l *Lib) SetOut(dir string) {
	l.mu.Lock()
	l.out = filepath.Clean(dir)
	l.mu.Unlock()
	l.saveConfig()
}

// copies 选中的若全是导出夹里的副本（截出、转出的），给出它们的路径；夹杂一条原件就给空——原件不许从这里删。
func (l *Lib) copies(ids []int) []string {
	out := l.outDir() + string(filepath.Separator)
	ps := l.paths(ids)
	for _, p := range ps {
		if !strings.HasPrefix(p, out) {
			return nil
		}
	}
	return ps
}

// trash 把副本移进废纸篓（回收站）再扫一遍，回报删了几个。
func (l *Lib) trash(ps []string, mv func(string) error) int {
	n := 0
	for _, p := range ps {
		if mv(p) == nil {
			n++
		}
	}
	l.scan()
	return n
}

// OutDir 给页面：导出夹在哪。
func (l *Lib) OutDir() string { return l.outDir() }

// Clip 把一条的 a–b 秒截成 WAV 24-bit，首尾各淡 5 毫秒免得咔哒，放进 导出夹/片段，并进库。
func (l *Lib) Clip(id int, a, b float64) (string, error) {
	p, err := l.cut(id, a, b)
	if err == nil {
		l.keepOut()
	}
	return p, err
}

// keepOut 导出夹进库并扫一遍。
func (l *Lib) keepOut() {
	l.addRoots([]string{l.outDir()})
	l.bg.Add(1)
	go func() { defer l.bg.Done(); l.scan() }()
}

// cut 只截不进库：拖出的片段要等真放下了才算数。
func (l *Lib) cut(id int, a, b float64) (string, error) {
	it := l.item(id)
	if it == nil {
		return "", fmt.Errorf("gone")
	}
	if l.ff == "" {
		return "", fmt.Errorf("这一版没带 ffmpeg，截不了")
	}
	if a < 0 {
		a = 0
	}
	n := b - a
	if n < 0.01 {
		return "", fmt.Errorf("这段太短")
	}
	base := strings.TrimSuffix(filepath.Base(it.abs), filepath.Ext(it.abs)) + " " + clipTime(a) + "-" + clipTime(b)
	dir := filepath.Join(l.outDir(), "片段")
	out, err := outPath(dir, base, "wav")
	if err != nil {
		return "", err
	}
	// -ss 放在 -i 前：直接跳到段头，长文件拖出时不必从头解码。
	// 随包的 Mac 版 ffmpeg 只编了最少的滤镜（没有 afade），淡入淡出在下面 fadeEdges 里自己做
	cmd := exec.Command(l.ff, "-hide_banner", "-nostdin", "-v", "error", "-y", "-ss", strconv.FormatFloat(a, 'f', 4, 64), "-t", strconv.FormatFloat(n, 'f', 4, 64), "-i", it.abs,
		"-vn", "-map", "0:a:0", "-map_metadata", "-1", "-c:a", "pcm_s24le", out)
	hideWindow(cmd)
	if bs, err := cmd.CombinedOutput(); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("没截成：%s", strings.TrimSpace(lastLine(string(bs))))
	}
	if err := fadeEdges(out, 0.005); err != nil {
		os.Remove(out)
		return "", fmt.Errorf("没截成：%v", err)
	}
	return out, nil
}

// clipTime 1.25 秒写成 0m01.25s 这种文件名里也安全的样子。
func clipTime(s float64) string {
	m := int(s) / 60
	return fmt.Sprintf("%dm%05.2fs", m, s-float64(m*60))
}

// fadeEdges 给 24-bit WAV 首尾各做 sec 秒的线性淡入淡出（段太短就按四分之一段长），原地改。
func fadeEdges(path string, sec float64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if len(b) < 12 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return fmt.Errorf("不是 WAV")
	}
	ch, sr, bits := 0, 0, 0
	for i := 12; i+8 <= len(b); {
		id, n := string(b[i:i+4]), int(binary.LittleEndian.Uint32(b[i+4:i+8]))
		body := i + 8
		if body+n > len(b) {
			n = len(b) - body
		}
		switch id {
		case "fmt ":
			if n >= 16 {
				ch, sr, bits = int(binary.LittleEndian.Uint16(b[body+2:])), int(binary.LittleEndian.Uint32(b[body+4:])), int(binary.LittleEndian.Uint16(b[body+14:]))
			}
		case "data":
			if bits != 24 || ch < 1 || sr < 1 {
				return fmt.Errorf("不是 24-bit WAV")
			}
			d := b[body : body+n]
			frames := len(d) / (3 * ch)
			k := min(int(sec*float64(sr)), frames/4)
			for j := 0; j < k; j++ {
				g := float64(j) / float64(k)
				for _, fr := range []int{j, frames - 1 - j} {
					for c := 0; c < ch; c++ {
						o := (fr*ch + c) * 3
						v := int32(uint32(d[o])|uint32(d[o+1])<<8|uint32(d[o+2])<<16) << 8 >> 8
						v = int32(float64(v) * g)
						d[o], d[o+1], d[o+2] = byte(v), byte(v>>8), byte(v>>16)
					}
				}
			}
			_, err := f.WriteAt(d, int64(body))
			return err
		}
		i = body + n + n&1
	}
	return fmt.Errorf("WAV 里没有声音")
}
