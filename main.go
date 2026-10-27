// FREEEFX：管音效与音乐的桌面 App。只读人拖进来或载入的文件夹与文件；
// 打分、批注记在 App 的数据夹，转格式与截段的产物放进导出夹（默认 音乐/FREEEFX）。本 App 不联网、不下载、无打印功能。
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/egoist/mygo"
)

//go:embed web
var web embed.FS

// 推给页面的事件。lib：库重排了，页面重取；items：一批条目有了新数；progress：分析或转换进度。
var (
	evLib      = mygo.NewEvent[int]("lib")
	evItems    = mygo.NewEvent[[]Item]("items")
	evProgress = mygo.NewEvent[Progress]("progress")
)

func appEmit(name string, v any) {
	switch name {
	case "lib":
		evLib.Broadcast(v.(int))
	case "items":
		evItems.Broadcast(v.([]Item))
	case "progress":
		evProgress.Broadcast(v.(Progress))
	}
}

// findFF 找随包的 ffmpeg：打包后在 Resources/bin，go run／go test 时在 resources/<系统-架构>/bin。
func findFF(res string) string {
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name = "ffmpeg.exe"
	}
	for _, p := range []string{
		filepath.Join(res, "bin", name),
		filepath.Join(res, runtime.GOOS+"-"+runtime.GOARCH, "bin", name),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// App 是页面调的壳：库之外，凡要窗口的（对话框、右键菜单）在这里。
type App struct {
	lib *Lib
	win *mygo.Window
}

// Pick 载入：dir 为真选文件夹，否则选音频文件（可多选）。
func (a *App) Pick(ctx context.Context, dir bool) (int, error) {
	opt := mygo.OpenDialogOptions{Parent: mygo.CallerWindow(ctx), Title: "载入", Multiple: true, Directory: dir}
	if !dir {
		var exts []string
		for e := range audioExt {
			exts = append(exts, e)
		}
		opt.Filters = []mygo.FileFilter{{Name: "音频", Extensions: exts}}
	}
	got, err := mygo.Dialog.Open(opt)
	if err != nil || len(got) == 0 {
		return 0, err
	}
	return a.lib.Add(got), nil
}

// Menu 在鼠标处弹原生右键菜单，对选中的这些条目。
func (a *App) Menu(ctx context.Context, ids []int) {
	win := mygo.CallerWindow(ctx)
	mygo.NewMenu(a.lib.menu(ids, func(s string) { evToast.Broadcast(s) })).Popup(win)
}

var evToast = mygo.NewEvent[string]("toast")

// draggingOut 本窗口正往外拖时为真（拖出去又松回本窗口，窗口也会报一次落下）；droppedSelf 记下这种松回。
var draggingOut, droppedSelf atomic.Bool

// PickOut 换导出夹；返回新的导出夹，取消就原样。
func (a *App) PickOut(ctx context.Context) (string, error) {
	got, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: mygo.CallerWindow(ctx), Title: "导出夹", Directory: true, CreateDirectories: true})
	if err != nil || len(got) == 0 {
		return a.lib.outDir(), err
	}
	a.lib.SetOut(got[0])
	return a.lib.outDir(), nil
}

// Drag 把选中的几条拖出窗口（进达芬奇、Pro Tools、访达）。页面在按住鼠标拖动时调。
func (a *App) Drag(ctx context.Context, ids []int) error {
	return dragFiles(mygo.CallerWindow(ctx), a.lib.paths(ids), nil)
}

// DragClip 先把 a–b 秒截成一段，再拖出这一段。放到别处才留下进库；Esc、拖回本窗口算反悔，截的删掉。
func (a *App) DragClip(ctx context.Context, id int, from, to float64) error {
	p, err := a.lib.cut(id, from, to)
	if err != nil {
		return err
	}
	err = dragFiles(mygo.CallerWindow(ctx), []string{p}, func(dropped bool) {
		if dropped {
			a.lib.keepOut()
			return
		}
		os.Remove(p)
		os.Remove(filepath.Dir(p)) // 片段夹空了顺手收掉，不空删不动
	})
	if err != nil {
		os.Remove(p)
	}
	return err
}

// Theme 页面的明暗钮：窗口（连同侧栏材质）跟着换，auto 跟系统。
func (a *App) Theme(t string) {
	switch t {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// ShowOut 在访达里打开导出夹，没有就先建。
func (a *App) ShowOut() error {
	d := a.lib.outDir()
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	return mygo.Shell.OpenPath(d)
}

func trashLabel() string {
	if runtime.GOOS == "darwin" {
		return "移到废纸篓"
	}
	return "移到回收站"
}

// Trash 删副本：只删导出夹里截出、转出的；选中里有原件就一个不删。
func (a *App) Trash(ids []int) (int, error) {
	ps := a.lib.copies(ids)
	if len(ps) == 0 {
		return 0, errors.New("原件不删，只有导出夹里的副本能删")
	}
	return a.lib.trash(ps, mygo.Shell.TrashItem), nil
}

func reveal() string {
	if runtime.GOOS == "darwin" {
		return "在访达中显示"
	}
	return "在资源管理器中显示"
}

// menu 搭右键菜单；单列出来，go test 能逐项点。
func (l *Lib) menu(ids []int, toast func(string)) []*mygo.MenuItem {
	n := len(ids)
	if n == 0 {
		return nil
	}
	stars := []*mygo.MenuItem{}
	for s := 5; s >= 1; s-- {
		s := s
		stars = append(stars, &mygo.MenuItem{Label: strings.Repeat("★", s) + strings.Repeat("☆", 5-s), Click: func(*mygo.MenuItem, *mygo.Window) { go l.Rate(ids, s) }})
	}
	stars = append(stars, &mygo.MenuItem{Type: mygo.MenuItemSeparator}, &mygo.MenuItem{Label: "清掉星", Click: func(*mygo.MenuItem, *mygo.Window) { go l.Rate(ids, 0) }})
	items := []*mygo.MenuItem{{Label: "打分", Submenu: stars}}
	if l.ff != "" {
		conv := []*mygo.MenuItem{}
		for _, f := range formats {
			f := f
			conv = append(conv, &mygo.MenuItem{Label: f.Label, Click: func(*mygo.MenuItem, *mygo.Window) {
				go func() {
					r, err := l.Convert(ids, f.ID)
					switch {
					case err != nil:
						toast(err.Error())
					case len(r.Failed) > 0:
						toast(itoa(r.Done) + " 个转好，" + itoa(len(r.Failed)) + " 个没转成：" + r.Failed[0])
					default:
						toast(itoa(r.Done) + " 个转成 " + f.Label + "，放进导出夹")
					}
				}()
			}})
		}
		label := "转成"
		if n > 1 {
			label = "这 " + itoa(n) + " 个转成"
		}
		items = append(items, &mygo.MenuItem{Label: label, Submenu: conv})
	}
	items = append(items,
		&mygo.MenuItem{Type: mygo.MenuItemSeparator},
		&mygo.MenuItem{Label: reveal(), Click: func(*mygo.MenuItem, *mygo.Window) {
			if p := l.paths(ids); len(p) > 0 {
				mygo.Shell.ShowItemInFolder(p[0])
			}
		}},
		&mygo.MenuItem{Label: "拷贝文件", Click: func(*mygo.MenuItem, *mygo.Window) {
			if err := mygo.Clipboard.WriteFiles(l.paths(ids)...); err == nil {
				toast("拷好了，去访达或剪辑软件里粘贴")
			}
		}},
		&mygo.MenuItem{Label: "拷贝路径", Click: func(*mygo.MenuItem, *mygo.Window) {
			mygo.Clipboard.WriteText(strings.Join(l.paths(ids), "\n"))
		}},
	)
	if ps := l.copies(ids); len(ps) > 0 { // 原件不给这一项
		items = append(items, &mygo.MenuItem{Type: mygo.MenuItemSeparator}, &mygo.MenuItem{Label: trashLabel(), Click: func(*mygo.MenuItem, *mygo.Window) {
			go func() { toast(itoa(l.trash(ps, mygo.Shell.TrashItem)) + " 个副本已" + trashLabel()) }()
		}})
	}
	return items
}

func itoa(n int) string { return strconv.Itoa(n) }

func main() {
	serve := flag.String("serve", "", "不开窗口，在这个地址起 HTTP 供测试，如 127.0.0.1:8549")
	dataDir := flag.String("data", "", "-serve 时的数据夹")
	webDir := flag.String("web", "", "-serve 时从这个夹读页面（改页面不必重编）")
	flag.Parse()
	page, _ := fs.Sub(web, "web")
	if *serve != "" {
		if *webDir != "" {
			page = os.DirFS(*webDir)
		}
		runServe(*serve, *dataDir, page)
		return
	}
	mygo.SetFrontend(page)
	app := &App{}
	lib := &Lib{} // 先占位，WhenReady 里换成真的；Bind 要在 Run 之前
	app.lib = lib
	mygo.Bind(lib)
	mygo.Bind(app)
	mygo.Protocol.HandleFunc("snd", func(w2 http.ResponseWriter, r *http.Request) { app.lib.serveSnd(w2, r) })
	mygo.App.WhenReady(func() {
		data, err := mygo.App.Path(mygo.PathUserData)
		if err != nil {
			log.Fatal(err)
		}
		os.MkdirAll(data, 0o755)
		res, _ := mygo.App.Path(mygo.PathResources)
		lib.open(data, findFF(res), appEmit)
		go lib.scan()
		opt := mygo.WindowOptions{URL: "/", Title: "FREEEFX", Width: 1280, Height: 820, MinWidth: 760, MinHeight: 480, BackgroundColor: "light-dark(#fbfbfa, #151515)",
			TitleBarStyle: mygo.TitleBarHiddenInset, TitleBarHeight: 52, TrafficLightPosition: &mygo.Point{X: 18, Y: 19}} // 红绿灯居中在 52px 顶栏
		if runtime.GOOS == "darwin" { // 侧栏毛玻璃；Windows 10 没有 Mica，侧栏照旧实色
			opt.Transparent, opt.Vibrancy = true, mygo.VibrancySidebar
		}
		app.win = mygo.NewWindow(opt)
		app.win.OnFileDrop(func(e *mygo.FileDropEvent) {
			if draggingOut.Load() {
				droppedSelf.Store(true)
				return
			}
			go lib.Add(e.Paths)
		})
		app.win.OnFocus(func() { go lib.Rescan(false) })
		app.win.Page().OnWillNavigate(func(e *mygo.NavigateEvent) {
			if u, err := url.Parse(e.URL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && !strings.HasSuffix(u.Hostname(), ".localhost") && e.UserInitiated {
				e.PreventDefault()
			}
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
