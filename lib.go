package main

// 库：人拖进来或载入的文件夹与文件（根），只读这些，别处一概不碰。
// 数据夹里三份：config.json 记根，meta.json 记人给的（星、批注、加入时间、听过几次），cache.json 记算出来的（时长、峰值……），换了文件（大小或改动时间变了）就重算。

import (
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

var audioExt = map[string]bool{"wav": true, "wave": true, "aif": true, "aiff": true, "aifc": true, "flac": true, "mp3": true, "m4a": true, "aac": true, "ogg": true, "oga": true, "opus": true, "caf": true, "w64": true}

type Item struct {
	ID    int      `json:"id"`
	Root  int      `json:"r"`
	Rel   string   `json:"p"` // 相对根的路径，斜杠；根是单个文件时就是文件名
	Ext   string   `json:"e"`
	Size  int64    `json:"s"`
	Dur   float64  `json:"d"`
	SR    int      `json:"sr"`
	Ch    int      `json:"ch"`
	Bits  int      `json:"b"`
	Peak  float32  `json:"pk"`
	RMS   float32  `json:"rm"`
	Cen   float32  `json:"c"`
	Env   []byte   `json:"v"`  // 小波形
	At    float32  `json:"at"` // 起音秒数
	Ds    float32  `json:"ds"` // 持续度
	Lo    float32  `json:"lo"` // 低频占比
	Hi    float32  `json:"hi"` // 高频占比
	Tx    string   `json:"tx"` // 文件自带的说明
	Done  bool     `json:"a"`
	Bad   bool     `json:"x"`
	Cats  []int    `json:"k"`
	Star  int      `json:"st"`
	Note  string   `json:"no"`
	Added int64    `json:"ad"`
	Plays int      `json:"pl"`
	Dup   []string `json:"du,omitempty"` // 内容一模一样的另几份放在哪（不另列）
	abs   string
	mod   int64
	key   string // 认类目用的：根名／相对路径
}

type Meta struct {
	Star  int    `json:"st,omitempty"`
	Note  string `json:"no,omitempty"`
	Added int64  `json:"ad,omitempty"`
	Plays int    `json:"pl,omitempty"`
}

type Feat struct {
	Size int64   `json:"s"`
	Mod  int64   `json:"m"`
	Dur  float64 `json:"d"`
	SR   int     `json:"sr,omitempty"`
	Ch   int     `json:"ch,omitempty"`
	Bits int     `json:"b,omitempty"`
	Peak float32 `json:"pk"`
	RMS  float32 `json:"rm"`
	Cen  float32 `json:"c"`
	Env  []byte  `json:"v,omitempty"`
	At   float32 `json:"at,omitempty"`
	Ds   float32 `json:"ds,omitempty"`
	Lo   float32 `json:"lo,omitempty"`
	Hi   float32 `json:"hi,omitempty"`
	Tx   string  `json:"tx,omitempty"`
	Bad  bool    `json:"x,omitempty"`
	Ver  int     `json:"ver,omitempty"`
}

// featVer 算法改了就加一，旧缓存自动重算。
const featVer = 2

type Root struct {
	Name string `json:"name"`
	Path string `json:"path"`
	File bool   `json:"file"`
}

type Lib struct {
	mu     sync.RWMutex
	data   string
	ff     string
	roots  []string
	out    string // 导出夹：转格式、截段的产物都放这里
	items  []*Item
	byID   map[int]*Item
	ids    map[string]int
	nextID int
	meta   map[string]*Meta
	cache  map[string]*Feat
	gen    int
	dirty  map[int]*Item // 分析出来、还没推给页面的
	scanAt time.Time
	sig    uint64
	saveMu sync.Mutex
	scanMu sync.Mutex      // 一次只扫一遍：截段后台重扫与手动重扫可能撞上
	hcache map[string]hent // 认重复用的内容指纹，只在 scan 里用（scanMu 管着）
	bg     sync.WaitGroup  // 后台重扫与分析，测试收尾时等它（Windows 上缓存没写完，临时夹删不掉）

	dmu    sync.Mutex
	dcache map[int]*list.Element
	dlru   *list.List

	emit func(name string, v any)
}

type dent struct {
	id int
	d  *Detail
}

func newLib(data, ff string, emit func(string, any)) *Lib {
	l := &Lib{}
	l.open(data, ff, emit)
	if l.out == "" { // -serve 与测试：导出夹落在数据夹里，不碰 音乐
		l.out = filepath.Join(data, "导出")
	}
	return l
}

func (l *Lib) open(data, ff string, emit func(string, any)) {
	l.data, l.ff, l.emit = data, ff, emit
	l.byID, l.ids, l.meta, l.cache, l.dirty = map[int]*Item{}, map[string]int{}, map[string]*Meta{}, map[string]*Feat{}, map[int]*Item{}
	l.dcache, l.dlru = map[int]*list.Element{}, list.New()
	var c struct {
		Roots []string `json:"roots"`
		Out   string   `json:"out"`
	}
	readJSON(filepath.Join(data, "config.json"), &c)
	l.roots, l.out = c.Roots, c.Out
	readJSON(filepath.Join(data, "meta.json"), &l.meta)
	readJSON(filepath.Join(data, "cache.json"), &l.cache)
	if l.meta == nil {
		l.meta = map[string]*Meta{}
	}
	if l.cache == nil {
		l.cache = map[string]*Feat{}
	}
	go l.pump()
}

func readJSON(p string, v any) {
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, v)
	}
}

func writeJSON(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (l *Lib) saveConfig() {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	l.mu.RLock()
	b := map[string]any{"roots": l.roots, "out": l.out}
	l.mu.RUnlock()
	writeJSON(filepath.Join(l.data, "config.json"), b)
}

func (l *Lib) saveMeta() {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	l.mu.RLock()
	b, _ := json.Marshal(l.meta)
	l.mu.RUnlock()
	tmp := filepath.Join(l.data, "meta.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(l.data, "meta.json"))
	}
}

func (l *Lib) saveCache() {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	l.mu.RLock()
	b, _ := json.Marshal(l.cache)
	l.mu.RUnlock()
	tmp := filepath.Join(l.data, "cache.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(l.data, "cache.json"))
	}
}

func extOf(p string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
}

// ---------- 根 ----------

func within(p, root string) bool {
	r, err := filepath.Rel(root, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// addRoots 收进新的根：已在某个根里的不重收，新根包住旧根的就把旧根换掉。返回收进了几个。
func (l *Lib) addRoots(paths []string) int {
	n := 0
	l.mu.Lock()
	for _, p := range paths {
		p = filepath.Clean(p)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() && !audioExt[extOf(p)] {
			continue
		}
		dup := false
		for _, r := range l.roots {
			if within(p, r) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		keep := l.roots[:0:0]
		for _, r := range l.roots {
			if !within(r, p) {
				keep = append(keep, r)
			}
		}
		l.roots = append(keep, p)
		n++
	}
	l.mu.Unlock()
	if n > 0 {
		l.saveConfig()
	}
	return n
}

// ---------- 扫 ----------

// scan 按根重排一遍条目。已知的（同路径、同大小、同改动时间）直接带上算好的，新的进分析队列。
func (l *Lib) scan() {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	l.mu.RLock()
	roots := append([]string(nil), l.roots...)
	l.mu.RUnlock()
	type found struct {
		root int
		rel  string
		abs  string
		size int64
		mod  int64
	}
	var all []found
	for ri, r := range roots {
		st, err := os.Stat(r)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			all = append(all, found{ri, filepath.Base(r), r, st.Size(), st.ModTime().UnixNano()})
			continue
		}
		filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") && p != r {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if strings.HasSuffix(strings.ToLower(name), ".app") || strings.HasSuffix(strings.ToLower(name), ".bundle") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, "._") || !audioExt[extOf(name)] {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(r, p)
			all = append(all, found{ri, filepath.ToSlash(rel), p, info.Size(), info.ModTime().UnixNano()})
			return nil
		})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].root != all[j].root {
			return all[i].root < all[j].root
		}
		return natLess(all[i].rel, all[j].rel)
	})
	hidden, dups := l.dedup(len(all), func(i int) (string, int64, int64) { return all[i].abs, all[i].size, all[i].mod })
	sig := fnv.New64a()
	fmt.Fprintln(sig, strings.Join(roots, "\x00"))
	for _, f := range all {
		fmt.Fprintf(sig, "%d\x00%s\x00%d\x00%d\n", f.root, f.abs, f.size, f.mod)
	}
	now := time.Now().Unix()
	l.mu.Lock()
	if l.sig == sig.Sum64() && l.items != nil {
		l.scanAt = time.Now() // 什么都没变：不重排、不惊动页面
		l.mu.Unlock()
		return
	}
	l.sig = sig.Sum64()
	l.gen++
	items := make([]*Item, 0, len(all))
	byID := make(map[int]*Item, len(all))
	metaChanged := false
	for fi, f := range all {
		if hidden[fi] {
			continue
		}
		id, ok := l.ids[f.abs]
		if !ok {
			l.nextID++
			id = l.nextID
			l.ids[f.abs] = id
		}
		it := &Item{ID: id, Root: f.root, Rel: f.rel, Ext: extOf(f.rel), Size: f.size, Dup: dups[fi], abs: f.abs, mod: f.mod}
		it.key = filepath.Base(roots[f.root]) + "/" + f.rel
		it.Cats = classify(it.key)
		m := l.meta[f.abs]
		if m == nil {
			m = &Meta{Added: now}
			l.meta[f.abs] = m
			metaChanged = true
		}
		it.Star, it.Note, it.Added, it.Plays = m.Star, m.Note, m.Added, m.Plays
		if c := l.cache[f.abs]; c != nil && c.Size == f.size && c.Mod == f.mod && c.Ver == featVer {
			it.apply(c)
		}
		items = append(items, it)
		byID[id] = it
	}
	l.items, l.byID = items, byID
	l.scanAt = time.Now()
	gen := l.gen
	l.mu.Unlock()
	if metaChanged {
		l.saveMeta()
	}
	l.emit("lib", gen)
	l.bg.Add(1)
	go func() { defer l.bg.Done(); l.analyze(gen) }()
}

func (it *Item) apply(c *Feat) {
	it.Dur, it.SR, it.Ch, it.Bits = c.Dur, c.SR, c.Ch, c.Bits
	it.Peak, it.RMS, it.Cen, it.Bad = c.Peak, c.RMS, c.Cen, c.Bad
	it.Env, it.At, it.Ds, it.Lo, it.Hi, it.Tx = c.Env, c.At, c.Ds, c.Lo, c.Hi, c.Tx
	if c.Tx != "" {
		it.Cats = classify(it.key + " " + c.Tx)
	}
	it.Done = true
}

// natLess 文件名里的数字按大小比：声2 排在 声10 前。
func natLess(a, b string) bool {
	for a != "" && b != "" {
		ca, cb := a[0], b[0]
		if isDigit(ca) && isDigit(cb) {
			i, j := 0, 0
			for i < len(a) && isDigit(a[i]) {
				i++
			}
			for j < len(b) && isDigit(b[j]) {
				j++
			}
			na, nb := strings.TrimLeft(a[:i], "0"), strings.TrimLeft(b[:j], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[i:], b[j:]
			continue
		}
		la, lb := strings.ToLower(a[:1]), strings.ToLower(b[:1])
		if la != lb {
			return la < lb
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// ---------- 分析 ----------

type Progress struct {
	Phase string `json:"phase"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

func workers() int {
	n := runtime.NumCPU() - 1
	if n > 6 {
		n = 6
	}
	if n < 2 {
		n = 2
	}
	return n
}

// analyze 两遍：先读头拿时长（WAV／AIFF 一眨眼），再细听（峰值、响度、音色）。换了扫描代数就停。
func (l *Lib) analyze(gen int) {
	l.mu.RLock()
	var todo []*Item
	for _, it := range l.items {
		if !it.Done {
			todo = append(todo, it)
		}
	}
	l.mu.RUnlock()
	if len(todo) == 0 {
		l.emit("progress", Progress{Done: 0, Total: 0})
		return
	}
	stale := func() bool {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return l.gen != gen
	}
	run := func(phase string, fn func(*Item)) bool {
		jobs := make(chan *Item)
		var wg sync.WaitGroup
		var done int
		var dmu sync.Mutex
		last := time.Now()
		for w := 0; w < workers(); w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for it := range jobs {
					fn(it)
					dmu.Lock()
					done++
					if time.Since(last) > 250*time.Millisecond {
						last = time.Now()
						l.emit("progress", Progress{phase, done, len(todo)})
					}
					dmu.Unlock()
				}
			}()
		}
		ok := true
		for _, it := range todo {
			if stale() {
				ok = false
				break
			}
			jobs <- it
		}
		close(jobs)
		wg.Wait()
		return ok
	}
	if !run("读时长", l.probe) {
		return
	}
	l.flush()
	saved := time.Now()
	ok := run("听音色", func(it *Item) {
		l.deep(it)
		if time.Since(saved) > 8*time.Second {
			saved = time.Now()
			go l.saveCache()
		}
	})
	l.flush()
	l.saveCache()
	if ok {
		l.emit("progress", Progress{Done: len(todo), Total: len(todo)})
	}
}

func (l *Lib) probe(it *Item) {
	f, err := probeNative(it.abs, it.Ext)
	if err != nil {
		return
	}
	l.mu.Lock()
	it.Dur, it.SR, it.Ch, it.Bits = f.dur(), f.SR, f.Ch, f.Bits
	l.dirty[it.ID] = it
	l.mu.Unlock()
}

const deepMax = 120 // 细听只听前两分钟：长曲子的音色看开头够了，省读盘

func (l *Lib) deep(it *Item) {
	var x []float32
	var tags string
	sr := 22050
	f, err := probeNative(it.abs, it.Ext)
	if err == nil {
		sr = f.SR
		x, err = readNative(it.abs, f, deepMax)
		if it.Ext == "wav" || it.Ext == "wave" {
			tags = wavMeta(it.abs)
		}
	}
	if err != nil {
		var info format
		x, info, err = ffRead(l.ff, it.abs, sr, deepMax)
		f = format{SR: info.SR, Ch: info.Ch}
		tags = info.Tags
		if info.Len > 0 {
			f.Len = -info.Len // 记下：这是微秒
		}
	}
	c := &Feat{Size: it.Size, Mod: it.mod, Ver: featVer, Tx: tags}
	if err != nil {
		c.Bad = true
	} else {
		ft := features(x, sr)
		c.Peak, c.RMS, c.Cen = ft.Peak, ft.RMS, ft.Cen
		c.Env, c.At, c.Ds, c.Lo, c.Hi = ft.Env, ft.At, ft.Ds, ft.Lo, ft.Hi
		c.SR, c.Ch, c.Bits = f.SR, f.Ch, f.Bits
		switch {
		case f.Len < 0:
			c.Dur = float64(-f.Len) / 1e6
		case f.Bits > 0:
			c.Dur = f.dur()
		default:
			c.Dur = float64(len(x)) / float64(sr)
		}
	}
	l.mu.Lock()
	l.cache[it.abs] = c
	it.apply(c)
	l.dirty[it.ID] = it
	l.mu.Unlock()
}

// pump 每 300 毫秒把算好的一批推给页面。
func (l *Lib) pump() {
	for range time.Tick(300 * time.Millisecond) {
		l.flush()
	}
}

func (l *Lib) flush() {
	l.mu.Lock()
	if len(l.dirty) == 0 {
		l.mu.Unlock()
		return
	}
	out := make([]Item, 0, len(l.dirty))
	for _, it := range l.dirty {
		out = append(out, *it)
	}
	l.dirty = map[int]*Item{}
	l.mu.Unlock()
	l.emit("items", out)
}

// ---------- 页面调的 ----------

type Snapshot struct {
	Roots []Root   `json:"roots"`
	Cats  []string `json:"cats"`
	Items []Item   `json:"items"`
	Conv  []Fmt    `json:"conv"` // 能转的格式；没带 ffmpeg 时为空
	Out   string   `json:"out"`  // 导出夹
}

// State 返回整个库。两万条约 7.5 MB（含小波形），页面一次拿齐，筛与搜都在页面里做。
func (l *Lib) State() Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s := Snapshot{Cats: catNames(), Items: make([]Item, len(l.items))}
	for i, it := range l.items {
		s.Items[i] = *it
	}
	for _, r := range l.roots {
		st, err := os.Stat(r)
		s.Roots = append(s.Roots, Root{Name: filepath.Base(r), Path: r, File: err == nil && !st.IsDir()})
	}
	if s.Roots == nil {
		s.Roots = []Root{}
	}
	s.Out = l.out
	s.Conv = []Fmt{}
	if l.ff != "" {
		s.Conv = formats
	}
	return s
}

// Add 收进拖进来或载入的路径，返回收进了几个根。
func (l *Lib) Add(paths []string) int {
	n := l.addRoots(paths)
	if n > 0 {
		l.scan()
	}
	return n
}

// Remove 把一个根移出库（文件不动）。
func (l *Lib) Remove(path string) {
	l.mu.Lock()
	keep := l.roots[:0:0]
	for _, r := range l.roots {
		if r != path {
			keep = append(keep, r)
		}
	}
	l.roots = keep
	l.mu.Unlock()
	l.saveConfig()
	l.scan()
}

// Rescan 窗口切回来时调：距上次扫过半分钟才重扫。
func (l *Lib) Rescan(force bool) {
	l.mu.RLock()
	fresh := time.Since(l.scanAt) < 30*time.Second
	l.mu.RUnlock()
	if force || !fresh {
		l.scan()
	}
}

func (l *Lib) metaOf(it *Item) *Meta {
	m := l.meta[it.abs]
	if m == nil {
		m = &Meta{Added: time.Now().Unix()}
		l.meta[it.abs] = m
	}
	return m
}

// Rate 给若干条打星，0 是清掉。
func (l *Lib) Rate(ids []int, star int) []Item {
	if star < 0 || star > 5 {
		return nil
	}
	l.mu.Lock()
	var out []Item
	for _, id := range ids {
		if it := l.byID[id]; it != nil {
			it.Star = star
			l.metaOf(it).Star = star
			out = append(out, *it)
		}
	}
	l.mu.Unlock()
	l.saveMeta()
	l.emit("items", out)
	return out
}

func (l *Lib) SetNote(id int, note string) error {
	note = strings.TrimSpace(note)
	l.mu.Lock()
	it := l.byID[id]
	if it == nil {
		l.mu.Unlock()
		return errors.New("gone")
	}
	it.Note = note
	l.metaOf(it).Note = note
	l.mu.Unlock()
	l.saveMeta()
	return nil
}

func (l *Lib) Played(id int) {
	l.mu.Lock()
	it := l.byID[id]
	if it == nil {
		l.mu.Unlock()
		return
	}
	it.Plays++
	l.metaOf(it).Plays = it.Plays
	l.mu.Unlock()
	l.saveMeta()
}

func (l *Lib) item(id int) *Item {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byID[id]
}

// Detail 选中一条时取它的波形与频谱，最近看过的 64 条留在内存。
func (l *Lib) Detail(id int) (*Detail, error) {
	l.dmu.Lock()
	if e, ok := l.dcache[id]; ok {
		l.dlru.MoveToFront(e)
		d := e.Value.(dent).d
		l.dmu.Unlock()
		return d, nil
	}
	l.dmu.Unlock()
	it := l.item(id)
	if it == nil {
		return nil, errors.New("gone")
	}
	const maxDetail = 30 * 60
	var x []float32
	sr := 0
	f, err := probeNative(it.abs, it.Ext)
	if err == nil {
		sr = f.SR
		x, err = readNative(it.abs, f, maxDetail)
	}
	if err != nil {
		sr = 32000
		x, _, err = ffRead(l.ff, it.abs, sr, maxDetail)
		if err != nil {
			return nil, err
		}
	}
	dur := it.Dur
	if dur == 0 && sr > 0 {
		dur = float64(len(x)) / float64(sr)
	}
	d := detail(x, sr, dur)
	l.dmu.Lock()
	l.dcache[id] = l.dlru.PushFront(dent{id, d})
	for l.dlru.Len() > 64 {
		e := l.dlru.Back()
		delete(l.dcache, e.Value.(dent).id)
		l.dlru.Remove(e)
	}
	l.dmu.Unlock()
	return d, nil
}

func (l *Lib) paths(ids []int) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []string
	for _, id := range ids {
		if it := l.byID[id]; it != nil {
			out = append(out, it.abs)
		}
	}
	return out
}
