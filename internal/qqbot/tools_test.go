package qqbot

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const sampleSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256">
  <rect x="0" y="0" width="256" height="256" fill="#1b2330"/>
  <circle cx="128" cy="110" r="70" fill="#4a7bd6"/>
  <circle cx="100" cy="95" r="12" fill="#fff"/>
  <circle cx="156" cy="95" r="12" fill="#fff"/>
  <path d="M90 150 Q128 180 166 150" stroke="#fff" stroke-width="8" fill="none"/>
</svg>`

// SVG → PNG：产物必须是合法 PNG（QQ 不收 SVG，必须光栅化）。
func TestRenderSVG(t *testing.T) {
	out, err := RenderSVG(sampleSVG, 128)
	if err != nil {
		t.Fatalf("RenderSVG: %v", err)
	}
	if len(out) < 100 {
		t.Fatalf("PNG 太小: %d 字节", len(out))
	}
	if !bytes.HasPrefix(out, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("不是 PNG: % x", out[:8])
	}
}

// 异常输入要报错而不是 panic/挂死。
func TestRenderSVGGuards(t *testing.T) {
	if _, err := RenderSVG("", 128); err == nil {
		t.Error("空 SVG 应报错")
	}
	if _, err := RenderSVG("not svg", 128); err == nil {
		t.Error("非 SVG 应报错")
	}
	if _, err := RenderSVG("<svg>"+strings.Repeat("x", maxSVGBytes)+"</svg>", 128); err == nil {
		t.Error("超大 SVG 应报错")
	}
	// 畸形但带 <svg> 的输入：不能 panic
	if _, err := RenderSVG("<svg><path d=\"M0 0 L", 128); err == nil {
		t.Log("畸形 SVG 被接受了（渲染库容错），不算失败")
	}
	// 超大画布要被夹紧
	out, err := RenderSVG(sampleSVG, 4000)
	if err != nil {
		t.Fatalf("夹紧后应能渲染: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("空结果")
	}
}

// 工具注册表：draw_sticker 应渲染并发送图片。
func TestToolDrawSticker(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	ob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, r.URL.Path+" "+string(body))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0}`)
	}))
	defer ob.Close()

	cfg := Default()
	cfg.OneBotAPI = ob.URL
	cfg.StickerDir = t.TempDir()
	cfg.ProfileEnabled = false
	b := New(cfg)
	tools := b.buildTools()
	ev := &Event{MessageType: "private", UserID: 941628696, SelfID: 3467208422}

	args, _ := json.Marshal(map[string]string{"svg": sampleSVG})
	res, err := tools.exec(toolContext{ev: ev, b: b}, ToolCall{
		ID: "c1", Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "draw_sticker", Arguments: string(args)},
	})
	if err != nil {
		t.Fatalf("draw_sticker: %v", err)
	}
	if !strings.Contains(res, "已把自绘表情包") {
		t.Errorf("返回文案不对: %s", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], "/send_private_msg") || !strings.Contains(sent[0], `"type":"image"`) {
		t.Fatalf("应发出一条图片消息: %v", sent)
	}
}

// 未知工具要报错（白名单之外一律不执行）。
func TestToolUnknown(t *testing.T) {
	b := New(Default())
	tools := b.buildTools()
	_, err := tools.exec(toolContext{ev: &Event{}, b: b}, ToolCall{
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "rm_rf", Arguments: "{}"},
	})
	if err == nil {
		t.Fatal("未知工具应报错")
	}
}

// 工具循环：第一轮返回 tool_calls，第二轮返回正文 → 最终答案取正文。
func TestChatToolLoop(t *testing.T) {
	var mu sync.Mutex
	round := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		round++
		cur := round
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if cur == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"","tool_calls":[
				{"id":"c1","type":"function","function":{"name":"remember_note","arguments":"{\"note\":\"他喜欢鲸鱼娘\"}"}}]}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"记住了，蠢货。"}}]}`)
	}))
	defer gw.Close()

	cfg := Default()
	cfg.GatewayBase = gw.URL
	cfg.MemoryFile = ""
	cfg.ProfileEnabled = false
	b := New(cfg)
	ev := &Event{MessageType: "private", UserID: 941628696, SelfID: 3467208422}
	ans, err := b.chat("private:941628696", ev, "记住我喜欢鲸鱼娘", nil, "", nil)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(ans, "记住了") {
		t.Errorf("最终答案应取第二轮正文，得到 %q", ans)
	}
	// 工具副作用：笔记进了记忆
	if u, ok := b.mem.Lookup(941628696); !ok || len(u.Notes) == 0 {
		t.Errorf("remember_note 应写入记忆: %+v", u)
	}
	if round < 2 {
		t.Errorf("应发生至少两轮请求，实际 %d", round)
	}
}
