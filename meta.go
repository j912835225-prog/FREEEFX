package main

// 文件里自带的说明：WAV 的 BWF bext 描述、iXML（类目、名字、关键词）、LIST INFO；
// 别的格式取 ffmpeg 报出来的标签。读出来的话并进搜索，也拿去认类目。

import (
	"encoding/binary"
	"io"
	"os"
	"regexp"
	"strings"
)

var (
	reIXML  = regexp.MustCompile(`(?is)<(CATEGORY|SUBCATEGORY|CATID|CATEGORYFULL|FXNAME|DESCRIPTION|KEYWORDS|NOTE|TRACK_NAME|USER_CATEGORY|LIBRARY)>([^<]*)</`)
	infoIDs = map[string]bool{"INAM": true, "ICMT": true, "IKEY": true, "ISBJ": true, "IGNR": true}
	tagKeys = map[string]bool{"title": true, "comment": true, "description": true, "genre": true, "keywords": true, "subject": true, "category": true}
)

func cstr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), ""))
}

// wavMeta 走一遍 RIFF 的块，跳过采样数据，只读说明类的块。读不出就空。
func wavMeta(path string) string {
	fh, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer fh.Close()
	var h [12]byte
	if _, err := io.ReadFull(fh, h[:]); err != nil || string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		return ""
	}
	var out []string
	add := func(s string) {
		s = strings.Join(strings.Fields(s), " ")
		if s != "" && len(out) < 24 {
			out = append(out, s)
		}
	}
	read := func(n int64) []byte {
		if n > 1<<20 { // 说明块不会这么大，大了不读
			fh.Seek(n, io.SeekCurrent)
			return nil
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(fh, b); err != nil {
			return nil
		}
		return b
	}
	for i := 0; i < 64; i++ {
		var ch [8]byte
		if _, err := io.ReadFull(fh, ch[:]); err != nil {
			break
		}
		id, n := string(ch[0:4]), int64(binary.LittleEndian.Uint32(ch[4:8]))
		switch id {
		case "bext":
			if b := read(n); len(b) >= 256 {
				add(cstr(b[:256]))
			}
		case "iXML":
			if b := read(n); b != nil {
				for _, m := range reIXML.FindAllStringSubmatch(string(b), -1) {
					add(m[2])
				}
			}
		case "LIST":
			b := read(n)
			if len(b) < 4 || string(b[:4]) != "INFO" {
				break
			}
			for p := 4; p+8 <= len(b); {
				sid, sn := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:p+8]))
				p += 8
				if sn < 0 || p+sn > len(b) {
					break
				}
				if infoIDs[sid] {
					add(cstr(b[p : p+sn]))
				}
				p += sn + sn%2
			}
		default:
			if _, err := fh.Seek(n, io.SeekCurrent); err != nil {
				return dedup(out)
			}
		}
		if n%2 == 1 {
			fh.Seek(1, io.SeekCurrent)
		}
	}
	return dedup(out)
}

// ffTags 从 ffmpeg 报的输入头里取标签（Output 段之前的 Metadata）。
func ffTags(e string) string {
	if i := strings.Index(e, "Output #"); i >= 0 {
		e = e[:i]
	}
	var out []string
	for _, ln := range strings.Split(e, "\n") {
		k, v, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		if tagKeys[k] {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
	}
	return dedup(out)
}

func dedup(xs []string) string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[strings.ToLower(x)] {
			seen[strings.ToLower(x)] = true
			out = append(out, x)
		}
	}
	s := strings.Join(out, " · ")
	if len(s) > 600 {
		s = strings.ToValidUTF8(s[:600], "")
	}
	return s
}
