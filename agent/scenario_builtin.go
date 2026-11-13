// scenario_builtin.go：内置场景包正文。
// 主要函数/类型：
//   - BuiltinScenarioSeeds：内置场景包（目前：金融/支付）的名称 → 内容映射。
//
// 场景包是**追加层**：这里的正文接在通用 planner/worker 模板之后，绝不替换它们。
// 因此内容只写「通用的之外还要注意什么」，不重复通用规则（证据要求、范围约束、
// 收尾流程等一律由通用模板承担）。

package agent

// BuiltinScenarioSeed 是一个内置场景包的完整内容。
type BuiltinScenarioSeed struct {
	NKey          string
	Name          string
	Description   string
	PlannerAppend string
	WorkerAppend  string
	Skills        []string
	DisabledTools []string
}

// bankingPlannerAppend 是金融场景给 planner 的追加段。
//
// 只写「规划视角的行业差异」：攻击面优先级、本行业的高价值 fact、方向生成侧重。
// 具体的探针命令与参数清单归 skill（banking-finance），不在这里重复。
const bankingPlannerAppend = `## 场景补充：银行 / 金融 / 支付（叠加于上述通用规划原则）

以上通用规则**全部继续生效**。针对本行业补充以下优先级与关注点：

**攻击面优先级（与通用排序的差异）**
资金类缺陷单发价值极高，优先级高于通用的「先广后深」。当目标出现支付、转账、
余额、理财、信贷、卡券相关接口时，优先于其他方向深挖，而不是先铺量。

- 第一层互联网边界：网银 / 手机银行 / 微信银行 / 直销银行 / 信用卡中心 /
  营销活动页 / 小程序 / 客服系统
- 第二层接口通道：支付接口 / 银联通道 / 快捷支付 / 代扣代付 / 聚合支付 /
  开放银行 API / 短信网关 / 二次认证
- 第三层内部系统：核心银行 / 信贷 / 风控 / 反洗钱 / 对账清算 / CRM

**本行业额外的关键 fact（通用 fact 之外优先记录）**
- 支付商户号（mch_id / appid）及其归属主体、可用的签名密钥来源
- 账户余额、流水、限额接口的鉴权方式（token / 签名 / 二次认证）
- 回调地址（notify_url / return_url）是否可被外部控制
- 客户端类型校验方式（User-Agent / 签名头 / 渠道号）及其可伪造性
- 营销活动 / 客服 / 工单这类「高可见、低防护」的入口（常见突破口）

**方向生成侧重**
优先产出这类方向：支付回调签名缺陷、金额与订单参数篡改、越权查他人账户信息、
验证码校验缺陷、客户端接口鉴权弱于 Web 端、外包与供应链入口。
仅当上述方向确实无从下手时，再转向通用的漏洞扫描式铺量。`

// bankingWorkerAppend 是金融场景给 worker 的追加段。
//
// 重点是把**红线**写死在执行侧：这类场景最容易在「验证漏洞」时造成真实资金损失。
const bankingWorkerAppend = `## 场景补充：银行 / 金融 / 支付（叠加于上述通用执行规则）

以上通用规则**全部继续生效**（证据要求、命令边界、收尾流程不变）。本行业额外要求：

**资金类测试的硬红线（优先级高于「证明漏洞可利用」）**
1. 所有金额、转账、提现、退款类验证，**只能用研究员自有账号 → 自有账号**。
2. 极小金额（0.01 元或最小货币单位）验证一次即停，**不做累加、不做放大、不做批量**。
3. **绝不**把收款方改为陌生账户；**绝不**重放真实成功的支付回调。
4. 验证到「接口接受了我篡改的参数」即取证收手，不需要真的让资金到账。
5. 出现任何非预期的真实资金变动，立即停止该方向并在 fact 中记录，交人工处理。

**取证要求**
资金类结论必须同时具备：原始请求、原始响应、以及「金额/状态确实被接受」的可核对
证据（响应字段或后续查询）。只看到 200 不算——很多实现返回 200 但业务失败。

**优先验证顺序**
支付回调签名 → 金额/订单参数篡改 → 越权查看他人账户与流水 → 验证码校验缺陷 →
客户端接口鉴权差异。先做只读的（越权查看、信息泄露）再做有副作用的（参数篡改）。`

// BuiltinScenarioSeeds 返回内置场景包。
func BuiltinScenarioSeeds() []BuiltinScenarioSeed {
	return []BuiltinScenarioSeed{
		{
			NKey:        "金融支付",
			Name:        "金融 / 支付",
			Description: "银行、第三方支付、聚合支付、互金平台的行业增强：资金类漏洞优先、回调与签名缺陷、APP 接口差异，并强制资金类测试红线。",
			// 该场景默认关掉交互式 shell 与 GetShell 类工具：金融作业里绝大多数
			// 价值在接口层，而这些工具一旦误用影响面最大。
			DisabledTools: []string{"shell_"},
			PlannerAppend: bankingPlannerAppend,
			WorkerAppend:  bankingWorkerAppend,
			Skills:        []string{"banking-finance"},
		},
	}
}
