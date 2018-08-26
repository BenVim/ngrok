package msg

import (
	"encoding/json"
	"reflect"
)

var TypeMap map[string]reflect.Type

func init() {
	TypeMap = make(map[string]reflect.Type)

	t := func(obj interface{}) reflect.Type { return reflect.TypeOf(obj).Elem() }
	TypeMap["Auth"] = t((*Auth)(nil))
	TypeMap["AuthResp"] = t((*AuthResp)(nil))
	TypeMap["ReqTunnel"] = t((*ReqTunnel)(nil))
	TypeMap["NewTunnel"] = t((*NewTunnel)(nil))
	TypeMap["RegProxy"] = t((*RegProxy)(nil))
	TypeMap["ReqProxy"] = t((*ReqProxy)(nil))
	TypeMap["StartProxy"] = t((*StartProxy)(nil))
	TypeMap["Ping"] = t((*Ping)(nil))
	TypeMap["Pong"] = t((*Pong)(nil))
}

type Message interface{}

type Envelope struct {
	Type    string
	Payload json.RawMessage
}

// When a client opens a new control channel to the server
// it must start by sending an Auth message.
// 当客户端向服务器打开新的控制通道时，它必须通过发送Auth消息开始。
type Auth struct {
	Version   string // protocol version
	MmVersion string // major/minor software version (informational only)
	User      string
	Password  string
	OS        string
	Arch      string
	ClientId  string // empty for new sessions
}

// A server responds to an Auth message with an
// AuthResp message over the control channel.
// 服务器通过控制通道响应带有AuthResp消息的Auth消息。
// If Error is not the empty string
// the server has indicated it will not accept
// the new session and will close the connection.
// 如果Error不为空，则服务器已指示它不接受新会话并将关闭连接。

// The server response includes a unique ClientId 服务器响应包括唯一的ClientID
// that is used to associate and authenticate future
// proxy connections via the same field in RegProxy messages.
// 用于通过RegProxy消息中的相同字段关联和验证将来的代理连接。
type AuthResp struct {
	Version   string
	MmVersion string
	ClientId  string
	Error     string
}

// A client sends this message to the server over the control channel
// to request a new tunnel be opened on the client's behalf.
// ReqId is a random number set by the client that it can pull
// from future NewTunnel's to correlate then to the requesting ReqTunnel.
type ReqTunnel struct {
	ReqId    string
	Protocol string

	// http only
	Hostname  string
	Subdomain string
	HttpAuth  string

	// tcp only
	RemotePort uint16
}

// When the server opens a new tunnel on behalf of
// a client, it sends a NewTunnel message to notify the client.
// ReqId is the ReqId from the corresponding ReqTunnel message.
//
// A client may receive *multiple* NewTunnel messages from a single
// ReqTunnel. (ex. A client opens an https tunnel and the server
// chooses to open an http tunnel of the same name as well)
type NewTunnel struct {
	ReqId    string
	Url      string
	Protocol string
	Error    string
}

// When the server wants to initiate a new tunneled connection, it sends
// this message over the control channel to the client. When a client receives
// this message, it must initiate a new proxy connection to the server.
type ReqProxy struct {
}

// After a client receives a ReqProxy message, it opens a new
// connection to the server and sends a RegProxy message.
type RegProxy struct {
	ClientId string
}

// This message is sent by the server to the client over a *proxy* connection before it
// begins to send the bytes of the proxied request.
type StartProxy struct {
	Url        string // URL of the tunnel this connection connection is being proxied for
	ClientAddr string // Network address of the client initiating the connection to the tunnel
}

// A client or server may send this message periodically over
// the control channel to request that the remote side acknowledge
// its connection is still alive. The remote side must respond with a Pong.
type Ping struct {
}

// Sent by a client or server over the control channel to indicate
// it received a Ping.
type Pong struct {
}
