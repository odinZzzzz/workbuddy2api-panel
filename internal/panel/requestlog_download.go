package panel

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// requestlog_download.go —— 请求归档的下载与可用日期查询。
//
// 为什么需要：归档是 JSONL，此前只能 ssh 上服务器翻。面板「运行日志」已经有查询，
// 但完整原始归档（含全部字段）只有文件里有；下载出来才能本地 grep / 喂给脚本。
//
// 安全：day 参数严格按 YYYY-MM-DD 校验后再拼路径——文件名直接来自用户输入，
// 不做校验就是路径穿越（../../etc/passwd）。

// archiveDayPattern 归档文件名的日期形态。
func validArchiveDay(day string) bool {
	if len(day) != 10 {
		return false
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return false
	}
	return true
}

// requestLogsDays GET /panel/api/request_logs/days
//
// 返回归档目录下已有的日期（升序），供前端做日期选择。目录不存在时返回空列表。
func (p *Panel) requestLogsDays(w http.ResponseWriter, r *http.Request) {
	dir := ""
	if p.cfg.RequestLog != nil {
		dir = p.cfg.RequestLog.ArchiveDir()
	}
	if dir == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": false, "days": []string{}})
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": true, "days": []string{}})
		return
	}
	days := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "requests-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "requests-"), ".jsonl")
		if validArchiveDay(day) {
			days = append(days, day)
		}
	}
	sort.Strings(days)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": true, "days": days})
}

// requestLogsDownload GET /panel/api/request_logs/download?day=YYYY-MM-DD
//
// 不给 day 时下载当天（服务器本地时区）。文件不存在时回 404 并提示可用日期，
// 而不是空响应——"下载了一个空文件"比"明确的 404"更难排查。
func (p *Panel) requestLogsDownload(w http.ResponseWriter, r *http.Request) {
	dir := ""
	if p.cfg.RequestLog != nil {
		dir = p.cfg.RequestLog.ArchiveDir()
	}
	if dir == "" {
		writeErr(w, http.StatusNotImplemented, "request archive is disabled (logging.request_archive_enabled=false)")
		return
	}
	day := strings.TrimSpace(r.URL.Query().Get("day"))
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	if !validArchiveDay(day) {
		writeErr(w, http.StatusBadRequest, "day 需要 YYYY-MM-DD（示例 2026-10-30）")
		return
	}
	path := filepath.Join(dir, "requests-"+day+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		// 列出可用日期，省掉用户再点一次"有哪些"
		avail := ""
		if entries, derr := os.ReadDir(dir); derr == nil {
			var days []string
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, "requests-") && strings.HasSuffix(name, ".jsonl") {
					d := strings.TrimSuffix(strings.TrimPrefix(name, "requests-"), ".jsonl")
					if validArchiveDay(d) {
						days = append(days, d)
					}
				}
			}
			sort.Strings(days)
			if len(days) > 0 {
				avail = "；可用日期：" + strings.Join(days, ", ")
			}
		}
		writeErr(w, http.StatusNotFound, "该日期没有归档文件："+day+avail)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stat archive: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"requests-%s.jsonl\"", day))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", stat.Size()))
	w.WriteHeader(http.StatusOK)
	buf := make([]byte, 64*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if rerr != nil {
			return
		}
	}
}
