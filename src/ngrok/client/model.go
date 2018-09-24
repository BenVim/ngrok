package client

import (
	"crypto/tls"
	"fmt"
	metrics "github.com/rcrowley/go-metrics"
	"io/ioutil"
	"math"
	"net"
	"ngrok/client/mvc"
	"ngrok/conn"
	"ngrok/log"
	"ngrok/msg"
	"ngrok/proto"
	"ngrok/util"
	"ngrok/version"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultServerAddr   = "ngrokd.ngrok.com:443"
	defaultInspectAddr  = "127.0.0.1:4040"
	pingInterval        = 20 * time.Second
	maxPongLatency      = 15 * time.Second
	updateCheckInterval = 6 * time.Hour
	BadGateway          = `<html>
<body style="background-color: #97a8b9">
    <div style="margin:auto; width:400px;padding: 20px 60px; background-color: #D3D3D3; border: 5px solid maroon;">
        <h2>Tunnel %s unavailable</h2>
        <p>Unable to initiate connection to <strong>%s</strong>. A web server must be running on port <strong>%s</strong> to complete the tunnel.</p>
`
)

type ClientModel struct {
	log.Logger

	id            string
	tunnels       map[string]mvc.Tunnel
	serverVersion string
	metrics       *ClientMetrics
	updateStatus  mvc.UpdateStatus
	connStatus    mvc.ConnStatus
	protoMap      map[string]proto.Protocol
	protocols     []proto.Protocol
	ctl           mvc.Controller
	serverAddr    string
	proxyUrl      string
	authToken     string
	tlsConfig     *tls.Config //tls包的
	tunnelConfig  map[string]*TunnelConfiguration
	configPath    string
}

func newClientModel(config *Configuration, ctl mvc.Controller) *ClientModel {
	protoMap := make(map[string]proto.Protocol)
	protoMap["http"] = proto.NewHttp()
	protoMap["https"] = protoMap["http"]
	protoMap["tcp"] = proto.NewTcp()
	protocols := []proto.Protocol{protoMap["http"], protoMap["tcp"]}

	m := &ClientModel{
		Logger: log.NewPrefixLogger("client"),

		// server address
		serverAddr: config.ServerAddr,

		// proxy address
		proxyUrl: config.HttpProxy,

		// auth token
		authToken: config.AuthToken,

		// connection status
		connStatus: mvc.ConnConnecting, //mvc.state const ConnConnecting

		// update status
		updateStatus: mvc.UpdateNone,

		// metrics
		metrics: NewClientMetrics(),

		// protocols
		protoMap: protoMap,

		// protocol list
		protocols: protocols,

		// open tunnels
		tunnels: make(map[string]mvc.Tunnel),

		// controller
		ctl: ctl,

		// tunnel configuration
		tunnelConfig: config.Tunnels,

		// config path
		configPath: config.Path,
	}

	// configure TLS
	if config.TrustHostRootCerts {
		m.Info("Trusting host's root certificates") //因为model结构体包含了logger接口 所以可以使用 logger的Info方法
		m.tlsConfig = &tls.Config{} //给model增加tls的Config引用
	} else {
		m.Info("Trusting root CAs: %v", rootCrtPaths)
		var err error
		//加载证书 证书的地址 tls.go 的LoadTLSConfig主要的功能是负责加载证书文件的。
		if m.tlsConfig, err = LoadTLSConfig(rootCrtPaths); err != nil {
			panic(err)
		}
	}

	// configure TLS SNI
	// 用于认证返回证书的主机名（除非设置了InsecureSkipVerify）。
	// 也被用在客户端的握手里，以支持虚拟主机。
	m.tlsConfig.ServerName = serverName(m.serverAddr)
	// InsecureSkipVerify控制客户端是否认证服务端的证书链和主机名。
	// 如果InsecureSkipVerify为真，TLS连接会接受服务端提供的任何证书和该证书中的任何主机名。
	// 此时，TLS连接容易遭受中间人攻击，这种设置只应用于测试。
	m.tlsConfig.InsecureSkipVerify = useInsecureSkipVerify() //debug为true。

	return m
}

// server name in release builds is the host part of the server address
func serverName(addr string) string {
	host, _, err := net.SplitHostPort(addr)

	// should never panic because the config parser calls SplitHostPort first
	if err != nil {
		panic(err)
	}

	return host
}

// mvc.State interface
func (c ClientModel) GetProtocols() []proto.Protocol { return c.protocols }
func (c ClientModel) GetClientVersion() string       { return version.MajorMinor() }
func (c ClientModel) GetServerVersion() string       { return c.serverVersion }
func (c ClientModel) GetTunnels() []mvc.Tunnel {
	tunnels := make([]mvc.Tunnel, 0)
	for _, t := range c.tunnels {
		tunnels = append(tunnels, t)
	}
	return tunnels
}
//mvc.ConnStatus 是位置mvc目录下state.go 文件中的 type ConnStatus int
func (c ClientModel) GetConnStatus() mvc.ConnStatus     { return c.connStatus }
// type UpdateStatus int 有三个常量是做为该参数的值类型
func (c ClientModel) GetUpdateStatus() mvc.UpdateStatus { return c.updateStatus }

// 从model中获取metrics中的connMeter和connTimer统计结果 metrics是统计用的
func (c ClientModel) GetConnectionMetrics() (metrics.Meter, metrics.Timer) {
	return c.metrics.connMeter, c.metrics.connTimer
}

//也是获取统计信息的
func (c ClientModel) GetBytesInMetrics() (metrics.Counter, metrics.Histogram) {
	return c.metrics.bytesInCount, c.metrics.bytesIn
}

//获取统计信息的
func (c ClientModel) GetBytesOutMetrics() (metrics.Counter, metrics.Histogram) {
	return c.metrics.bytesOutCount, c.metrics.bytesOut
}
//更新updateStatus 更新model的状态后，更新调用控制器的update方法。设置model的updateStatus后。则需要更新信息。
func (c ClientModel) SetUpdateStatus(updateStatus mvc.UpdateStatus) {
	c.updateStatus = updateStatus
	c.update()
}

// mvc.Model interface
func (c *ClientModel) PlayRequest(tunnel mvc.Tunnel, payload []byte) {
	var localConn conn.Conn
	localConn, err := conn.Dial(tunnel.LocalAddr, "prv", nil)
	if err != nil {
		c.Warn("Failed to open private leg to %s: %v", tunnel.LocalAddr, err)
		return
	}

	defer localConn.Close()
	localConn = tunnel.Protocol.WrapConn(localConn, mvc.ConnectionContext{Tunnel: tunnel, ClientAddr: "127.0.0.1"})
	localConn.Write(payload)
	ioutil.ReadAll(localConn)
}

func (c *ClientModel) Shutdown() {
}

//调用控制器ctl的Update方法 参数为当前的model更新控制器状态。
func (c *ClientModel) update() {
	c.ctl.Update(c)
}

func (c *ClientModel) Run() {
	// how long we should wait before we reconnect 重新连接之前应该等多久
	maxWait := 30 * time.Second
	wait := 1 * time.Second

	for {
		// run the control channel 运行控制通道
		c.control()

		// control only returns when a failure has occurred, so we're going to try to reconnect
		// 控制仅在发生故障时返回，因此我们将尝试重新连接
		if c.connStatus == mvc.ConnOnline {
			wait = 1 * time.Second
		}

		log.Info("Waiting %d seconds before reconnecting", int(wait.Seconds()))
		time.Sleep(wait)
		// exponentially increase wait time 指数增加等待时间 连接失败后，重试的时间会不断增加。
		wait = 2 * wait
		wait = time.Duration(math.Min(float64(wait), float64(maxWait)))
		c.connStatus = mvc.ConnReconnecting
		c.update()
	}
}

// Establishes and manages a tunnel control connection with the server
// 建立和管理与服务器的隧道控制连接
func (c *ClientModel) control() {
	defer func() {
		if r := recover(); r != nil {
			log.Error("control recovering from failure %v", r)
		}
	}()

	// establish control channel 建立控制渠道
	// var () 批量定义变量，则不用在每个前面写var
	var (
		ctlConn conn.Conn
		err     error
	)
	if c.proxyUrl == "" {
		// simple non-proxied case, just connect to the server 简单的非代理情况，只需连接到服务器即可
		// conn.Dial conn自定义的客户端。
		ctlConn, err = conn.Dial(c.serverAddr, "ctl", c.tlsConfig)
	} else {
		//http proxy
		ctlConn, err = conn.DialHttpProxy(c.proxyUrl, c.serverAddr, "ctl", c.tlsConfig)
	}
	if err != nil {
		panic(err)
	}
	defer ctlConn.Close()

	// authenticate with the server
	// msg 是 msg包中的msg.go 组织了Auth格式的数组结构
	auth := &msg.Auth{
		ClientId:  c.id,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Version:   version.Proto,
		MmVersion: version.MajorMinor(),
		User:      c.authToken,
	}

	//把组织好的auth数据通过ctlConn写入缓冲区
	if err = msg.WriteMsg(ctlConn, auth); err != nil {
		panic(err)
	}

	// wait for the server to authenticate us
	// 等待服务器验证我们
	var authResp msg.AuthResp
	if err = msg.ReadMsgInto(ctlConn, &authResp); err != nil {
		panic(err)
	}

	// 如果验证有错，则关掉控制器
	if authResp.Error != "" {
		emsg := fmt.Sprintf("Failed to authenticate to server: %s", authResp.Error)
		c.ctl.Shutdown(emsg)
		return
	}

	c.id = authResp.ClientId //服务端返回唯一的客户端id
	c.serverVersion = authResp.MmVersion //返回服务器的版本信息。
	c.Info("Authenticated with server, client id: %v", c.id) //打印clientID
	c.update() //更新model及controller
	//保存authToken
	if err = SaveAuthToken(c.configPath, c.authToken); err != nil {
		c.Error("Failed to save auth token: %v", err)
	}

	// request tunnels 请求隧道
	//TODO 隧道配置信息由服务端返回。返回的是所有的隧道数据。用户可以通过服务端关闭某个隧道。关闭的隧道不返回。
	reqIdToTunnelConfig := make(map[string]*TunnelConfiguration)
	//遍历隧道配置，解析每个隧道配置信息, 并发送隧道请求。
	for _, config := range c.tunnelConfig {
		// create the protocol list to ask for
		var protocols []string
		for protocol, _ := range config.Protocols {
			protocols = append(protocols, protocol)
		}

		//TODO 给服务器发送创建隧道的请求 , 多隧道发送给服务端去创建。
		reqTunnel := &msg.ReqTunnel{
			ReqId:      util.RandId(8),
			Protocol:   strings.Join(protocols, "+"),
			Hostname:   config.Hostname,
			Subdomain:  config.Subdomain,
			HttpAuth:   config.HttpAuth,
			RemotePort: config.RemotePort,
		}

		// send the tunnel request 发送隧道请求
		if err = msg.WriteMsg(ctlConn, reqTunnel); err != nil {
			panic(err)
		}

		// save request id association so we know which local address
		// to proxy to later
		// 保存请求ID关联，以便我们知道以后代理哪个本地地址
		reqIdToTunnelConfig[reqTunnel.ReqId] = config
	}

	// start the heartbeat 开始心跳
	lastPong := time.Now().UnixNano()
	c.ctl.Go(func() { c.heartbeat(&lastPong, ctlConn) })

	// main control loop
	for {
		var rawMsg msg.Message
		if rawMsg, err = msg.ReadMsg(ctlConn); err != nil {
			panic(err)
		}

		switch m := rawMsg.(type) {
		case *msg.ReqProxy:
			c.ctl.Go(c.proxy) //TODO 服务端请求建立proxy连接 ReqProxy 使用控制器的go方法执行 model模块的proxy方法。

		case *msg.Pong:
			atomic.StoreInt64(&lastPong, time.Now().UnixNano())

		case *msg.NewTunnel:
			if m.Error != "" {
				emsg := fmt.Sprintf("Server failed to allocate tunnel: %s", m.Error)
				c.Error(emsg)
				c.ctl.Shutdown(emsg)
				continue
			}

			tunnel := mvc.Tunnel{
				PublicUrl: m.Url,
				LocalAddr: reqIdToTunnelConfig[m.ReqId].Protocols[m.Protocol],
				Protocol:  c.protoMap[m.Protocol],
			}

			c.tunnels[tunnel.PublicUrl] = tunnel
			c.connStatus = mvc.ConnOnline
			c.Info("Tunnel established at %v", tunnel.PublicUrl)
			c.update()

		default:
			ctlConn.Warn("Ignoring unknown control message %v ", m)
		}
	}
}

// Establishes and manages a tunnel proxy connection with the server
// 建立和管理与服务器的隧道代理连接
func (c *ClientModel) proxy() {
	var (
		remoteConn conn.Conn
		err        error
	)


	//重新建立一写client与服务端之间的代理连接。
	if c.proxyUrl == "" {
		remoteConn, err = conn.Dial(c.serverAddr, "pxy", c.tlsConfig)
	} else {
		remoteConn, err = conn.DialHttpProxy(c.proxyUrl, c.serverAddr, "pxy", c.tlsConfig)
	}

	if err != nil {
		log.Error("Failed to establish proxy connection: %v", err)
		return
	}
	defer remoteConn.Close()

	// 给服务器发送个注册clientId参数 ,创建好代理连接后，需要注册
	// TODO 注册proxy连接
	err = msg.WriteMsg(remoteConn, &msg.RegProxy{ClientId: c.id})
	if err != nil {
		remoteConn.Error("Failed to write RegProxy: %v", err)
		return
	}

	// wait for the server to ack our register
	// 等待服务器确认我们的注册
	// !!! 注册成功后
	var startPxy msg.StartProxy
	if err = msg.ReadMsgInto(remoteConn, &startPxy); err != nil {
		remoteConn.Error("Server failed to write StartProxy: %v", err)
		return
	}

	//TODO 根据startPxy.Url获取私人连接的信息。
	tunnel, ok := c.tunnels[startPxy.Url]
	if !ok {
		remoteConn.Error("Couldn't find tunnel for proxy: %s", startPxy.Url)
		return
	}

	// start up the private connection
	// 启动私人连接
	start := time.Now()
	localConn, err := conn.Dial(tunnel.LocalAddr, "prv", nil)
	if err != nil {
		remoteConn.Warn("Failed to open private leg %s: %v", tunnel.LocalAddr, err)

		if tunnel.Protocol.GetName() == "http" {
			// try to be helpful when you're in HTTP mode and a human might see the output
			// 当你处于HTTP模式并且人类可能看到输出时，尝试提供帮助
			badGatewayBody := fmt.Sprintf(BadGateway, tunnel.PublicUrl, tunnel.LocalAddr, tunnel.LocalAddr)
			remoteConn.Write([]byte(fmt.Sprintf(`HTTP/1.0 502 Bad Gateway
												Content-Type: text/html
												Content-Length: %d

												%s`, len(badGatewayBody), badGatewayBody)))
		}
		return
	}
	defer localConn.Close()

	//客户端访问本地服务成功后，使用conn.join实现双向通信。

	m := c.metrics //统计信息
	m.proxySetupTimer.Update(time.Since(start))
	m.connMeter.Mark(1)
	c.update()
	m.connTimer.Time(func() {
		localConn := tunnel.Protocol.WrapConn(localConn, mvc.ConnectionContext{Tunnel: tunnel, ClientAddr: startPxy.ClientAddr})
		bytesIn, bytesOut := conn.Join(localConn, remoteConn)
		m.bytesIn.Update(bytesIn)
		m.bytesOut.Update(bytesOut)
		m.bytesInCount.Inc(bytesIn)
		m.bytesOutCount.Inc(bytesOut)
	})
	c.update()
}

// Hearbeating to ensure our connection ngrokd is still live
// 为了确保我们的连接，ngrokd仍在继续接听
func (c *ClientModel) heartbeat(lastPongAddr *int64, conn conn.Conn) {
	lastPing := time.Unix(atomic.LoadInt64(lastPongAddr)-1, 0)
	ping := time.NewTicker(pingInterval)
	pongCheck := time.NewTicker(time.Second)

	defer func() {
		conn.Close()
		ping.Stop()
		pongCheck.Stop()
	}()

	for {
		select {
		case <-pongCheck.C:
			lastPong := time.Unix(0, atomic.LoadInt64(lastPongAddr))
			needPong := lastPong.Sub(lastPing) < 0
			pongLatency := time.Since(lastPing)

			if needPong && pongLatency > maxPongLatency {
				c.Info("Last ping: %v, Last pong: %v", lastPing, lastPong)
				c.Info("Connection stale, haven't gotten PongMsg in %d seconds", int(pongLatency.Seconds()))
				return
			}

		case <-ping.C:
			err := msg.WriteMsg(conn, &msg.Ping{})
			if err != nil {
				conn.Debug("Got error %v when writing PingMsg", err)
				return
			}
			lastPing = time.Now()
		}
	}
}
