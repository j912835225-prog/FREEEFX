//go:build darwin

package main

// 拖出：把库里的声音拖进达芬奇、Pro Tools、访达。MyGo 的网页窗口不带原生拖出，这里直接跟 AppKit 说：
// 在窗口的 contentView 上起一次拖拽，每个文件一个 NSDraggingItem，写的是文件 URL，只给「拷贝」。

import (
	"errors"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

type nsPoint struct{ X, Y float64 }
type nsSize struct{ W, H float64 }
type nsRect struct {
	O nsPoint
	S nsSize
}

var (
	dragOnce   sync.Once
	dragErr    error
	dragSource objc.ID
	msgPt      func(objc.ID, objc.SEL) nsPoint
	msgPtView  func(objc.ID, objc.SEL, nsPoint, objc.ID) nsPoint
	msgFrame   func(objc.ID, objc.SEL, nsRect, objc.ID)
	msgSize    func(objc.ID, objc.SEL, nsSize)
	msgF64     func(objc.ID, objc.SEL) float64
	msgWinAt   func(objc.ID, objc.SEL, nsPoint, int) int
	dragWin    objc.ID    // 正在拖出的窗口，只在主线程上读写
	dragDone   func(bool) // 同上
	msgMouseEv func(objc.ID, objc.SEL, uint, nsPoint, uint, float64, int, objc.ID, int, int, float32) objc.ID
)

var msgSendAddr uintptr

func registerLike(f any) { purego.RegisterFunc(f, msgSendAddr) }

func sel(s string) objc.SEL { return objc.RegisterName(s) }
func cls(s string) objc.ID  { return objc.ID(objc.GetClass(s)) }

func dragInit() {
	lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		dragErr = err
		return
	}
	// 窗口里 MyGo 早已载入 AppKit；单测里没有，补一次也无妨
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		dragErr = err
		return
	}
	send, err := purego.Dlsym(lib, "objc_msgSend")
	if err != nil {
		dragErr = err
		return
	}
	msgSendAddr = send
	for _, f := range []any{&msgPt, &msgPtView, &msgFrame, &msgSize, &msgF64, &msgWinAt, &msgMouseEv} {
		registerLike(f)
	}
	c, err := objc.RegisterClass("FreeefxDragSource", objc.GetClass("NSObject"), []*objc.Protocol{objc.GetProtocol("NSDraggingSource")}, nil, []objc.MethodDef{
		{Cmd: sel("draggingSession:sourceOperationMaskForDraggingContext:"), Fn: func(self objc.ID, _ objc.SEL, session objc.ID, ctx int) uint { return 1 }},
		{Cmd: sel("draggingSession:endedAtPoint:operation:"), Fn: func(self objc.ID, _ objc.SEL, session objc.ID, p nsPoint, op uint) {
			draggingOut.Store(false)
			f := dragDone
			dragDone = nil
			if f == nil {
				return
			}
			// 松在本窗口上也算反悔：看松手处最上面那扇窗是不是自己（压在上面的达芬奇、访达不算）。
			// 网页的落下回调要晚一拍才到，这里不能只靠 droppedSelf
			back := droppedSelf.Load()
			if dragWin != 0 {
				back = back || msgWinAt(cls("NSWindow"), sel("windowNumberAtPoint:belowWindowWithWindowNumber:"), p, 0) == int(dragWin.Send(sel("windowNumber")))
			}
			go f(op != 0 && !back)
		}},
	})
	if err != nil {
		dragErr = err
		return
	}
	dragSource = objc.ID(c).Send(sel("alloc")).Send(sel("init"))
}

// dragFiles 必须在页面按住鼠标拖动的那一刻调：AppKit 要一个「正按着」的鼠标事件起拖。
// done 非空时，拖完告诉它是不是真放到了别处（放回本窗口、Esc 都算没放）。
func dragFiles(win *mygo.Window, paths []string, done func(dropped bool)) error {
	if len(paths) == 0 {
		return errors.New("没有可拖的")
	}
	if win == nil {
		return errors.New("没有窗口")
	}
	dragOnce.Do(dragInit)
	if dragErr != nil {
		return dragErr
	}
	var err error
	mygo.RunOnMain(func() {
		nswin := objc.ID(win.NativeHandle())
		view := nswin.Send(sel("contentView"))
		if view == 0 {
			err = errors.New("没有内容视图")
			return
		}
		pool := cls("NSAutoreleasePool").Send(sel("alloc")).Send(sel("init"))
		defer pool.Send(sel("drain"))

		ev := cls("NSApplication").Send(sel("sharedApplication")).Send(sel("currentEvent"))
		if t := uint(0); ev != 0 {
			t = uint(ev.Send(sel("type")))
			if t != 1 && t != 6 { // 不是按下也不是拖动，就照当下鼠标位置合成一个拖动事件
				ev = 0
			}
		}
		if ev == 0 {
			loc := msgPt(nswin, sel("mouseLocationOutsideOfEventStream"))
			ts := msgF64(cls("NSProcessInfo").Send(sel("processInfo")), sel("systemUptime"))
			num := int(nswin.Send(sel("windowNumber")))
			ev = msgMouseEv(cls("NSEvent"), sel("mouseEventWithType:location:modifierFlags:timestamp:windowNumber:context:eventNumber:clickCount:pressure:"), 6, loc, 0, ts, num, 0, 0, 1, 1)
		}
		p := msgPtView(view, sel("convertPoint:fromView:"), msgPt(ev, sel("locationInWindow")), 0)
		ws := cls("NSWorkspace").Send(sel("sharedWorkspace"))
		items := cls("NSMutableArray").Send(sel("array"))
		for i, path := range paths {
			s := cls("NSString").Send(sel("stringWithUTF8String:"), path)
			url := cls("NSURL").Send(sel("fileURLWithPath:"), s)
			di := cls("NSDraggingItem").Send(sel("alloc")).Send(sel("initWithPasteboardWriter:"), url).Send(sel("autorelease"))
			icon := ws.Send(sel("iconForFile:"), s)
			msgSize(icon, sel("setSize:"), nsSize{32, 32})
			off := float64(min(i, 4) * 3)
			msgFrame(di, sel("setDraggingFrame:contents:"), nsRect{nsPoint{p.X - 16 + off, p.Y - 16 - off}, nsSize{32, 32}}, icon)
			items.Send(sel("addObject:"), di)
		}
		session := view.Send(sel("beginDraggingSessionWithItems:event:source:"), items, ev, dragSource)
		if session == 0 {
			err = errors.New("AppKit 没起拖")
			return
		}
		dragWin, dragDone = nswin, done
		draggingOut.Store(true)
		droppedSelf.Store(false)
		session.Send(sel("setAnimatesToStartingPositionsOnCancelOrFail:"), true)
	})
	return err
}
