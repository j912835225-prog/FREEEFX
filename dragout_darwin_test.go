//go:build darwin

package main

import (
	"testing"

	"github.com/ebitengine/purego/objc"
)

// 拖完的回报：Esc（op 0）、松回本窗口都算没放；真放到别处才算。
func TestDragEndReportsDrop(t *testing.T) {
	dragOnce.Do(dragInit)
	if dragErr != nil {
		t.Skip(dragErr)
	}
	var ended func(objc.ID, objc.SEL, objc.ID, nsPoint, uint)
	registerLike(&ended)
	end := func(op uint, self bool) bool {
		ch := make(chan bool, 1)
		dragWin, dragDone = 0, func(ok bool) { ch <- ok }
		draggingOut.Store(true)
		droppedSelf.Store(self)
		ended(dragSource, sel("draggingSession:endedAtPoint:operation:"), 0, nsPoint{10, 10}, op)
		if draggingOut.Load() {
			t.Error("draggingOut left on")
		}
		return <-ch
	}
	if end(0, false) || end(1, true) || !end(1, false) {
		t.Fatal("wrong verdict")
	}
}
