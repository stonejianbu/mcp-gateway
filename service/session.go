// Session
// 用于存储会话状态，包括接收的消息和处理结果
package service

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lucky-aeon/agentx/plugin-helper/types"
	"github.com/lucky-aeon/agentx/plugin-helper/xlog"
	"github.com/tidwall/gjson"
)

type McpName = string
type McpToolName = string

type McpMessage struct {
	McpName McpName
	Content string
	Type    string // "send" or "receive"
	Time    time.Time
}

type Session struct {
	sync.RWMutex
	Id              string
	Results         []string
	Offset          int
	Receives        []string
	ReceiveOffset   int       // 新增接收消息的偏移量
	LastReceiveTime time.Time // 最后一次接收消息的时间

	// 消息历史记录
	messagesMutex sync.RWMutex
	messages      []McpMessage

	// SSE事件通道
	eventChan chan string
	doneChan  chan struct{}

	// SSE订阅
	sseWaitGroup sync.WaitGroup
	sseConns     map[McpName]*http.Response // 存储SSE连接，用于关闭
	sseConnMutex sync.RWMutex

	mcpMessageUrl  map[McpName]string
	mcpMsgIdsMutex sync.RWMutex
	messageIds     map[int]int

	// 工具映射
	mcpToolsMutex sync.RWMutex
	mcpToolsMap   map[McpName]map[McpToolName]types.McpTool
	sendToolsChan chan bool
}

func NewSession(id string) *Session {
	return &Session{
		Id:              id,
		LastReceiveTime: time.Now(),
		messages:        make([]McpMessage, 0),
		eventChan:       make(chan string, 100), // 缓冲通道，避免阻塞
		doneChan:        make(chan struct{}),
		mcpMessageUrl:   make(map[McpName]string),
		messageIds:      make(map[int]int),
		mcpToolsMap:     make(map[McpName]map[McpToolName]types.McpTool),
		sendToolsChan:   make(chan bool),
		sseConns:        make(map[McpName]*http.Response),
	}
}

func (s *Session) AddReceive(receive string) {
	s.Lock()
	defer s.Unlock()
	s.Receives = append(s.Receives, receive)
	s.LastReceiveTime = time.Now()
}

func (s *Session) AddResult(result string) {
	s.Lock()
	defer s.Unlock()
	s.Results = append(s.Results, result)
}

func (s *Session) GetId() string {
	return s.Id
}

func (s *Session) GetResults() []string {
	s.RLock()
	defer s.RUnlock()
	results := make([]string, len(s.Results))
	copy(results, s.Results)
	return results
}

func (s *Session) GetReceives() []string {
	s.RLock()
	defer s.RUnlock()
	receives := make([]string, len(s.Receives))
	copy(receives, s.Receives)
	return receives
}

func (s *Session) GetOffset() int {
	s.RLock()
	defer s.RUnlock()
	return s.Offset
}

func (s *Session) SetOffset(offset int) {
	s.Lock()
	defer s.Unlock()
	s.Offset = offset
}

// GetUnprocessedReceives 获取未处理的接收消息
func (s *Session) GetUnprocessedReceives() []string {
	s.Lock()
	defer s.Unlock()

	if s.ReceiveOffset >= len(s.Receives) {
		return nil
	}

	unprocessed := make([]string, len(s.Receives)-s.ReceiveOffset)
	copy(unprocessed, s.Receives[s.ReceiveOffset:])
	s.ReceiveOffset = len(s.Receives)
	return unprocessed
}

// GetUnreadResults 获取未读取的处理结果
func (s *Session) GetUnreadResults() []string {
	s.Lock()
	defer s.Unlock()

	if s.Offset >= len(s.Results) {
		return nil
	}

	unread := make([]string, len(s.Results)-s.Offset)
	copy(unread, s.Results[s.Offset:])
	s.Offset = len(s.Results)
	return unread
}

func (s *Session) SendMessage(xl xlog.Logger, content string) error {
	// 发送消息到 MCP 服务
	method := gjson.Get(content, "method").String()
	xl.Infof("method: %s, content: %s", method, content)
	var singleMcp McpName
	if method == "tools/call" {
		name := gjson.Get(content, "params.name").String()
		if names := strings.Split(name, "-"); len(names) > 1 {
			singleMcp = names[0]
		}
	}

	// 对所有 MCP 服务器发送消息
	if singleMcp == "" {
		xl.Infof("send to all MCP servers: %s", content)
		for mcpName := range s.mcpMessageUrl {
			err := s.sendToMcp(xl, mcpName, content)
			if err != nil {
				return err
			}
		}
	} else {
		xl.Infof("send to single MCP server: %s, content: %s", singleMcp, content)
		err := s.sendToMcp(xl, singleMcp, content)
		if err != nil {
			return err
		}
	}

	s.AddMessage(singleMcp, content, "send")
	return nil
}

func (s *Session) generateMessageId(realMessageId int) int {
	s.mcpMsgIdsMutex.Lock()
	defer s.mcpMsgIdsMutex.Unlock()
	// 生成唯一的消息ID
	now := int(time.Now().UnixMilli())

	xlog.NewLogger("session-"+s.Id).Debugf("generate message id: %d, real message id: %d", now, realMessageId)
	s.messageIds[now] = realMessageId
	return now
}

func (s *Session) getRealMessageId(messageId int) (int, bool) {
	s.mcpMsgIdsMutex.RLock()
	defer s.mcpMsgIdsMutex.RUnlock()
	realMessageId, exists := s.messageIds[messageId]
	xlog.NewLogger("session-"+s.Id).Debugf("get real message id: %d, exists: %t", realMessageId, exists)
	return realMessageId, exists
}

func (s *Session) removeMessageId(messageId int) {
	s.mcpMsgIdsMutex.Lock()
	defer s.mcpMsgIdsMutex.Unlock()
	delete(s.messageIds, messageId)
}

func (s *Session) sendToMcp(xl xlog.Logger, mcpName McpName, content string) error {
	// 发送消息到 MCP 服务
	// 生成唯一的消息ID
	if gjson.Get(content, "id").Exists() {
		id := s.generateMessageId(int(gjson.Get(content, "id").Int()))
		// 替换消息中的ID
		content = strings.Replace(content, fmt.Sprintf(`"id":%d`, gjson.Get(content, "id").Int()), fmt.Sprintf(`"id":%d`, id), 1)
	}
	xl.Debugf("Sending message to %s: %s", mcpName, content)
	resp, err := http.Post(s.mcpMessageUrl[mcpName], "application/json", strings.NewReader(content))
	if err != nil {
		xl.Errorf("failed to send message: %v", err)
		return fmt.Errorf("failed to send message: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		xl.Errorf("failed to send message, status code: %d", resp.StatusCode)
		return fmt.Errorf("failed to send message, status code: %d", resp.StatusCode)
	}
	s.AddMessage(mcpName, content, "send")
	return nil
}

// AddMessage 添加一条消息记录
func (s *Session) AddMessage(mcpName McpName, content string, msgType string) {
	s.messagesMutex.Lock()
	defer s.messagesMutex.Unlock()

	s.messages = append(s.messages, McpMessage{
		McpName: mcpName,
		Content: content,
		Type:    msgType,
		Time:    time.Now(),
	})
}

// GetMessages 获取所有消息记录
func (s *Session) GetMessages() []McpMessage {
	s.messagesMutex.RLock()
	defer s.messagesMutex.RUnlock()

	// 返回消息记录的副本
	messages := make([]McpMessage, len(s.messages))
	copy(messages, s.messages)
	return messages
}

// SubscribeSSE 订阅MCP服务的SSE事件
func (s *Session) SubscribeSSE(mcpName McpName, sseUrl string) {
	s.sseWaitGroup.Add(1)
	go func() {
		defer s.sseWaitGroup.Done()
		xl := xlog.NewLogger("SSE-RECEIVE-" + string(mcpName))

		xl.Infof("Subscribing to SSE: %s", sseUrl)
		resp, err := http.Get(sseUrl)
		if err != nil {
			xl.Errorf("failed to subscribe SSE: %v", err)
			return
		}

		// 保存连接以便后续关闭
		s.sseConnMutex.Lock()
		s.sseConns[mcpName] = resp
		s.sseConnMutex.Unlock()

		defer func() {
			s.sseConnMutex.Lock()
			delete(s.sseConns, mcpName)
			s.sseConnMutex.Unlock()

			if err := resp.Body.Close(); err != nil {
				xl.Errorf("failed to close SSE: %v", err)
			}
		}()

		reader := bufio.NewReader(resp.Body)
		var currentEvent string

		for {
			select {
			case <-s.doneChan:
				if err := resp.Body.Close(); err != nil {
					xl.Errorf("failed to close SSE: %v", err)
				}
				xl.Infof("Closed SSE subscription: %s", sseUrl)
				return
			default:
				line, err := reader.ReadString('\n')
				if err != nil {
					xl.Errorf("failed to read SSE: %v", err)
					return
				}
				line = strings.TrimSpace(line)

				if line == "" {
					continue
				}

				if strings.HasPrefix(line, "event: ") {
					xl.Debugf("SSE event: %s", line)
					currentEvent = strings.TrimPrefix(line, "event: ")
				} else if strings.HasPrefix(line, "data: ") {
					data := strings.TrimPrefix(line, "data: ")
					xl.Debugf("SSE data: %s", data)

					// 如果是endpoint事件，保存endpoint
					if currentEvent == "endpoint" && s.mcpMessageUrl[mcpName] == "" {
						xl.Infof("Add SSE endpoint: %s", data)
						s.mcpMessageUrl[mcpName] = fmt.Sprintf("%s://%s%s", resp.Request.URL.Scheme, resp.Request.Host, data)
					}

					if gjson.Get(data, "id").Exists() {
						messageId := gjson.Get(data, "id").Int()
						// 检查是否是当前会话的消息
						realMessage, exists := s.getRealMessageId(int(messageId))
						if !exists {
							xl.Debugf("Message ID %d not found in session %s", messageId, s.Id)
							continue
						} else {
							xl.Debugf("Message ID %d found in session %s", messageId, s.Id)
						}
						s.removeMessageId(int(messageId))
						// 将消息ID替换为当前会话ID
						data = strings.Replace(data, fmt.Sprintf(`"id":%d`, messageId), fmt.Sprintf(`"id":%d`, realMessage), 1)

						// 获取tools
						if tools := gjson.Get(data, "result.tools").Array(); len(tools) > 0 {
							s.mcpToolsMutex.Lock()
							defer s.mcpToolsMutex.Unlock()
							s.mcpToolsMap[mcpName] = make(map[McpToolName]types.McpTool)
							for _, toolJ := range tools {
								var tool types.McpTool
								if err := json.Unmarshal([]byte(toolJ.Raw), &tool); err != nil {
									xl.Errorf("Failed to unmarshal tool: %v", err)
									continue
								}
								s.mcpToolsMap[mcpName][McpToolName(tool.Name)] = tool
							}
						}
					}

					// 记录接收到的消息
					s.AddMessage(mcpName, data, "receive")

					// 如果不是endpoint事件，转发给客户端
					if currentEvent != "endpoint" {
						s.SendEvent(fmt.Sprintf("data: %s\n\n", data))
					}
				}
			}
		}
	}()
}

// Close 关闭会话
func (s *Session) Close() {
	// 先关闭所有SSE连接
	s.sseConnMutex.Lock()
	for _, conn := range s.sseConns {
		if err := conn.Body.Close(); err != nil {
			xlog.NewLogger("session-"+s.Id).Errorf("failed to close SSE connection: %v", err)
		}
	}
	s.sseConnMutex.Unlock()

	close(s.doneChan)
	s.sseWaitGroup.Wait() // 等待所有SSE订阅goroutine结束
}

// SendEvent 发送SSE事件
func (s *Session) SendEvent(event string) {
	select {
	case s.eventChan <- event:
	default:
		// 如果通道已满，丢弃事件
	}
}

// GetEventChan 获取事件通道
func (s *Session) GetEventChan() <-chan string {
	return s.eventChan
}

// GetDoneChan 获取关闭通道
func (s *Session) GetDoneChan() <-chan struct{} {
	return s.doneChan
}

// GetMcpTools 获取指定 MCP 的所有工具
func (s *Session) GetMcpTools(mcpName McpName) map[McpToolName]types.McpTool {
	s.mcpToolsMutex.RLock()
	defer s.mcpToolsMutex.RUnlock()
	if tools, ok := s.mcpToolsMap[mcpName]; ok {
		// 创建一个副本以避免外部修改
		result := make(map[McpToolName]types.McpTool)
		for k, v := range tools {
			result[k] = v
		}
		return result
	}
	return nil
}

// GetMcpTool 获取指定 MCP 的指定工具
func (s *Session) GetMcpTool(mcpName McpName, toolName McpToolName) (types.McpTool, bool) {
	s.mcpToolsMutex.RLock()
	defer s.mcpToolsMutex.RUnlock()
	if tools, ok := s.mcpToolsMap[mcpName]; ok {
		if tool, ok := tools[toolName]; ok {
			return tool, true
		}
	}
	return types.McpTool{}, false
}
