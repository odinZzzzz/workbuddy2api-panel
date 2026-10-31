package qqbot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// tts.go —— 在线语音合成（edge-tts，微软神经网络语音）。
//
// 为什么不走网关：网关没有 OpenAI 的 /v1/audio/speech（实测 501 not_implemented，
// 52 个模型里也没有音频模型）。也不走离线 TTS：espeak-ng 在服务器上缺中文词典
// （"Full dictionary is not installed for 'zh'"），读中文基本不能用。
//
// 所以用 edge-tts（在线、免费、中文自然）：服务器上装了 python3.9 + edge-tts，
// 用 exec 调 CLI 合成 mp3，再通过 OneBot 的 record 段发出。
//
// 安全：文本作为独立 argv 传给 python（不经过 shell），避免命令注入。

// VoiceSynthesis 把文本合成为 mp3，返回文件路径。
func (b *Bridge) VoiceSynthesis(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("没有要念的内容")
	}
	cfg := b.cfg()
	limit := cfg.TTSMaxRunes
	if limit <= 0 {
		limit = 80
	}
	text = truncateRunes(text, limit)

	py := strings.TrimSpace(cfg.TTSPython)
	if py == "" {
		py = "python3.9"
	}
	voice := strings.TrimSpace(cfg.TTSVoice)
	if voice == "" {
		voice = "zh-CN-XiaoxiaoNeural"
	}

	dir := strings.TrimSpace(cfg.StickerDir)
	if dir == "" {
		dir = os.TempDir()
	}
	out := filepath.Join(dir, "generated")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(out, fmt.Sprintf("voice_%d.mp3", time.Now().UnixNano()))

	cmd := exec.Command(py, "-m", "edge_tts",
		"--voice", voice,
		"--text", text,
		"--write-media", path,
	)
	// 在线合成偶尔会卡：给个上限，别让整条回复挂住。
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动 edge-tts 失败: %w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("edge-tts 失败: %w", err)
		}
	case <-time.After(25 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("edge-tts 超时")
	}

	st, err := os.Stat(path)
	if err != nil || st.Size() < 1024 {
		return "", fmt.Errorf("语音文件异常（%v）", err)
	}
	return path, nil
}
