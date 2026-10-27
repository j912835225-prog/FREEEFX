//go:build windows

package main

// 拖出：把库里的声音拖进达芬奇、Pro Tools、资源管理器。MyGo 的网页窗口不带原生拖出（StartDataDrag 只给原生 UI 窗口，
// 网页窗口会报「a data drag requires native UI」），这里直接跟 Windows 外壳说：拿这几个文件的外壳数据对象
// （和从资源管理器里拖文件时的那个是同一种，带 CF_HDROP），交给 SHDoDragDrop 起一次拖拽，只给「拷贝」。

import (
	"errors"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo"
)

var (
	shell32            = syscall.NewLazyDLL("shell32.dll")
	ole32              = syscall.NewLazyDLL("ole32.dll")
	user32             = syscall.NewLazyDLL("user32.dll")
	procParseName      = shell32.NewProc("SHParseDisplayName")
	procCreateDataObj  = shell32.NewProc("SHCreateDataObject")
	procSHDoDragDrop   = shell32.NewProc("SHDoDragDrop")
	procOleInitialize  = ole32.NewProc("OleInitialize")
	procCoTaskMemFree  = ole32.NewProc("CoTaskMemFree")
	procReleaseCapture = user32.NewProc("ReleaseCapture")
)

var iidIDataObject = syscall.GUID{Data1: 0x0000010E, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}

const (
	dropEffectCopy = 1
	dragDropSDrop  = 0x00040100 // DRAGDROP_S_DROP：真放下了
)

// shellData 给这几个文件要一个外壳数据对象。用完调 comRelease。
func shellData(paths []string) (uintptr, error) {
	pidls := make([]uintptr, 0, len(paths))
	defer func() {
		for _, p := range pidls {
			procCoTaskMemFree.Call(p)
		}
	}()
	for _, p := range paths {
		u, err := syscall.UTF16PtrFromString(p)
		if err != nil {
			return 0, err
		}
		var pidl uintptr
		if hr, _, _ := procParseName.Call(uintptr(unsafe.Pointer(u)), 0, uintptr(unsafe.Pointer(&pidl)), 0, 0); int32(hr) < 0 || pidl == 0 {
			return 0, errors.New("找不到文件：" + p)
		}
		pidls = append(pidls, pidl)
	}
	// 父夹是桌面（空 IDLIST），各文件用完整路径的 IDLIST，所以不同夹里的文件也能一起拖
	desktop := [2]byte{}
	var obj uintptr
	if hr, _, _ := procCreateDataObj.Call(uintptr(unsafe.Pointer(&desktop)), uintptr(len(pidls)), uintptr(unsafe.Pointer(&pidls[0])), 0,
		uintptr(unsafe.Pointer(&iidIDataObject)), uintptr(unsafe.Pointer(&obj))); int32(hr) < 0 || obj == 0 {
		return 0, errors.New("拿不到拖拽数据")
	}
	return obj, nil
}

func comRelease(obj uintptr) {
	vt := *(*[3]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	syscall.SyscallN(vt[2], obj)
}

// done 非空时，拖完告诉它是不是真放到了别处（放回本窗口、Esc 都算没放）。
func dragFiles(win *mygo.Window, paths []string, done func(dropped bool)) error {
	if len(paths) == 0 {
		return errors.New("没有可拖的")
	}
	if win == nil {
		return errors.New("没有窗口")
	}
	var err error
	mygo.RunOnMain(func() {
		procOleInitialize.Call(0) // 已初始化过的返回 S_FALSE，无妨
		var obj uintptr
		if obj, err = shellData(paths); err != nil {
			return
		}
		defer comRelease(obj)
		draggingOut.Store(true) // 拖出去又松回本窗口的，不当成载入
		droppedSelf.Store(false)
		defer draggingOut.Store(false)
		procReleaseCapture.Call() // 拖拽期间鼠标归 OLE
		var effect uint32
		// 拖拽源给空，外壳用自带的：松手放下、Esc 取消。这里会一直等到松手才返回。
		hr, _, _ := procSHDoDragDrop.Call(win.NativeHandle(), obj, 0, dropEffectCopy, uintptr(unsafe.Pointer(&effect)))
		if int32(hr) < 0 {
			err = errors.New("拖拽没起来")
			return
		}
		if done != nil {
			go done(hr == dragDropSDrop && effect != 0 && !droppedSelf.Load())
		}
	})
	return err
}
