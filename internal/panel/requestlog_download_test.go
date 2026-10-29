package panel

import "testing"

// day 参数直接参与拼路径，必须严格校验（防路径穿越）。
func TestValidArchiveDay(t *testing.T) {
	ok := []string{"2026-10-30", "2024-02-29", "1999-01-01"}
	bad := []string{"", "2026-1-1", "2026-10-30 ", "../etc/passwd", "2026-13-01", "2026-02-30", "requests-2026-10-30.jsonl", "2026/10/30"}
	for _, d := range ok {
		if !validArchiveDay(d) {
			t.Errorf("%q 应通过校验", d)
		}
	}
	for _, d := range bad {
		if validArchiveDay(d) {
			t.Errorf("%q 应被拒绝", d)
		}
	}
}
