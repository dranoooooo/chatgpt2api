package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
)

// Telegram 推送配置全部来自环境变量，设置后生成图会直接发到指定频道/群：
//
//	TELEGRAM_BOT_TOKEN  必填，BotFather 给的 token
//	TELEGRAM_CHAT_ID    必填，频道/群的 chat id（如 -1001234567890 或 @channelname）
//	TELEGRAM_SEND_AS    选填，photo（默认，按图片发，方便在频道里直接看）或 document（按原文件发，不压缩）
//	TELEGRAM_CAPTION    选填，图片说明文字
//	TELEGRAM_API_BASE   选填，默认 https://api.telegram.org
type telegramSettings struct {
	botToken string
	chatID   string
	sendAs   string
	caption  string
	apiBase  string
}

func telegramFromEnv() (telegramSettings, bool) {
	settings := telegramSettings{
		botToken: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")),
		chatID:   strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID")),
		sendAs:   strings.ToLower(strings.TrimSpace(os.Getenv("TELEGRAM_SEND_AS"))),
		caption:  strings.TrimSpace(os.Getenv("TELEGRAM_CAPTION")),
		apiBase:  strings.TrimSpace(os.Getenv("TELEGRAM_API_BASE")),
	}
	if settings.botToken == "" || settings.chatID == "" {
		return telegramSettings{}, false
	}
	if settings.sendAs == "" {
		settings.sendAs = "photo"
	}
	if settings.apiBase == "" {
		settings.apiBase = "https://api.telegram.org"
	}
	return settings, true
}

// notifyTelegram 把生成好的图片直接推送到 Telegram 频道/群。
// 异步执行，失败只记录日志，不影响本地/WebDAV 保存。
func (s *ImageService) notifyTelegram(data []byte, name string) {
	settings, ok := telegramFromEnv()
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := sendTelegramImage(ctx, settings, data, name); err != nil {
			log.Printf("[telegram] 图片推送失败: %v", err)
			return
		}
		log.Printf("[telegram] 图片已推送: %s", name)
	}()
}

func sendTelegramImage(ctx context.Context, settings telegramSettings, data []byte, name string) error {
	if settings.sendAs == "document" {
		return postTelegram(ctx, settings, "sendDocument", "document", data, name)
	}
	// 默认按图片发送；超出图片上限或格式不支持时改为按原文件发送
	if err := postTelegram(ctx, settings, "sendPhoto", "photo", data, name); err != nil {
		log.Printf("[telegram] sendPhoto 失败，改为按文件发送: %v", err)
		return postTelegram(ctx, settings, "sendDocument", "document", data, name)
	}
	return nil
}

func postTelegram(ctx context.Context, settings telegramSettings, method, field string, data []byte, name string) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("chat_id", settings.chatID); err != nil {
		return err
	}
	if settings.caption != "" {
		if err := writer.WriteField("caption", settings.caption); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile(field, name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/bot%s/%s", strings.TrimSuffix(settings.apiBase, "/"), settings.botToken, method)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{Timeout: 90 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	var result struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	_ = json.Unmarshal(payload, &result)
	if response.StatusCode != http.StatusOK || !result.OK {
		message := strings.TrimSpace(result.Description)
		if message == "" {
			message = strings.TrimSpace(string(payload))
		}
		return fmt.Errorf("%s 返回 %d: %s", method, response.StatusCode, message)
	}
	return nil
}
