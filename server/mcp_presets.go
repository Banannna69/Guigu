// mcp_presets.go：常用后渗透 MCP 的可导入模板。
// 主要函数/类型：
//   - mcpPreset / builtinMCPPresets：模板清单。
//   - mcpPresetList / mcpPresetApply：列出模板、按模板创建 MCP 服务。
//
// 为什么需要：平台的后渗透编排（execution_mode → vuln_dispatcher）已经写得很严谨，
// 但「取得立足点之后用什么」一直是空的——WebShell 通道与 C2 全在外部 MCP 里，而
// 用户不知道要装哪个、怎么填参数。这里给出一份可一键导入的清单，把门槛从「读文档
// 自己拼配置」降到「填个地址点导入」。
//
// 注意：模板只预填**非敏感**字段（transport / 地址形态 / 启动命令）。密钥、密码、
// 会话 id 一律留空由用户填——不预填凭据，也不在模板里写死任何真实主机。

package server

import (
	"encoding/json"
	"net/http"

	"artex/db"
)

// mcpPreset 是一个后渗透类 MCP 的导入模板。
type mcpPreset struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`        // 建议的服务名（用户可改）
	Category    string   `json:"category"`    // shell | c2 | browser | recon
	Description string   `json:"description"` // 用途与前置条件
	Transport   string   `json:"transport"`   // stdio | http | sse
	Command     string   `json:"command"`     // stdio 时的启动命令
	Args        []string `json:"args"`        // stdio 参数
	URL         string   `json:"url"`         // http/sse 的地址形态（占位）
	// NeedsInput 列出用户导入后**必须自己填**的字段，前端据此提示。
	// 模板永远不预填这些值。
	NeedsInput []string `json:"needs_input"`
	DocsURL    string   `json:"docs_url,omitempty"`
}

// builtinMCPPresets 是内置模板。
//
// 只收录「做了能立刻用」的：每一项都在渗透流程里有明确位置，且配置形态固定到
// 可以给出可直接复制的模板。刻意不收录需要复杂自建的项——那类模板填一半反而
// 误导。
func builtinMCPPresets() []mcpPreset {
	return []mcpPreset{
		{
			Key:      "godzilla-super",
			Name:     "GodzillaSuper",
			Category: "shell",
			Description: "WebShell 管理：连接已上传的 WebShell 并执行命令、传文件。取得立足点后，" +
				"用它把「一次性的 RCE 回显」变成「可持续的交互通道」——平台的后渗透派发判定" +
				"要求的正是可连接证据。需要 Java 17+。",
			Transport: "sse",
			URL:       "http://127.0.0.1:9123/sse",
			NeedsInput: []string{
				"url：MCP 服务监听地址（先在本机 java -jar 启动）",
				"启动后在本 MCP 内配置 WebShell 的地址、密码、密钥与类型",
			},
			DocsURL: "https://github.com/Xaaaa-bip/GodzillaSuper",
		},
		{
			Key:      "playwright-mcp",
			Name:     "Playwright",
			Category: "browser",
			Description: "浏览器自动化：需要真实浏览器才能推进的场景（登录后页面、SPA、截图取证）。" +
				"镜像内已预装 Chromium，无需额外下载。",
			Transport: "stdio",
			Command:   "npx",
			Args:      []string{"@playwright/mcp", "--headless"},
			NeedsInput: []string{
				"若宿主机未装 Node，可改用镜像内路径",
			},
			DocsURL: "https://github.com/microsoft/playwright-mcp",
		},
		{
			Key:      "scopesentry",
			Name:     "ScopeSentry",
			Category: "recon",
			Description: "资产测绘同步：把 ScopeSentry 扫描到的资产导入本平台资产图。",
			Transport: "http",
			URL:       "http://127.0.0.1:8082/mcp",
			NeedsInput: []string{
				"url：ScopeSentry 的 MCP 端点",
				"请求头：Authorization=Bearer <在 ScopeSentry 侧生成的 API Key>",
			},
			DocsURL: "https://github.com/DoubleShark/ScopeSentry",
		},
	}
}

// mcpPresetList GET /api/mcp/presets
func (s *Server) mcpPresetList(w http.ResponseWriter, r *http.Request) {
	if s.pg(w) == nil {
		return
	}
	writeJSON(w, 200, map[string]any{"presets": builtinMCPPresets()})
}

// mcpPresetApply POST /api/mcp/presets/{key}
//
// 按模板创建一个 MCP 服务。只创建、不自动启用：用户填完地址/凭据后自行开启，
// 避免一个指向错误地址的服务被自动拉起来并反复失败重连。
func (s *Server) mcpPresetApply(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	key := r.PathValue("key")
	var preset *mcpPreset
	for i := range builtinMCPPresets() {
		if builtinMCPPresets()[i].Key == key {
			p := builtinMCPPresets()[i]
			preset = &p
			break
		}
	}
	if preset == nil {
		writeErr(w, 404, "未知的 MCP 模板")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	// 允许空 body：直接用模板建议的名字。
	_ = decodeJSONBounded(w, r, &body, maxSmallJSONBodyBytes)
	name := preset.Name
	if body.Name != "" {
		name = body.Name
	}

	argsJSON, _ := json.Marshal(preset.Args)
	srv := &db.MCPServer{
		Name:      name,
		Transport: preset.Transport,
		Command:   preset.Command,
		Args:      argsJSON,
		Env:       json.RawMessage(`{}`),
		URL:       preset.URL,
		// 刻意不启用：模板里的地址是占位，用户必须自己改。
		Enabled: false,
	}
	if _, err := pg.SaveMCP(srv); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	pg.RecordAudit(AdminUsername, db.AuditCategorySettings, "mcp_preset_apply", "success", clientIPFrom(r),
		"按模板创建 MCP："+name, map[string]any{"preset": key, "name": name})
	writeJSON(w, 200, map[string]any{
		"name":        name,
		"enabled":     false,
		"needs_input": preset.NeedsInput,
		// 前端据此提示用户「已创建，请填写后启用」。
		"message": "已按模板创建（默认停用）。请填写地址/凭据后启用。",
	})
}