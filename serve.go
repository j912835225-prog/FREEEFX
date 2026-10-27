package main

// -serve：不开窗口，把同一套 Lib 挂在 HTTP 上（/api/方法名、/events、/snd/），供无头浏览器压测与验收。
// 只听 127.0.0.1。窗口专有的（对话框、原生菜单）不在这里。

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
)

type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]bool
}

func (h *hub) emit(name string, v any) {
	b, _ := json.Marshal(v)
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", name, b))
	h.mu.Lock()
	for c := range h.subs {
		select {
		case c <- msg:
		default:
		}
	}
	h.mu.Unlock()
}

func runServe(addr, data string, page fs.FS) {
	if data == "" {
		log.Fatal("-serve 要配 -data")
	}
	os.MkdirAll(data, 0o755)
	h := &hub{subs: map[chan []byte]bool{}}
	wd, _ := os.Getwd()
	l := newLib(data, findFF(wd+"/resources"), h.emit)
	go l.scan()
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(page))
	mux.Handle("/snd/", http.StripPrefix("/snd", http.HandlerFunc(l.serveSnd)))
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		c := make(chan []byte, 256)
		h.mu.Lock()
		h.subs[c] = true
		h.mu.Unlock()
		defer func() { h.mu.Lock(); delete(h.subs, c); h.mu.Unlock() }()
		fl := w.(http.Flusher)
		fmt.Fprint(w, ": hi\n\n")
		fl.Flush()
		for {
			select {
			case m := <-c:
				w.Write(m)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	lv := reflect.ValueOf(l)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/")
		name = strings.TrimPrefix(name, "Lib.")
		m := lv.MethodByName(name)
		if !m.IsValid() || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var raw []json.RawMessage
		if len(body) > 0 {
			if err := json.Unmarshal(body, &raw); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
		}
		t := m.Type()
		args := make([]reflect.Value, t.NumIn())
		for i := range args {
			p := reflect.New(t.In(i))
			if i < len(raw) {
				if err := json.Unmarshal(raw[i], p.Interface()); err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
			}
			args[i] = p.Elem()
		}
		out := m.Call(args)
		if n := len(out); n > 0 {
			if e, ok := out[n-1].Interface().(error); ok && e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			if out[n-1].Type() == reflect.TypeFor[error]() {
				out = out[:n-1]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if len(out) == 0 {
			w.Write([]byte("null"))
			return
		}
		json.NewEncoder(w).Encode(out[0].Interface())
	})
	if !strings.HasPrefix(addr, "127.0.0.1:") {
		log.Fatal("-serve 只听 127.0.0.1")
	}
	log.Printf("serve http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
