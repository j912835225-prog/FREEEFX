package main

// 读声音：WAV、AIFF 由 Go 直读（快，不开进程），其余交随包的 ffmpeg 解成单声道 16 位。
// 读出来的东西两种用途：列表用的特征（时长、峰值、响度、音色明暗），选中时看的波形与频谱。

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"math/cmplx"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type format struct {
	SR, Ch, Bits int
	Float        bool
	BE           bool
	Off, Len     int64  // 采样数据在文件里的位置与字节数
	Tags         string // ffmpeg 报出来的标题、注释之类
}

func (f format) frames() int64 {
	if f.Ch == 0 || f.Bits == 0 {
		return 0
	}
	return f.Len / int64(f.Ch*f.Bits/8)
}

func (f format) dur() float64 {
	if f.SR == 0 {
		return 0
	}
	return float64(f.frames()) / float64(f.SR)
}

var errFormat = errors.New("unsupported")

// probeWav 只读头。认 PCM 8/16/24/32、float 32/64、WAVE_FORMAT_EXTENSIBLE。
func probeWav(r io.ReadSeeker) (format, error) {
	var f format
	var h [12]byte
	if _, err := io.ReadFull(r, h[:]); err != nil || string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		return f, errFormat
	}
	pos := int64(12)
	got := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return f, errFormat
		}
		id, n := string(ch[0:4]), int64(binary.LittleEndian.Uint32(ch[4:8]))
		pos += 8
		switch id {
		case "fmt ":
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil || n < 16 {
				return f, errFormat
			}
			tag := binary.LittleEndian.Uint16(b[0:2])
			f.Ch = int(binary.LittleEndian.Uint16(b[2:4]))
			f.SR = int(binary.LittleEndian.Uint32(b[4:8]))
			f.Bits = int(binary.LittleEndian.Uint16(b[14:16]))
			if tag == 0xFFFE && n >= 26 {
				tag = binary.LittleEndian.Uint16(b[24:26])
			}
			switch tag {
			case 1:
			case 3:
				f.Float = true
			default:
				return f, errFormat
			}
			got = true
		case "data":
			if !got {
				return f, errFormat
			}
			f.Off, f.Len = pos, n
			if end, err := r.Seek(0, io.SeekEnd); err == nil && f.Off+f.Len > end {
				f.Len = end - f.Off // 录音机没写完头、或写了 0xFFFFFFFF 的
			}
			return f, nil
		default:
			if _, err := r.Seek(n, io.SeekCurrent); err != nil {
				return f, errFormat
			}
		}
		if n%2 == 1 {
			r.Seek(1, io.SeekCurrent)
			pos++
		}
		pos += n
		if id == "fmt " {
			continue
		}
	}
}

// probeAiff 认 AIFF 与 AIFC 的 NONE／sowt／fl32。
func probeAiff(r io.ReadSeeker) (format, error) {
	var f format
	var h [12]byte
	if _, err := io.ReadFull(r, h[:]); err != nil || string(h[0:4]) != "FORM" || (string(h[8:12]) != "AIFF" && string(h[8:12]) != "AIFC") {
		return f, errFormat
	}
	f.BE = true
	pos := int64(12)
	got := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return f, errFormat
		}
		id, n := string(ch[0:4]), int64(binary.BigEndian.Uint32(ch[4:8]))
		pos += 8
		switch id {
		case "COMM":
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil || n < 18 {
				return f, errFormat
			}
			f.Ch = int(binary.BigEndian.Uint16(b[0:2]))
			f.Bits = int(binary.BigEndian.Uint16(b[6:8]))
			f.SR = int(ext80(b[8:18]))
			if n >= 22 {
				switch string(b[18:22]) {
				case "NONE", "twos":
				case "sowt":
					f.BE = false
				case "fl32", "FL32":
					f.Float, f.Bits = true, 32
				default:
					return f, errFormat
				}
			}
			got = true
		case "SSND":
			if !got {
				return f, errFormat
			}
			var b [8]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return f, errFormat
			}
			off := int64(binary.BigEndian.Uint32(b[0:4]))
			f.Off, f.Len = pos+8+off, n-8-off
			if end, err := r.Seek(0, io.SeekEnd); err == nil && f.Off+f.Len > end {
				f.Len = end - f.Off
			}
			return f, nil
		default:
			if _, err := r.Seek(n, io.SeekCurrent); err != nil {
				return f, errFormat
			}
		}
		if n%2 == 1 {
			r.Seek(1, io.SeekCurrent)
			pos++
		}
		pos += n
	}
}

// ext80 读 IEEE 754 80 位扩展精度（AIFF 的采样率就这么存）。
func ext80(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:2]) & 0x7FFF)
	man := binary.BigEndian.Uint64(b[2:10])
	if exp == 0 && man == 0 {
		return 0
	}
	return math.Ldexp(float64(man), exp-16383-63)
}

func probeNative(path, ext string) (format, error) {
	fh, err := os.Open(path)
	if err != nil {
		return format{}, err
	}
	defer fh.Close()
	switch ext {
	case "wav", "wave":
		return probeWav(fh)
	case "aif", "aiff", "aifc":
		return probeAiff(fh)
	}
	return format{}, errFormat
}

// readNative 把采样读成单声道 float32；maxSec>0 时只读到那里。
func readNative(path string, f format, maxSec float64) ([]float32, error) {
	if f.Ch <= 0 || f.Bits <= 0 || f.Bits > 64 || f.Bits%8 != 0 {
		return nil, errFormat
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	frames := f.frames()
	if maxSec > 0 && float64(frames) > maxSec*float64(f.SR) {
		frames = int64(maxSec * float64(f.SR))
	}
	if _, err := fh.Seek(f.Off, io.SeekStart); err != nil {
		return nil, err
	}
	bps := f.Bits / 8
	fsz := bps * f.Ch
	out := make([]float32, frames)
	br := bufio.NewReaderSize(fh, 1<<16)
	buf := make([]byte, fsz*4096)
	var i int64
	inv := 1 / float32(f.Ch)
	for i < frames {
		n := int64(len(buf) / fsz)
		if frames-i < n {
			n = frames - i
		}
		b := buf[:n*int64(fsz)]
		m, err := io.ReadFull(br, b)
		n = int64(m / fsz)
		for k := int64(0); k < n; k++ {
			var s float32
			fr := b[k*int64(fsz):]
			for c := 0; c < f.Ch; c++ {
				s += sample(fr[c*bps:], bps, f.Float, f.BE)
			}
			out[i+k] = s * inv
		}
		i += n
		if err != nil {
			break
		}
	}
	return out[:i], nil
}

func sample(b []byte, bps int, fl, be bool) float32 {
	var o binary.ByteOrder = binary.LittleEndian
	if be {
		o = binary.BigEndian
	}
	switch {
	case fl && bps == 4:
		return math.Float32frombits(o.Uint32(b))
	case fl && bps == 8:
		return float32(math.Float64frombits(o.Uint64(b)))
	case bps == 1:
		if be { // AIFF 的 8 位是有符号的
			return float32(int8(b[0])) / 128
		}
		return (float32(b[0]) - 128) / 128
	case bps == 2:
		return float32(int16(o.Uint16(b))) / 32768
	case bps == 3:
		var v int32
		if be {
			v = int32(b[0])<<24 | int32(b[1])<<16 | int32(b[2])<<8
		} else {
			v = int32(b[2])<<24 | int32(b[1])<<16 | int32(b[0])<<8
		}
		return float32(v) / 2147483648
	case bps == 4:
		return float32(int32(o.Uint32(b))) / 2147483648
	}
	return 0
}

var (
	reDur    = regexp.MustCompile(`Duration: (\d+):(\d\d):(\d\d(?:\.\d+)?)`)
	reStream = regexp.MustCompile(`Audio: [^,]+(?:\([^)]*\))?, (\d+) Hz, ([^,]+)`)
)

// ffRead 用 ffmpeg 解成单声道 s16le（随包的 ffmpeg 只编了 s16le 这一种裸 PCM 输出），sr 是解出来的采样率。info 是从 ffmpeg 报的头里读到的原样信息。
func ffRead(ff, path string, sr int, maxSec float64) ([]float32, format, error) {
	var info format
	if ff == "" {
		return nil, info, errFormat
	}
	args := []string{"-hide_banner", "-nostdin", "-v", "info"}
	if maxSec > 0 {
		args = append(args, "-t", strconv.FormatFloat(maxSec, 'f', 1, 64))
	}
	args = append(args, "-i", path, "-vn", "-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(sr), "-c:a", "pcm_s16le", "-f", "s16le", "-")
	cmd := exec.Command(ff, args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	e := stderr.String()
	if m := reStream.FindStringSubmatch(e); m != nil {
		info.SR, _ = strconv.Atoi(m[1])
		switch c := strings.TrimSpace(m[2]); {
		case c == "mono":
			info.Ch = 1
		case c == "stereo":
			info.Ch = 2
		case strings.HasSuffix(c, "channels"):
			info.Ch, _ = strconv.Atoi(strings.Fields(c)[0])
		default:
			if p := strings.SplitN(c, ".", 2); len(p) == 2 { // 5.1 之类
				a, _ := strconv.Atoi(p[0])
				b, _ := strconv.Atoi(strings.Split(p[1], "(")[0])
				info.Ch = a + b
			}
		}
	}
	if m := reDur.FindStringSubmatch(e); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		s, _ := strconv.ParseFloat(m[3], 64)
		info.Len = int64((float64(h*3600+mi*60) + s) * 1e6) // 借 Len 带微秒时长
	}
	info.Tags = ffTags(e)
	if err != nil && len(out) == 0 {
		return nil, info, errors.New(strings.TrimSpace(lastLine(e)))
	}
	pcm := make([]float32, len(out)/2)
	for i := range pcm {
		pcm[i] = float32(int16(binary.LittleEndian.Uint16(out[i*2:]))) / 32768
	}
	return pcm, info, nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ---------- 特征 ----------

type feat struct {
	Peak, RMS, Cen float32
	Env            []byte  // envN 个峰值，按本条自己的峰归一，0–255，列表里画小波形
	At             float32 // 起音：从出声到最响用了几秒
	Ds             float32 // 持续度：出声以后，离最响不到 20 dB 的时间占几成
	Lo, Hi         float32 // 200 Hz 以下、5 kHz 以上的能量各占几成
}

const envN = 48

func db(v float64) float32 {
	if v < 1e-9 {
		return -180
	}
	return float32(20 * math.Log10(v))
}

// features：峰值、均方根（dBFS），频谱重心（Hz，按能量加权的若干帧平均，静的帧不算），
// 小波形、起音与持续度（10 毫秒一帧的均方根），高低频能量占比。
func features(x []float32, sr int) feat {
	var pk, ss float64
	for _, v := range x {
		a := math.Abs(float64(v))
		if a > pk {
			pk = a
		}
		ss += float64(v) * float64(v)
	}
	ft := feat{Peak: db(pk), RMS: -180, Env: make([]byte, envN)}
	if len(x) > 0 {
		ft.RMS = db(math.Sqrt(ss / float64(len(x))))
	}
	if pk > 1e-6 {
		for i := 0; i < envN; i++ {
			a, b := i*len(x)/envN, (i+1)*len(x)/envN
			if b <= a {
				b = min(a+1, len(x))
			}
			var m float64
			for _, v := range x[a:b] {
				if v := math.Abs(float64(v)); v > m {
					m = v
				}
			}
			ft.Env[i] = byte(math.Min(255, m/pk*255+0.5))
		}
	}
	if sr > 0 && len(x) > 0 {
		hop := max(1, sr/100)
		var fr []float64
		for p := 0; p < len(x); p += hop {
			var e float64
			q := min(p+hop, len(x))
			for _, v := range x[p:q] {
				e += float64(v) * float64(v)
			}
			fr = append(fr, math.Sqrt(e/float64(q-p)))
		}
		top, at := 0.0, 0
		for i, v := range fr {
			if v > top {
				top, at = v, i
			}
		}
		if top > 1e-5 {
			start := 0
			for start < len(fr) && fr[start] < top*0.01 { // 比最响低 40 dB 以内算出了声
				start++
			}
			ft.At = float32(at-start) / 100
			n, loud := 0, 0
			for _, v := range fr[start:] {
				n++
				if v >= top*0.1 {
					loud++
				}
			}
			if n > 0 {
				ft.Ds = float32(loud) / float32(n)
			}
		}
	}
	const N = 2048
	if len(x) < N/4 || sr == 0 {
		return ft
	}
	frames := 48
	step := (len(x) - N) / frames
	if step < N/2 {
		step = N / 2
	}
	win := hann(N)
	buf := make([]complex128, N)
	var num, den, lo, hi, all float64
	kLo, kHi := 200*N/sr, 5000*N/sr
	for p := 0; p+N/4 <= len(x); p += step {
		var e float64
		for i := 0; i < N; i++ {
			v := 0.0
			if p+i < len(x) {
				v = float64(x[p+i])
			}
			e += v * v
			buf[i] = complex(v*win[i], 0)
		}
		if e/N < 1e-8 { // 约 -80 dB 以下当静
			continue
		}
		fft(buf)
		var fn, fd float64
		for k := 1; k < N/2; k++ {
			m := cmplx.Abs(buf[k])
			fn += float64(k) * m
			fd += m
			p2 := m * m
			all += p2
			if k <= kLo {
				lo += p2
			} else if k >= kHi {
				hi += p2
			}
		}
		if fd > 0 {
			c := fn / fd * float64(sr) / N
			num += c * e
			den += e
		}
	}
	if den > 0 {
		ft.Cen = float32(num / den)
	}
	if all > 0 {
		ft.Lo, ft.Hi = float32(lo/all), float32(hi/all)
	}
	return ft
}

var hannCache = map[int][]float64{}

func hann(n int) []float64 {
	if w, ok := hannCache[n]; ok {
		return w
	}
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	hannCache[n] = w
	return w
}

func init() { hann(2048); hann(1024) } // 先算好，免得并发写 map

// fft 原地基 2。
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(l)))
		for i := 0; i < n; i += l {
			wn := complex(1, 0)
			for k := 0; k < l/2; k++ {
				u, v := a[i+k], a[i+k+l/2]*wn
				a[i+k], a[i+k+l/2] = u+v, u-v
				wn *= w
			}
		}
	}
}

// ---------- 选中时看的：波形与频谱 ----------

const (
	waveW    = 1600
	specCols = 720
	specRows = 96
)

type Detail struct {
	Wave []byte  `json:"wave"` // waveW 个峰值，0–255
	Spec []byte  `json:"spec"` // specCols×specRows，按列存，行 0 是最低频；0–255 对应 -96–0 dB
	Cols int     `json:"cols"`
	Rows int     `json:"rows"`
	Lo   float64 `json:"lo"` // 频谱下沿、上沿 Hz
	Hi   float64 `json:"hi"`
	Dur  float64 `json:"dur"`
}

func detail(x []float32, sr int, dur float64) *Detail {
	d := &Detail{Cols: specCols, Rows: specRows, Dur: dur, Lo: 30}
	d.Hi = float64(sr) / 2
	d.Wave = make([]byte, waveW)
	if len(x) == 0 {
		d.Spec = make([]byte, specCols*specRows)
		return d
	}
	var top float32
	for _, v := range x {
		if v < 0 {
			v = -v
		}
		if v > top {
			top = v
		}
	}
	if top < 1e-6 {
		top = 1
	}
	for i := 0; i < waveW; i++ {
		a, b := i*len(x)/waveW, (i+1)*len(x)/waveW
		if b <= a {
			b = a + 1
		}
		var m float32
		for _, v := range x[a:min(b, len(x))] {
			if v < 0 {
				v = -v
			}
			if v > m {
				m = v
			}
		}
		d.Wave[i] = byte(math.Min(255, float64(m/top)*255+0.5))
	}
	N := 2048
	if sr <= 24000 {
		N = 1024
	}
	win := hann(N)
	buf := make([]complex128, N)
	d.Spec = make([]byte, specCols*specRows)
	// 行的频率：对数从 Lo 到 Hi，每行取覆盖的 bin 的最大值
	edges := make([]int, specRows+1)
	for r := 0; r <= specRows; r++ {
		f := d.Lo * math.Pow(d.Hi/d.Lo, float64(r)/specRows)
		edges[r] = int(f * float64(N) / float64(sr))
	}
	norm := 0.0
	for _, w := range win {
		norm += w
	}
	norm /= 2
	for c := 0; c < specCols; c++ {
		center := int((float64(c) + 0.5) * float64(len(x)) / specCols)
		p := center - N/2
		for i := 0; i < N; i++ {
			v := 0.0
			if j := p + i; j >= 0 && j < len(x) {
				v = float64(x[j])
			}
			buf[i] = complex(v*win[i], 0)
		}
		fft(buf)
		for r := 0; r < specRows; r++ {
			a, b := edges[r], edges[r+1]
			if b <= a {
				b = a + 1
			}
			var m float64
			for k := a; k < b && k < N/2; k++ {
				if v := cmplx.Abs(buf[k]); v > m {
					m = v
				}
			}
			dbv := 20 * math.Log10(m/norm+1e-12)
			v := (dbv + 96) / 96 * 255
			d.Spec[c*specRows+r] = byte(math.Max(0, math.Min(255, v)))
		}
	}
	return d
}
