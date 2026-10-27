package main

// 去重：同一个声音放在几处（拷贝、备份盘、两个音效夹里各一份），内容一模一样的只列一条，
// 另几份记在它的 Dup 里，鼠标停在行上能看到都在哪。文件一个不动，只是不重复列。
// 认法：先按大小分堆，同大小的才读内容比指纹——大小独一份的不读盘。
// 留哪一份：打过星或写过批注的那份优先，否则按根的先后、路径排序取第一份。

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

type hent struct {
	size, mod int64
	sum       string
}

// 小于这个的整份读来比；更大的读头、中、尾各一截再加上大小，够分得出不同的录音。
const (
	hashWhole = 64 << 20
	hashChunk = 4 << 20
)

// contentSum 算一份文件的内容指纹；大小与改动时间没变就用上次的。读不了返回空。
func (l *Lib) contentSum(abs string, size, mod int64) string {
	if l.hcache == nil {
		l.hcache = map[string]hent{}
	}
	if h, ok := l.hcache[abs]; ok && h.size == size && h.mod == mod {
		return h.sum
	}
	f, err := os.Open(abs)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if size <= hashWhole {
		if _, err := io.Copy(h, f); err != nil {
			return ""
		}
	} else {
		for _, at := range []int64{0, size/2 - hashChunk/2, size - hashChunk} {
			if _, err := io.Copy(h, io.NewSectionReader(f, at, hashChunk)); err != nil {
				return ""
			}
		}
	}
	sum := hex.EncodeToString(h.Sum(nil))
	l.hcache[abs] = hent{size, mod, sum}
	return sum
}

// dedup 给扫到的 n 份文件（已按根、路径排好）认重复。hidden[i] 为真的不列；
// dups[i] 是留下的那份名下另几份的路径。只在 scan 里调。
func (l *Lib) dedup(n int, at func(int) (abs string, size, mod int64)) (hidden []bool, dups map[int][]string) {
	hidden, dups = make([]bool, n), map[int][]string{}
	bySize := map[int64][]int{}
	for i := 0; i < n; i++ {
		if _, size, _ := at(i); size > 0 {
			bySize[size] = append(bySize[size], i)
		}
	}
	for _, idx := range bySize {
		if len(idx) < 2 {
			continue
		}
		bySum := map[string][]int{}
		var order []string
		for _, i := range idx {
			abs, size, mod := at(i)
			s := l.contentSum(abs, size, mod)
			if s == "" {
				continue
			}
			if bySum[s] == nil {
				order = append(order, s)
			}
			bySum[s] = append(bySum[s], i)
		}
		for _, s := range order {
			g := bySum[s]
			if len(g) < 2 {
				continue
			}
			keep := g[0]
			l.mu.RLock()
			for _, i := range g {
				abs, _, _ := at(i)
				if m := l.meta[abs]; m != nil && (m.Star > 0 || m.Note != "") {
					keep = i
					break
				}
			}
			l.mu.RUnlock()
			for _, i := range g {
				if i != keep {
					hidden[i] = true
					abs, _, _ := at(i)
					dups[keep] = append(dups[keep], abs)
				}
			}
		}
	}
	return hidden, dups
}
