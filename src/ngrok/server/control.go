package server

import (
	"fmt"
	"io"
	"ngrok/conn"
	"ngrok/log"
	"ngrok/msg"
	"ngrok/util"
	"ngrok/version"
	"runtime/debug"
	"strings"
	"time"
)

const (
	pingTimeoutInterval = 30 * time.Second
	connReapInterval    = 10 * time.Second
	controlWriteTimeout = 10 * time.Second
	proxyStaleDuration  = 60 * time.Second
	proxyMaxPoolSize    = 10
)

type Control struct {
	// auth message
	auth *msg.Auth

	// actual connection
	conn conn.Conn

	// put a message in this channel to send it over
	// conn to the client
	out chan (msg.Message)

	// read from this channel to get the next message sent
	// to us over conn by the client
	in chan (msg.Message)

	// the last time we received a ping from the client - for heartbeats
	lastPing time.Time

	// all of the tunnels this control connection handles
	tunnels []*Tunnel

	// proxy connections
	proxies chan conn.Conn

	// identifier
	id string

	// synchronizer for controlled shutdown of writer()
	writerShutdown *util.Shutdown

	// synchronizer for controlled shutdown of reader()
	readerShutdown *util.Shutdown

	// synchronizer for controlled shutdown of manager()
	managerShutdown *util.Shutdown

	// synchronizer for controller shutdown of entire Control
	shutdown *util.Shutdown
}

func NewControl(ctlConn conn.Conn, authMsg *msg.Auth) {
	var err error

	// create the object
	c := &Control{
		auth:            authMsg,
		conn:            ctlConn,
		out:             make(chan msg.Message),
		in:              make(chan msg.Message),
		proxies:         make(chan conn.Conn, 10),
		lastPing:        time.Now(),
		writerShutdown:  util.NewShutdown(),
		readerShutdown:  util.NewShutdown(),
		managerShutdown: util.NewShutdown(),
		shutdown:        util.NewShutdown(),
	}

	//错误处理函数
	failAuth := func(e error) {
		_ = msg.WriteMsg(ctlConn, &msg.AuthResp{Error: e.Error()})
		ctlConn.Close()
	}

	// register the clientId !!!检查客户端的id 如果不存在就重新生成，生成失败，则返回
	c.id = authMsg.ClientId
	if c.id == "" {
		// it's a new session, assign an ID
		if c.id, err = util.SecureRandId(16); err != nil {
			failAuth(err)
			return
		}
	}

	// set logging prefix
	ctlConn.SetType("ctl")
	ctlConn.AddLogPrefix(c.id)

	//!!!版本不一致，则返回
	if authMsg.Version != version.Proto {
		failAuth(fmt.Errorf("Incompatible versions. Server %s, client %s. Download a new version at http://ngrok.com", version.MajorMinor(), authMsg.Version))
		return
	}

	//检查token是否存在，
	//todo 验证TOKEN是否存在，如果存在，获取其数据。发送给客户端，客户端使用该数据发起隧道创建请求。
	log.Debug("ClientId:User: %s ", authMsg.User)
	row, err := mysqlDBObj.QueryForRow("select uid,nickname from member where pwd=?", authMsg.User)
	//rows, err := mysqlDBObj.Query("select uid,nickname from member")
	if err != nil {
		failAuth(fmt.Errorf("auth error with token! %s", err))
		return
	}

	uid := 0
	nickname := ""

	//for rows.Next(){
	row.Scan(&uid, &nickname)
	log.Debug("mysqlDBObj:auth: %d %s", uid, nickname)
	//}

	//token 没有通过审核
	if uid == 0 {
		log.Debug("auth error with Token %s", authMsg.User)
		return
	}

	// register the control !!! 注册当前客户端与服务端建立的控制连接。
	if replaced := controlRegistry.Add(c.id, c); replaced != nil {
		replaced.shutdown.WaitComplete()
	}

	// start the writer first so that the following messages get sent
	// 首先启动编写器，以便发送以下消息
	go c.writer()
	// Respond to authentication

	//TODO 返回隧道的配值给客户端。
	c.out <- &msg.AuthResp{
		Version:   version.Proto,
		MmVersion: version.MajorMinor(),
		ClientId:  c.id,
	}

	// As a performance optimization, ask for a proxy connection up front
	// 作为性能优化，请事先询问代理连接
	c.out <- &msg.ReqProxy{}

	// manage the connection
	go c.manager()
	go c.reader()
	go c.stopper()
}

// Register a new tunnel on this control connection
// 在此控制器上注册新隧道
func (c *Control) registerTunnel(rawTunnelReq *msg.ReqTunnel) {

	log.Debug("create new Tunnel::::%s", rawTunnelReq)

	for _, proto := range strings.Split(rawTunnelReq.Protocol, "+") {
		tunnelReq := *rawTunnelReq
		tunnelReq.Protocol = proto

		c.conn.Debug("Registering new tunnel")
		t, err := NewTunnel(&tunnelReq, c)
		if err != nil {
			c.out <- &msg.NewTunnel{Error: err.Error()}
			if len(c.tunnels) == 0 {
				c.shutdown.Begin()
			}

			// we're done
			return
		}

		// add it to the list of tunnels
		c.tunnels = append(c.tunnels, t)

		// acknowledge success
		//TODO 通知客户端新的隧道注册成功。
		c.out <- &msg.NewTunnel{
			Url:      t.url,
			Protocol: proto,
			ReqId:    rawTunnelReq.ReqId,
		}

		rawTunnelReq.Hostname = strings.Replace(t.url, proto+"://", "", 1)
	}
}

func (c *Control) manager() {
	// don't crash on panics
	defer func() {
		if err := recover(); err != nil {
			c.conn.Info("Control::manager failed with error %v: %s", err, debug.Stack())
		}
	}()

	// kill everything if the control manager stops
	defer c.shutdown.Begin()

	// notify that manager() has shutdown
	defer c.managerShutdown.Complete()

	// reaping timer for detecting heartbeat failure
	reap := time.NewTicker(connReapInterval)
	defer reap.Stop()

	for {
		select {
		case <-reap.C:
			if time.Since(c.lastPing) > pingTimeoutInterval {
				c.conn.Info("Lost heartbeat")
				c.shutdown.Begin()
			}

			// 对接收到的消息进行分类处理
		case mRaw, ok := <-c.in:
			// c.in closes to indicate shutdown
			if !ok {
				return
			}

			switch m := mRaw.(type) {
			//TODO !!!客户端通过控制通道发送该消息到服务端请求打开新的隧道
			case *msg.ReqTunnel:
				c.registerTunnel(m)

			case *msg.Ping:
				c.lastPing = time.Now()
				c.out <- &msg.Pong{}
			}
		}
	}
}

func (c *Control) writer() {
	defer func() {
		if err := recover(); err != nil {
			c.conn.Info("Control::writer failed with error %v: %s", err, debug.Stack())
		}
	}()

	// kill everything if the writer() stops
	// 如果writer（）停止，则杀死所有内容
	defer c.shutdown.Begin()

	// notify that we've flushed all messages
	// 通知我们已刷新所有消息
	defer c.writerShutdown.Complete()

	// write messages to the control channel
	// 把c.out的数据全部发送出去，如果没有了，则阻塞，等待发送数据。
	for m := range c.out {
		c.conn.SetWriteDeadline(time.Now().Add(controlWriteTimeout))
		if err := msg.WriteMsg(c.conn, m); err != nil {
			panic(err)
		}
	}
}

func (c *Control) reader() {
	defer func() {
		if err := recover(); err != nil {
			c.conn.Warn("Control::reader failed with error %v: %s", err, debug.Stack())
		}
	}()

	// kill everything if the reader stops
	defer c.shutdown.Begin()

	// notify that we're done
	defer c.readerShutdown.Complete()

	// read messages from the control channel
	for {
		if message, err := msg.ReadMsg(c.conn); err != nil {
			if err == io.EOF {
				c.conn.Info("EOF")
				return
			} else {
				panic(err)
			}
		} else {
			// this can also panic during shutdown
			c.in <- message
		}
	}
}

func (c *Control) stopper() {
	defer func() {
		if r := recover(); r != nil {
			c.conn.Error("Failed to shut down control: %v", r)
		}
	}()

	// wait until we're instructed to shutdown
	c.shutdown.WaitBegin()

	// remove ourSelf from the control registry
	controlRegistry.Del(c.id)

	// shutdown manager() so that we have no more work to do
	close(c.in)
	c.managerShutdown.WaitComplete()

	// shutdown writer()
	close(c.out)
	c.writerShutdown.WaitComplete()

	// close connection fully
	c.conn.Close()

	// shutdown all of the tunnels
	for _, t := range c.tunnels {
		t.Shutdown()
	}

	// shutdown all of the proxy connections
	close(c.proxies)
	for p := range c.proxies {
		p.Close()
	}

	c.shutdown.Complete()
	c.conn.Info("Shutdown complete")
}

func (c *Control) RegisterProxy(conn conn.Conn) {
	conn.AddLogPrefix(c.id)

	conn.SetDeadline(time.Now().Add(proxyStaleDuration))
	select {
	case c.proxies <- conn:
		conn.Info("Registered")
	default:
		conn.Info("Proxies buffer is full, discarding.")
		conn.Close()
	}
}

// Remove a proxy connection from the pool and return it
// If not proxy connections are in the pool, request one
// and wait until it is available
// Returns an error if we couldn't get a proxy because it took too long
// or the tunnel is closing
// 从池中移除代理连接, 如果没有代理连接在池中, 请请求一个并等到它可用时返回一个错误, 如果无法获取代理, 因为它花费太长或隧道关闭

func (c *Control) GetProxy() (proxyConn conn.Conn, err error) {
	var ok bool

	// get a proxy connection from the pool
	// 从池中获取代理连接
	select {
	case proxyConn, ok = <-c.proxies:
		if !ok {
			err = fmt.Errorf("No proxy connections available, control is closing")
			return
		}
	default:
		// no proxy available in the pool, ask for one over the control channel
		// 池中没有可用的代理, 请在控制通道上选择一个
		c.conn.Debug("No proxy in pool, requesting proxy from control . . .")
		if err = util.PanicToError(func() { c.out <- &msg.ReqProxy{} }); err != nil {
			return
		}

		select {
		case proxyConn, ok = <-c.proxies:
			if !ok {
				err = fmt.Errorf("No proxy connections available, control is closing")
				return
			}

		case <-time.After(pingTimeoutInterval):
			err = fmt.Errorf("Timeout trying to get proxy connection")
			return
		}
	}
	return
}

// Called when this control is replaced by another control
// this can happen if the network drops out and the client reconnects
// before the old tunnel has lost its heartbeat
func (c *Control) Replaced(replacement *Control) {
	c.conn.Info("Replaced by control: %s", replacement.conn.Id())

	// set the control id to empty string so that when stopper()
	// calls registry.Del it won't delete the replacement
	c.id = ""

	// tell the old one to shutdown
	c.shutdown.Begin()
}
