package types

// Property 定义输入参数的属性
type McpToolProperty struct {
	Type        string `json:"type"`        // 参数类型
	Description string `json:"description"` // 参数描述
}

// InputSchema 定义工具的输入参数结构
type McpToolInputSchema struct {
	Type       string                     `json:"type"`       // 通常是 "object"
	Properties map[string]McpToolProperty `json:"properties"` // 参数属性定义
	Required   []string                   `json:"required"`   // 必需的参数列表
}

// McpTool 定义工具的结构
type McpTool struct {
	Name        string             `json:"name"`        // 工具名称
	Description string             `json:"description"` // 工具描述
	InputSchema McpToolInputSchema `json:"inputSchema"` // 输入参数结构
}
