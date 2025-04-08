package router

import (
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/lucky-aeon/agentx/plugin-helper/xlog"
)

// 全局SSE，这里返回所有MCP服务的SSE事件
func (m *ServerManager) handleGlobalSSE(c echo.Context) error {
	xl := xlog.WithEchoLogger(c.Logger())
	xl.Infof("Global SSE request: %v", c.Request().Body)
	querySessionId := c.QueryParam("sessionId")
	if querySessionId == "" {
		xl.Infof("No session ID provided, creating new session")
		// 没有sessionId，生成一个返回出去
		// create proxy session
		xl.Debugf("mcpServiceMgr: %+v", m.mcpServiceMgr)
		session := m.mcpServiceMgr.CreateProxySession(xl)
		xl.Infof("Created new session: %s", session.Id)
		// 302重定向到 /sse?sessionId={session.Id}
		return c.Redirect(http.StatusFound, fmt.Sprintf("/sse?sessionId=%s", session.Id))
	}
	c.Response().Header().Set("Content-Type", "text/event-stream")
	c.Response().Header().Set("Cache-Control", "no-cache")
	c.Response().Header().Set("Connection", "keep-alive")

	// get session by sessionId
	session, exists := m.mcpServiceMgr.GetProxySession(xl, querySessionId)
	if !exists {
		return c.String(http.StatusNotFound, "session not found")
	}
	defer func() {
		xl.Infof("Closing session: %s", session.Id)
		m.mcpServiceMgr.CloseProxySession(xl, session.Id)
	}()

	// 返回endpoint事件
	c.Response().WriteHeader(http.StatusOK)
	fmt.Fprintf(c.Response(), "event: endpoint\ndata: /message?sessionId=%s\n\n", session.Id)
	c.Response().Flush()

	// 转发所有SSE事件
	for {
		select {
		case <-c.Request().Context().Done():
			// client closed connection
			// session 通过 defer 关闭
			xl.Infof("Client closed connection, sessionId: %s", querySessionId)
			return nil
		case event := <-session.GetEventChan():
			xl.Infof("Event received: %v", event)
			if _, err := fmt.Fprintf(c.Response(), "%s", event); err != nil {
				xl.Errorf("Failed to write event: %v", err)
				return err
			}
			c.Response().Flush()
		}
	}
}
