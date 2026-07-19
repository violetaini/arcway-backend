package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const telegramAPIBase = "https://api.telegram.org/bot"

var httpClient = &http.Client{Timeout: 10 * time.Second}

func sendTelegram(ctx context.Context, botToken, chatID, text string) error {
	if botToken == "" || chatID == "" {
		return fmt.Errorf("bot token or chat ID is empty")
	}

	endpoint := telegramAPIBase + botToken + "/sendMessage"
	params := url.Values{
		"chat_id":    {chatID},
		"text":       {text},
		"parse_mode": {"Markdown"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.URL.RawQuery = params.Encode()

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var result struct {
			OK          bool   `json:"ok"`
			Description string `json:"description"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&result)
		return fmt.Errorf("telegram API error (status %d): %s", resp.StatusCode, result.Description)
	}

	return nil
}

// markdownEscaper 转义 Telegram legacy Markdown 的特殊字符。
var markdownEscaper = strings.NewReplacer("_", "\\_", "*", "\\*", "`", "\\`", "[", "\\[")

// EscapeMarkdown 把用户名/服务器名等动态内容安全地嵌进带 *bold* / `code` 的消息模板。
// 未转义时,含下划线(或 * ` [)的用户名会让 TG 的 Markdown 解析失败 → 400 bad request。
func EscapeMarkdown(s string) string {
	return markdownEscaper.Replace(s)
}
