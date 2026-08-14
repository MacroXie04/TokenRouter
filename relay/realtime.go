package relay

import (
	"crypto/tls"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/middleware"
	relaycommon "github.com/tokenrouter/tokenrouter/relay/common"
	"github.com/tokenrouter/tokenrouter/service"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsDialer dials upstream WebSockets through the SSRF-guarded dialer.
var wsDialer = &websocket.Dialer{
	NetDialContext: common.SafeDialContext,
	TLSClientConfig: &tls.Config{InsecureSkipVerify: common.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false)},
}

// RelayWebSocket proxies the OpenAI Realtime WebSocket between the client and a
// selected upstream channel, mapping the model name and applying auth.
func RelayWebSocket(c *gin.Context) {
	token := middleware.GetRelayToken(c)
	if token == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}
	group := middleware.GetTokenGroup(c)
	if group == "" {
		group = service.GroupDefault
	}
	modelName := c.Query("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing model"})
		return
	}

	channel, err := service.GetRandomSatisfiedChannel(group, modelName, nil, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "no available channel"})
		return
	}

	wsURL := toWebSocketURL(channel.BaseURL, "/v1/realtime") + "?model=" + relaycommon.GetMappedModel(channel, modelName)

	clientConn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()

	header := http.Header{}
	header.Set("Authorization", "Bearer "+channel.Key)
	upstreamConn, _, err := wsDialer.Dial(wsURL, header)
	if err != nil {
		_ = clientConn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "upstream dial failed"), time.Now().Add(5*time.Second))
		return
	}
	defer upstreamConn.Close()

	done := make(chan struct{}, 2)
	go proxyWebSocket(upstreamConn, clientConn, done)
	go proxyWebSocket(clientConn, upstreamConn, done)
	<-done
}

// proxyWebSocket copies messages from src to dst until either side errors.
func proxyWebSocket(dst, src *websocket.Conn, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	for {
		msgType, data, err := src.ReadMessage()
		if err != nil {
			return
		}
		if err := dst.WriteMessage(msgType, data); err != nil {
			return
		}
	}
}

// toWebSocketURL converts an http(s) base URL to ws(s) and appends a path.
func toWebSocketURL(base, path string) string {
	if base == "" {
		base = "https://api.openai.com"
	}
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}
	return relaycommon.JoinURL(base, path)
}
