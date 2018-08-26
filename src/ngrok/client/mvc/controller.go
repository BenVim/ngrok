package mvc

import (
	"ngrok/util"
)

type Controller interface {
	// how the model communicates that it has changed state 模型如何传达它已改变状态
	Update(State)

	// instructs the controller to shut the app down 指示控制器关闭应用程序
	Shutdown(message string)

	// PlayRequest instructs the model to play requests PlayRequest指示模型播放请求
	PlayRequest(tunnel Tunnel, payload []byte)

	// A channel of updates 更新渠道
	Updates() *util.Broadcast

	// returns the current state 返回当前状态
	State() State

	// safe wrapper for running go-routines 用于运行常规程序的安全包装器
	Go(fn func())

	// the address where the web inspection interface is running Web检查界面运行的地址
	GetWebInspectAddr() string
}
