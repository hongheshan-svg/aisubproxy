package server

import "net"

// Serve 在已经打开的监听上提供服务。先监听、再 Serve，调用方能在端口确实开始监听后做事
// （例如容器内在线升级的「完成启动」确认）。
func (s *Server) Serve(ln net.Listener) error {
	return s.engine.RunListener(ln)
}
