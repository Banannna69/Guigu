// embed.go：任意 OpenAI 兼容 /embeddings 端点的客户端。
// 主要函数/类型：
//   - EmbedConfig / EmbedTexts：embedding 端点配置与批量取向量。
//   - snapshot：错误信息里的响应片段截断。
//
// 为什么自己写而不是引 SDK：embedding 接口的契约极简（POST {base}/embeddings，
// 输入一组文本、输出一组向量），而各家兼容实现（智谱、SiliconFlow、OpenAI、
// Ollama、vLLM）在细节上并不一致——有的不回 index 字段、有的在 200 里塞 error、
// 有的忽略 dimensions 参数。自己写反而能把这些差异逐个兜住。
package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// EmbedConfig 是一个 OpenAI 兼容的 embedding 端点配置。
type EmbedConfig struct {
	BaseURL string
	Model   string
	APIKey  string
	Dim     int
}

type embedReq struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embedResp struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// EmbedTexts 一次调用取回一组文本的向量，返回顺序与输入一致。
//
// index 字段的处理是这里的要点：规范要求每条结果带 index 说明它对应第几个输入，
// 但实际有实现不返回（或乱序返回）。所以先按 index 归位，发现有空位就整体退回
// 按响应位置对齐——这是「规范实现」和「能跑的实现」都能走通的唯一写法。
func EmbedTexts(ctx context.Context, cfg EmbedConfig, texts []string) ([][]float32, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("embedder 未配置 api_key")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("embedder 未配置 model")
	}
	if len(texts) == 0 {
		return nil, nil
	}
	url := strings.TrimRight(cfg.BaseURL, "/") + "/embeddings"
	payload := embedReq{Model: cfg.Model, Input: texts}
	if cfg.Dim > 0 {
		payload.Dimensions = cfg.Dim
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings 请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	var out embedResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("embeddings 响应解析失败 (HTTP %d): %s", resp.StatusCode, snapshot(raw))
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("embeddings 错误: %s", out.Error.Message)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("embeddings HTTP %d: %s", resp.StatusCode, snapshot(raw))
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings 数量不符: got %d want %d", len(out.Data), len(texts))
	}
	res := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(res) {
			res[d.Index] = d.Embedding
		}
	}
	for i := range res {
		if res[i] == nil { // 供应商没按 index 归位——整体退回按位置对齐
			for j := range out.Data {
				res[j] = out.Data[j].Embedding
			}
			break
		}
	}
	return res, nil
}

// snapshot 截断响应体用于错误信息，避免把整页 HTML 塞进日志。
func snapshot(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
