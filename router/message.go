package router

import (
	"io"
	"net/http"

	"github.com/labstack/echo/v4"
)

// 全局MESSAGE，这里将POST请求转发到所有MCP服务
func (m *ServerManager) handleGlobalMessage(c echo.Context) error {
	c.Logger().Infof("Global message: %v", c.Request().Body)
	sessionId := c.QueryParam("sessionId")
	if sessionId == "" {
		return c.String(http.StatusBadRequest, "missing sessionId")
	}

	// 获取session
	session, exists := m.mcpServiceMgr.GetProxySession(sessionId)
	if !exists {
		return c.String(http.StatusNotFound, "session not found")
	}

	// 读取请求体
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		return err
	}

	// 转发消息
	c.Logger().Infof("Global message from session %s: %s", session.Id, string(body))
	mcpServices := m.mcpServiceMgr.GetMcpServices(c.Logger())
	for name, instance := range mcpServices {
		c.Logger().Infof("Forwarding message to %s", name)
		// 记录发送的消息
		session.AddMessage(name, string(body), "send")
		if err := instance.SendMessage(string(body)); err != nil {
			c.Logger().Errorf("Failed to forward message to %s: %v", name, err)
		}
	}

	return nil
}
