package qqbot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 图片段解析：image/mface 都要能拿到引用。
func TestParseImages(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"image","data":{"file":"a.jpg","url":"https://x/a.jpg"}},
		{"type":"mface","data":{"file":"b.gif"}},
		{"type":"text","data":{"text":"看这个"}}
	]`)
	refs := ParseImages(raw)
	if len(refs) != 2 {
		t.Fatalf("应解析出 2 个图片引用，得到 %d: %v", len(refs), refs)
	}
	if refs[0] != "https://x/a.jpg" || refs[1] != "b.gif" {
		t.Errorf("引用不对: %v", refs)
	}
	if len(ParseImages(json.RawMessage(`"[CQ:image,file=x.jpg]"`))) != 0 {
		t.Error("字符串形态暂不解析图片，应返回空")
	}
}

// 表情包关键词匹配。
func TestMatchesSticker(t *testing.T) {
	cfg := Default()
	cfg.StickerCommand = []string{"表情", "来个表情"}
	b := New(cfg)
	if !b.matchesSticker("来个表情") || !b.matchesSticker("发个 表情 看看") {
		t.Error("应命中表情关键词")
	}
	if b.matchesSticker("你好") {
		t.Error("普通消息不该命中")
	}
}

// 被戳一戳：回戳 + 回话，两次 OneBot 调用。
func TestHandlePoke(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	ob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, r.URL.Path+" "+string(body))
		mu.Unlock()
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0}`)
	}))
	defer ob.Close()

	cfg := Default()
	cfg.OneBotAPI = ob.URL
	cfg.PokeBack = true
	cfg.PokeReply = "戳什么戳"
	b := New(cfg)
	ev := &Event{PostType: "notice", NoticeType: "poke", TargetID: 3467208422, SelfID: 3467208422,
		MessageType: "group", GroupID: 992511553, UserID: 941628696}
	b.handlePoke(ev)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("应回戳 + 回话共 2 次调用，得到 %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "/group_poke") || !strings.Contains(calls[0], "941628696") {
		t.Errorf("第一次应是 group_poke 给发起者: %s", calls[0])
	}
	if !strings.Contains(calls[1], "/send_group_msg") || !strings.Contains(calls[1], "戳什么戳") {
		t.Errorf("第二次应是群消息回话: %s", calls[1])
	}
}

// 发图：payload 里是 image 段 + 正确会话字段。
func TestSendImage(t *testing.T) {
	var got string
	ob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = r.URL.Path + " " + string(body)
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0}`)
	}))
	defer ob.Close()

	cfg := Default()
	cfg.OneBotAPI = ob.URL
	b := New(cfg)
	ev := &Event{MessageType: "group", GroupID: 992511553, UserID: 941628696}
	if err := b.sendImage(ev, "/opt/qqbot/stickers/a.png"); err != nil {
		t.Fatalf("sendImage: %v", err)
	}
	for _, want := range []string{"/send_group_msg", `"type":"image"`, "/opt/qqbot/stickers/a.png", "992511553"} {
		if !strings.Contains(got, want) {
			t.Errorf("payload 缺 %q: %s", want, got)
		}
	}
}

// 被戳但不是戳自己 → 不处理。
func TestPokeOnlyWhenTargetIsSelf(t *testing.T) {
	called := 0
	ob := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		_, _ = io.WriteString(w, `{"status":"ok","retcode":0}`)
	}))
	defer ob.Close()
	cfg := Default()
	cfg.OneBotAPI = ob.URL
	b := New(cfg)
	srv := httptest.NewServer(b.Handler())
	defer srv.Close()

	body := `{"post_type":"notice","notice_type":"poke","target_id":123456,"self_id":3467208422,
		"message_type":"group","group_id":992511553,"user_id":941628696}`
	resp, err := http.Post(srv.URL+"/onebot", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if called != 0 {
		t.Fatalf("戳别人不该触发外呼，实际 %d 次", called)
	}
}
