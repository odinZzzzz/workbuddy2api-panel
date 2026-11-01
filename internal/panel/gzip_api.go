package panel

// gzip_api.go 面板 /panel/api/* 的响应压缩。
//
// 现场：请求日志 600 条 = 327KB 明文 JSON，服务端处理只要 10ms，但公网拉要 5s——
// 时间几乎全花在传未压缩的 JSON 上（/panel/api/request_logs?limit=1000 是 540KB）。
// 静态资源（index.html / app.js / app.css）早就有 gzip + ETag，只有 API 一直裸传。
//
// 做法：只对 /panel/api/ 生效的缓冲式 gzip 中间件——
//   - 客户端没发 Accept-Encoding: gzip → 原样透传；
//   - 响应体小于 gzipMinBytes（小接口压了反而多几个字节的头）→ 原样透传；
//   - 非 2xx → 原样透传（错误信息量小，也便于抓包看原文）。
// 缓冲是安全的：面板 API 都是小 JSON（受 limit 上限约束，最大几百 KB）。

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// gzipMinBytes 低于此长度不压缩。
const gzipMinBytes = 1024

// bufferedWriter 收集 handler 的输出，决定要不要压。
type bufferedWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (b *bufferedWriter) Header() http.Header { return b.header }

func (b *bufferedWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}

func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// withGzipAPI 给 /panel/api/* 加 gzip（其余路径原样透传，静态资源自己已压缩）。
func withGzipAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/panel/api/") ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		bw := &bufferedWriter{header: make(http.Header)}
		next.ServeHTTP(bw, r)
		if bw.status == 0 {
			bw.status = http.StatusOK
		}
		h := w.Header()
		for k, vs := range bw.header {
			for _, v := range vs {
				h.Add(k, v)
			}
		}
		// 太小 / 非成功：原样写出。
		if bw.status >= 300 || bw.body.Len() < gzipMinBytes {
			h.Del("Content-Length")
			w.WriteHeader(bw.status)
			_, _ = w.Write(bw.body.Bytes())
			return
		}
		var gz bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&gz, gzip.BestSpeed)
		if _, err := zw.Write(bw.body.Bytes()); err != nil {
			h.Del("Content-Length")
			w.WriteHeader(bw.status)
			_, _ = w.Write(bw.body.Bytes())
			return
		}
		_ = zw.Close()
		// 压缩反而更大（极小/高熵内容）就发原文。
		if gz.Len() >= bw.body.Len() {
			h.Del("Content-Length")
			w.WriteHeader(bw.status)
			_, _ = w.Write(bw.body.Bytes())
			return
		}
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		h.Set("Content-Length", strconv.Itoa(gz.Len()))
		w.WriteHeader(bw.status)
		_, _ = w.Write(gz.Bytes())
	})
}
