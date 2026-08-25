package main

import (
	"strings"
	"testing"
)

// TestRegexHintCopyTokens 防止正则版说明/提示文案与 core 正则行为漂移：
// 关键 token 必须至少出现在某一处正则版文案里，避免用户看不到关键能力提示。
func TestRegexHintCopyTokens(t *testing.T) {
	for _, tok := range []string{"$1", "${name}", "$$", "(?i)", "lookaround", "\\1"} {
		if !strings.Contains(paramHintRegex, tok) && !strings.Contains(oldTipRegex, tok) && !strings.Contains(newTipRegex, tok) {
			t.Errorf("regex copy missing token %q", tok)
		}
	}
}
