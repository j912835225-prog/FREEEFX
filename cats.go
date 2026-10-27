package main

// 按名字分：从文件名与它所在的各层文件夹名里认词。英文按词认（带复数、长词认词头），中文日文按字串认。

import (
	"strings"
	"unicode"
)

type cat struct {
	Name string
	En   []string
	CJK  []string
}

var cats = []cat{
	{"门", []string{"door", "gate", "latch", "knock", "hinge"}, []string{"门", "敲门", "ドア", "扉"}},
	{"脚步", []string{"footstep", "footsteps", "step", "steps", "walk", "walking", "run", "running", "foot", "feet"}, []string{"脚步", "足音", "歩"}},
	{"风", []string{"wind", "gust", "breeze", "windy"}, []string{"风", "風"}},
	{"雨", []string{"rain", "drizzle", "raindrop"}, []string{"雨"}},
	{"水", []string{"water", "splash", "drip", "river", "stream", "ocean", "sea", "wave", "bubble", "liquid", "pour", "underwater", "lake", "creek", "watr", "liqd"}, []string{"水", "滴", "浪", "海", "河", "泡"}},
	{"火", []string{"fire", "flame", "burn", "crackle", "campfire", "torch"}, []string{"火", "炎"}},
	{"雷", []string{"thunder", "lightning", "storm", "wthr"}, []string{"雷"}},
	{"爆炸", []string{"explosion", "explode", "blast", "bomb", "boom", "detonation", "expl"}, []string{"爆炸", "爆", "爆発"}},
	{"枪械", []string{"gun", "gunshot", "rifle", "pistol", "shotgun", "bullet", "reload", "firearm"}, []string{"枪", "銃"}},
	{"金属", []string{"metal", "metallic", "steel", "iron", "clank", "chain", "tin", "metl"}, []string{"金属", "铁", "鉄"}},
	{"玻璃", []string{"glass", "shatter", "bottle", "glas"}, []string{"玻璃", "ガラス"}},
	{"木", []string{"wood", "wooden", "plank", "branch", "stick"}, []string{"木"}},
	{"布与纸", []string{"cloth", "fabric", "paper", "rustle", "clothes", "page", "book", "clth", "papr"}, []string{"布", "纸", "紙", "衣"}},
	{"人声", []string{"voice", "vocal", "breath", "breathing", "scream", "laugh", "crowd", "whisper", "cough", "shout", "sigh", "talk", "speech", "dialog", "dialogue", "vox", "walla", "crwd"}, []string{"人声", "呼吸", "笑", "叫", "喊", "人群", "说话", "台词", "声優", "ボイス"}},
	{"动物", []string{"dog", "cat", "bird", "birds", "horse", "cow", "animal", "insect", "cricket", "crow", "owl", "wolf", "frog", "bee", "fly", "chicken", "pig", "sheep", "anml"}, []string{"狗", "猫", "鸟", "鳥", "犬", "虫", "马", "馬", "牛", "蛙", "動物", "动物"}},
	{"交通", []string{"car", "engine", "vehicle", "train", "plane", "airplane", "aircraft", "motorcycle", "traffic", "horn", "truck", "bus", "bike", "helicopter", "tire", "brake", "vehi"}, []string{"车", "汽车", "火车", "飞机", "車", "電車", "飛行機"}},
	{"机械", []string{"machine", "motor", "mechanical", "servo", "gear", "robot", "hydraulic", "pneumatic", "factory", "mach", "mech"}, []string{"机械", "機械", "马达", "齿轮"}},
	{"电子与界面", []string{"ui", "click", "button", "beep", "notification", "interface", "menu", "alert", "blip", "tone", "electric", "electronic", "glitch", "buzz"}, []string{"界面", "按钮", "提示音", "电子", "電子", "クリック"}},
	{"冲击", []string{"impact", "hit", "punch", "slam", "thud", "crash", "smash", "kick", "drop", "fall", "body"}, []string{"撞", "打击", "冲击", "衝撃", "落"}},
	{"呼啸", []string{"whoosh", "swoosh", "swish", "swipe", "woosh", "pass", "passby", "flyby", "riser", "whsh", "swsh"}, []string{"呼啸", "嗖", "风声"}},
	{"环境", []string{"ambience", "ambient", "amb", "atmos", "atmosphere", "roomtone", "room", "background", "bg"}, []string{"环境", "氛围", "環境", "底噪"}},
	{"城市", []string{"city", "street", "urban", "market", "station", "cafe", "restaurant", "office"}, []string{"城市", "街", "都市", "市场", "车站"}},
	{"自然", []string{"nature", "forest", "jungle", "birdsong", "field", "meadow", "mountain", "night", "insects"}, []string{"森林", "自然", "山", "野", "夜"}},
	{"魔幻科幻", []string{"magic", "spell", "scifi", "sci", "laser", "alien", "spaceship", "portal", "energy", "fantasy", "monster", "creature", "magc", "lasr", "scfi"}, []string{"魔法", "科幻", "怪物", "能量"}},
	{"乐器", []string{"piano", "guitar", "drum", "drums", "synth", "bass", "string", "strings", "violin", "cello", "pad", "kick", "snare", "hihat", "percussion", "bell", "chime"}, []string{"鼓", "钢琴", "吉他", "弦", "钟", "鈴", "铃"}},
	{"循环", []string{"loop", "loops", "loopable", "seamless"}, []string{"循环", "ループ"}},
}

// words 把一串名字切成英文小写词：非字母数字断开，驼峰断开，字母与数字断开。
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r)) {
			flush()
			continue
		}
		if len(cur) > 0 {
			p := rs[i-1]
			// 大写串接首字母大写的词也断开：UCS 的 CatID「DOORWood」认成 door、wood
			capRun := unicode.IsUpper(p) && unicode.IsUpper(r) && i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if (unicode.IsLower(p) && unicode.IsUpper(r)) || (unicode.IsDigit(p) != unicode.IsDigit(r)) || capRun {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

var enIndex = func() map[string][]int {
	m := map[string][]int{}
	for i, c := range cats {
		for _, w := range c.En {
			m[w] = append(m[w], i)
		}
	}
	return m
}()

func match(w string) []int {
	if c, ok := enIndex[w]; ok {
		return c
	}
	for _, suf := range []string{"es", "s", "ing", "ed"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			if c, ok := enIndex[w[:len(w)-len(suf)]]; ok {
				return c
			}
		}
	}
	return nil
}

// classify 给一条声音认类目，返回 cats 的下标。
func classify(name string) []int {
	seen := map[int]bool{}
	out := []int{} // 空也给 []，页面不必判 null
	add := func(i int) {
		if !seen[i] {
			seen[i] = true
			out = append(out, i)
		}
	}
	for _, w := range words(name) {
		for _, i := range match(w) {
			add(i)
		}
	}
	for i, c := range cats {
		for _, s := range c.CJK {
			if strings.Contains(name, s) {
				add(i)
				break
			}
		}
	}
	return out
}

func catNames() []string {
	out := make([]string, len(cats))
	for i, c := range cats {
		out[i] = c.Name
	}
	return out
}
