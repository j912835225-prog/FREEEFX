//go:build windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"syscall"
	"testing"
	"unsafe"
)

// 照 Pro Tools／达芬奇这类接收方的读法：向拖出的数据要 CF_HDROP（HGLOBAL），再用 DragQueryFileW 拆出路径。
// 两个文件放在不同夹、名字带中文和空格。
func TestShellDataHDrop(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procOleInitialize.Call(0)
	d := longPath(t.TempDir()) // 临时夹常是短名（ADMINI~1），外壳交回的是长名
	want := []string{filepath.Join(d, "E音效", "雨 夜 01.wav"), filepath.Join(d, "导出", "片段", "长 声 0m01.00s.wav")}
	for _, p := range want {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("RIFF"), 0o644)
	}
	obj, err := shellData(want)
	if err != nil {
		t.Fatal(err)
	}
	defer comRelease(obj)
	type formatEtc struct {
		Format uint16
		_      uintptr
		Aspect uint32
		Index  int32
		Tymed  uint32
	}
	type stgMedium struct {
		Tymed  uint32
		Handle uintptr
		Unk    uintptr
	}
	f := formatEtc{Format: 15, Aspect: 1, Index: -1, Tymed: 1} // CF_HDROP, DVASPECT_CONTENT, TYMED_HGLOBAL
	var m stgMedium
	vt := (*[4]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(obj))))
	if hr, _, _ := syscall.SyscallN(vt[3], obj, uintptr(unsafe.Pointer(&f)), uintptr(unsafe.Pointer(&m))); int32(hr) < 0 || m.Tymed != 1 {
		t.Fatalf("GetData CF_HDROP hr=%#x tymed=%d", hr, m.Tymed)
	}
	defer ole32.NewProc("ReleaseStgMedium").Call(uintptr(unsafe.Pointer(&m)))
	q := shell32.NewProc("DragQueryFileW")
	n, _, _ := q.Call(m.Handle, 0xFFFFFFFF, 0, 0)
	var got []string
	for i := uintptr(0); i < n; i++ {
		size, _, _ := q.Call(m.Handle, i, 0, 0)
		buf := make([]uint16, size+1)
		q.Call(m.Handle, i, uintptr(unsafe.Pointer(&buf[0])), size+1)
		got = append(got, syscall.UTF16ToString(buf))
	}
	t.Logf("DragQueryFileW 读到 %d 条: %q", len(got), got)
	if !slices.Equal(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}

func longPath(p string) string {
	u, _ := syscall.UTF16PtrFromString(p)
	buf := make([]uint16, 1024)
	n, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetLongPathNameW").Call(uintptr(unsafe.Pointer(u)), uintptr(unsafe.Pointer(&buf[0])), 1024)
	if n == 0 {
		return p
	}
	return syscall.UTF16ToString(buf[:n])
}
