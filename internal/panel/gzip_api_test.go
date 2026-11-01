package panel

// gzip_api_test.go /panel/api/* 压缩的回归：大响应要压、小响应和非 API 路径不压。

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gzipHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func TestGzipAPILargeResponse(t *testing.T) {
	body := `{"entries":[` + strings.Repeat(`{"a":"重复内容重复内容"},`, 200) + `{}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/panel/api/request_logs?limit=600", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	withGzipAPI(gzipHandler(body)).ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding=%q want gzip（%d 字节响应应被压缩）", got, len(body))
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("应带 Vary: Accept-Encoding")
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != body {
		t.Error("解压后内容与原文不一致")
	}
	if rec.Body.Len() >= len(body) {
		t.Errorf("压缩后 %d 字节，不比原文 %d 小", rec.Body.Len(), len(body))
	}
}

func TestGzipAPISkipsSmallAndNonAPI(t *testing.T) {
	// 小响应：不压（省不了字节还多几个头）
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/panel/api/overview", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	withGzipAPI(gzipHandler(`{"ok":true}`)).ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Error("小响应不该压缩")
	}

	// 非 API 路径：静态资源自己已有 gzip 逻辑，这里必须原样透传（否则双重压缩）
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/panel/app.js", nil)
	req2.Header.Set("Accept-Encoding", "gzip")
	big := strings.Repeat("var x = 1;\n", 500)
	withGzipAPI(gzipHandler(big)).ServeHTTP(rec2, req2)
	if rec2.Header().Get("Content-Encoding") == "gzip" {
		t.Error("非 /panel/api/ 路径不该被这层压缩")
	}
	if rec2.Body.String() != big {
		t.Error("非 API 路径内容应原样透传")
	}

	// 客户端不发 Accept-Encoding：不压
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/panel/api/request_logs", nil)
	withGzipAPI(gzipHandler(strings.Repeat("x", 4096))).ServeHTTP(rec3, req3)
	if rec3.Header().Get("Content-Encoding") == "gzip" {
		t.Error("客户端未声明 gzip 时不该压缩")
	}
}
