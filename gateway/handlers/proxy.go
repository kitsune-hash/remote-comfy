package handlers

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

type ProxyHandler struct {
	comfyURL string
	target   *url.URL
}

func NewProxyHandler(comfyURL string) *ProxyHandler {
	target, _ := url.Parse(comfyURL)
	return &ProxyHandler{
		comfyURL: comfyURL,
		target:   target,
	}
}

// ProxyHTTP forwards HTTP requests to ComfyUI
func (h *ProxyHandler) ProxyHTTP(c *gin.Context) {
	proxy := httputil.NewSingleHostReverseProxy(h.target)
	
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = h.target.Scheme
		req.URL.Host = h.target.Host
		req.URL.Path = c.Request.URL.Path
		req.URL.RawQuery = c.Request.URL.RawQuery
		req.Host = h.target.Host
		// Copy headers but remove problematic ones
		req.Header = make(http.Header)
		for k, v := range c.Request.Header {
			if !strings.EqualFold(k, "Host") && !strings.EqualFold(k, "Origin") && !strings.EqualFold(k, "Referer") {
				req.Header[k] = v
			}
		}
	}
	
	proxy.ServeHTTP(c.Writer, c.Request)
}

var proxyUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ProxyWS forwards WebSocket connections to ComfyUI
func (h *ProxyHandler) ProxyWS(c *gin.Context) {
	// Connect to ComfyUI WebSocket
	wsURL := strings.Replace(h.comfyURL, "http://", "ws://", 1) + "/ws"
	if clientId := c.Query("clientId"); clientId != "" {
		wsURL += "?clientId=" + clientId
	}
	
	comfyConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to connect to ComfyUI: " + err.Error()})
		return
	}
	defer comfyConn.Close()
	
	// Upgrade client connection
	clientConn, err := proxyUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer clientConn.Close()
	
	// Bidirectional relay
	errChan := make(chan error, 2)
	
	go func() {
		for {
			msgType, msg, err := comfyConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			if err := clientConn.WriteMessage(msgType, msg); err != nil {
				errChan <- err
				return
			}
		}
	}()
	
	go func() {
		for {
			msgType, msg, err := clientConn.ReadMessage()
			if err != nil {
				errChan <- err
				return
			}
			if err := comfyConn.WriteMessage(msgType, msg); err != nil {
				errChan <- err
				return
			}
		}
	}()
	
	<-errChan
}

// Upload handles file uploads to ComfyUI
func (h *ProxyHandler) Upload(c *gin.Context) {
	target := h.comfyURL + c.Request.URL.Path
	
	req, err := http.NewRequest(c.Request.Method, target, c.Request.Body)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	for k, v := range c.Request.Header {
		req.Header[k] = v
	}
	
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	
	c.Status(resp.StatusCode)
	for k, v := range resp.Header {
		c.Header(k, v[0])
	}
	io.Copy(c.Writer, resp.Body)
}
