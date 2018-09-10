package server

import (
	"crypto/tls"
	"fmt"
	vhost "github.com/inconshreveable/go-vhost"
	//"net"
	"ngrok/conn"
	"ngrok/log"
	"strings"
	"time"
)

const (
	NotAuthorized = `HTTP/1.0 401 Not Authorized
WWW-Authenticate: Basic realm="ngrok"
Content-Length: 23

Authorization required
`

	NotFound = `HTTP/1.0 404 Not Found
Content-Length: %d

Tunnel %s not found
`

	BadRequest = `HTTP/1.0 400 Bad Request
Content-Length: 12

Bad Request
`
)

// Listens for new http(s) connections from the public internet
// 从公共 internet 侦听新的 http 连接
func startHttpListener(addr string, tlsCfg *tls.Config) (listener *conn.Listener) {
	// bind/listen for incoming connections
	var err error
	if listener, err = conn.Listen(addr, "pub", tlsCfg); err != nil {
		panic(err)
	}

	proto := "http"
	if tlsCfg != nil {
		proto = "https"
	}

	log.Info("Listening for public %s connections on %v", proto, listener.Addr.String())
	go func() {
		for conn := range listener.Conns {
			go httpHandler(conn, proto)
		}
	}()

	return
}

// Handles a new http connection from the public internet
func httpHandler(c conn.Conn, proto string) {
	defer c.Close()
	defer func() {
		// recover from failures
		if r := recover(); r != nil {
			c.Warn("httpHandler failed with error %v", r)
		}
	}()

	// Make sure we detect dead connections while we decide how to multiplex
	// 设置超时时间
	//net.Conn 为Deadline提供了多个方法 Set[Read|Write]Deadline(time.Time) 。
	// Deadline是一个绝对时间值，当到达这个时间的时候，所有的 I/O 操作都会失败，返回超时(timeout)错误。
	c.SetDeadline(time.Now().Add(connReadTimeout))

	// multiplex by extracting the Host header, the vhost library
	// 虚拟主机库中提取主机头的多重复用
	vhostConn, err := vhost.HTTP(c)
	if err != nil {
		c.Warn("Failed to read valid %s request: %v", proto, err)
		c.Write([]byte(BadRequest))
		return
	}

	// read out the Host header and auth from the request
	// 从请求中读取主机头和授权
	host := strings.ToLower(vhostConn.Host())
	auth := vhostConn.Request.Header.Get("Authorization")

	// done reading mux data, free up the request memory
	// 完成读取 mux 数据, 释放请求内存
	vhostConn.Free()

	// We need to read from the vhost conn now since it mucked around reading the stream
	// 现在我们需要vhost conn中读取流数据
	c = conn.Wrap(vhostConn, "pub")

	// multiplex to find the right backend host
	// 多路查找右后端主机
	// 查找对应的配置信息。没有则返回同有找到。
	// !!!根据 proto host 找到对应的tunnel
	c.Debug("Found hostname %s in request", host)
	tunnel := tunnelRegistry.Get(fmt.Sprintf("%s://%s", proto, host))
	if tunnel == nil {
		c.Info("No tunnel found for hostname %s", host)
		c.Write([]byte(fmt.Sprintf(NotFound, len(host)+18, host)))
		return
	}

	// If the client specified http auth and it doesn't match this request's auth
	// then fail the request with 401 Not Authorized and request the client reissue the
	// request with basic authdeny the request
	// 如果客户端指定了 http 身份验证, 并且它与此请求的授权不匹配,
	// 则失败请求与401未被批准并且请求客户端重新发出请求以基本 authdeny 请求
	// !!! 检查该隧道是否通过身份验证
	if tunnel.req.HttpAuth != "" && auth != tunnel.req.HttpAuth {
		c.Info("Authentication failed: %s", auth)
		c.Write([]byte(NotAuthorized))
		return
	}

	// dead connections will now be handled by tunnel heartbeating and the client
	// 死连接现在将由隧道心跳和客户端处理
	c.SetDeadline(time.Time{})

	// let the tunnel handle the connection now
	// 现在让隧道处理连接
	tunnel.HandlePublicConnection(c)
}
