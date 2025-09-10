package chserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	chshare "github.com/zgsm-ai/cotun/share"
	"github.com/zgsm-ai/cotun/share/cnet"
	"github.com/zgsm-ai/cotun/share/settings"
	"github.com/zgsm-ai/cotun/share/tunnel"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sync/errgroup"
)

// handleClientHandler is the main http websocket handler for the cotun server
func (s *Server) handleClientHandler(w http.ResponseWriter, r *http.Request) {
	//websockets upgrade AND has cotun prefix
	upgrade := strings.ToLower(r.Header.Get("Upgrade"))
	protocol := r.Header.Get("Sec-WebSocket-Protocol")
	if upgrade == "websocket" {
		if protocol == chshare.ProtocolVersion {
			s.handleWebsocket(w, r)
			return
		}
		//print into server logs and silently fall-through
		s.Infof("ignored client connection using protocol '%s', expected '%s'",
			protocol, chshare.ProtocolVersion)
	}
	//proxy target was provided
	if s.reverseProxy != nil {
		s.reverseProxy.ServeHTTP(w, r)
		return
	}
	//no proxy defined, provide access to health/version checks
	switch r.URL.Path {
	case "/health":
		w.Write([]byte("OK\n"))
		return
	case "/version":
		w.Write([]byte(chshare.BuildVersion))
		return
	}
	//missing :O
	w.WriteHeader(404)
	w.Write([]byte("Not found"))
}

// handleWebsocket is responsible for handling the websocket connection
func (s *Server) handleWebsocket(w http.ResponseWriter, req *http.Request) {
	id := atomic.AddInt32(&s.sessCount, 1)
	l := s.Fork("session#%d", id)

	// 从HTTP请求头获取客户端信息
	clientId := req.Header.Get("X-Client-Id")
	appName := req.Header.Get("X-App-Name")
	userId := req.Header.Get("X-User-Id")
	clientPortStr := req.Header.Get("X-Client-Port")
	mappingPortStr := req.Header.Get("X-Mapping-Port")

	// 验证客户端信息是否完整
	if clientId == "" || appName == "" || userId == "" || clientPortStr == "" || mappingPortStr == "" {
		l.Infof("Missing required client headers: clientId=%s, appName=%s, userId=%s, clientPort=%s,mappingPort=%s",
			clientId, appName, userId, clientPortStr, mappingPortStr)
		rError(w, http.StatusForbidden, "Missing required client identification headers")
		return
	}
	clientPort, err := strconv.Atoi(clientPortStr)
	if err != nil {
		l.Infof("Invalid fields: clientPort=%s", clientPortStr)
		rError(w, http.StatusBadRequest, "Invalid fields")
		return
	}
	mappingPort, err := strconv.Atoi(mappingPortStr)
	if err != nil {
		l.Infof("Invalid fields: mappingPort=%s", mappingPortStr)
		rError(w, http.StatusBadRequest, "Invalid fields")
		return
	}
	// 查询allocator验证该客户端是否已分配端口
	alloc, err := s.allocator.ApplyPort(clientId, userId, appName, clientPort, mappingPort)
	if err != nil {
		l.Infof("Client not authorized: clientId=%s, appName=%s, userId=%s, clientPort=%s, mappingPort=%s, error=%v", clientId, appName, userId, clientPortStr, mappingPortStr, err)
		rError(w, http.StatusForbidden, "Client not authorized - no port allocated")
		return
	}
	alloc.Status = Connected

	l.Infof("Client authorized: clientId=%s, appName=%s, userId=%s, clientPort=%s, mappingPort=%s", clientId, appName, userId, clientPortStr, mappingPortStr)

	wsConn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		l.Debugf("Failed to upgrade (%s)", err)
		return
	}
	conn := cnet.NewWebSocketConn(wsConn)
	// perform SSH handshake on net.Conn
	l.Debugf("Handshaking with %s...", req.RemoteAddr)
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.sshConfig)
	if err != nil {
		s.Debugf("Failed to handshake (%s)", err)
		return
	}
	// pull the users from the session map
	var user *settings.User
	if s.users.Len() > 0 {
		sid := string(sshConn.SessionID())
		u, ok := s.sessions.Get(sid)
		if !ok {
			panic("bug in ssh auth handler")
		}
		user = u
		s.sessions.Del(sid)
	}
	// cotun server handshake (reverse of client handshake)
	// verify configuration
	l.Debugf("Verifying configuration")
	// wait for request, with timeout
	var r *ssh.Request
	select {
	case r = <-reqs:
	case <-time.After(settings.EnvDuration("CONFIG_TIMEOUT", 10*time.Second)):
		l.Debugf("Timeout waiting for configuration")
		sshConn.Close()
		return
	}
	failed := func(err error) {
		l.Debugf("Failed: %s", err)
		r.Reply(false, []byte(err.Error()))
	}
	if r.Type != "config" {
		failed(s.Errorf("expecting config request"))
		return
	}
	c, err := settings.DecodeConfig(r.Payload)
	if err != nil {
		failed(s.Errorf("invalid config"))
		return
	}
	//print if client and server  versions dont match
	cv := strings.TrimPrefix(c.Version, "v")
	if cv == "" {
		cv = "<unknown>"
	}
	sv := strings.TrimPrefix(chshare.BuildVersion, "v")
	if cv != sv {
		l.Infof("Client version (%s) differs from server version (%s)", cv, sv)
	}
	//validate remotes
	for _, r := range c.Remotes {
		//if user is provided, ensure they have
		//access to the desired remotes
		if user != nil {
			addr := r.UserAddr()
			if !user.HasAccess(addr) {
				failed(s.Errorf("access to '%s' denied", addr))
				return
			}
		}
		//confirm reverse tunnels are allowed
		if r.Reverse && !s.config.Reverse {
			l.Debugf("Denied reverse port forwarding request, please enable --reverse")
			failed(s.Errorf("Reverse port forwaring not enabled on server"))
			return
		}
		//confirm reverse tunnel is available
		if r.Reverse && !r.CanListen() {
			failed(s.Errorf("Server cannot listen on %s", r.String()))
			return
		}
	}
	//successfuly validated config!
	r.Reply(true, nil)
	//tunnel per ssh connection
	tunnel := tunnel.New(tunnel.Config{
		Logger:    l,
		Inbound:   s.config.Reverse,
		Outbound:  true, //server always accepts outbound
		Socks:     s.config.Socks5,
		KeepAlive: s.config.KeepAlive,
	})
	//bind
	eg, ctx := errgroup.WithContext(req.Context())
	eg.Go(func() error {
		//connected, handover ssh connection for tunnel to use, and block
		return tunnel.BindSSH(ctx, sshConn, reqs, chans)
	})
	eg.Go(func() error {
		//connected, setup reversed-remotes?
		serverInbound := c.Remotes.Reversed(true)
		if len(serverInbound) == 0 {
			return nil
		}
		//block
		return tunnel.BindRemotes(ctx, serverInbound)
	})
	err = eg.Wait()
	if err != nil && !strings.HasSuffix(err.Error(), "EOF") {
		l.Debugf("Closed connection (%s)", err)
	} else {
		l.Debugf("Closed connection")
	}
	alloc.Status = Allocated
}

// handleControlPlaneHandler 处理控制面API请求
func (s *Server) handleControlPlaneHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	method := r.Method

	paths := strings.Split(r.URL.Path, "/")
	if len(paths) < 5 {
		rError(w, http.StatusNotFound, "API endpoint not found")
		return
	}
	if paths[1] != "tunnel-manager" && paths[1] != "cotun" {
		rError(w, http.StatusNotFound, "API endpoint not found")
		return
	}
	if paths[2] != "api" || paths[3] != "v1" || paths[4] != "ports" {
		rError(w, http.StatusNotFound, "API endpoint not found")
		return
	}
	switch {
	case method == "GET":
		s.handleGetPorts(w, r)
	case method == "POST":
		s.handleCreatePort(w, r)
	case method == "DELETE":
		s.handleDeletePort(w, r)
	default:
		rError(w, http.StatusNotFound, "API endpoint not found")
	}
}

func (s *Server) getUserId(r *http.Request) string {
	// Get Authorization header
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	// Check if the header has Bearer prefix
	tokenString := authHeader
	if strings.HasPrefix(authHeader, "Bearer ") {
		tokenString = authHeader[7:] // Remove "Bearer " prefix
	}

	// Parse token without verification (for now)
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, jwt.MapClaims{})
	if err != nil {
		return ""
	}

	// Extract claims
	if claims, ok := token.Claims.(jwt.MapClaims); ok {
		// Extract user_id from claims
		if userID, exists := claims["id"]; exists {
			// Set user_id in request header
			return toString(userID)
		}
	}
	return ""
}

func rJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}

func rError(w http.ResponseWriter, statusCode int, err string) {
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(map[string]string{
		"error": err,
	})
}

/**
 * Convert interface value to string
 * @param {interface{}} v - Value to convert
 * @returns {string} String representation of the value
 * @description
 * - Handles different types: string, float64 (from JSON numbers), etc.
 * - Returns empty string for unsupported types
 */
func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%.0f", val)
	case int:
		return fmt.Sprintf("%d", val)
	case int64:
		return fmt.Sprintf("%d", val)
	default:
		return ""
	}
}

type PortQueryResponse struct {
	MappingPort int `json:"mappingPort"`
}

// handleGetPorts 获取所有端口信息
func (s *Server) handleGetPorts(w http.ResponseWriter, r *http.Request) {
	clientId := r.URL.Query().Get("clientid")
	appName := r.URL.Query().Get("appname")
	userId := r.URL.Query().Get("userid")
	paths := strings.Split(r.URL.Path, "/")
	//	/cotun/api/v1/ports/{client}/{app}
	if len(paths) == 7 {
		clientId = paths[5]
		appName = paths[6]
	}
	if userId == "" {
		userId = s.getUserId(r)
	}

	ports := s.allocator.QueryPorts(clientId, userId, appName)
	if clientId != "" && appName != "" && userId != "" {
		if len(ports) == 0 {
			s.Errorf("Port mapping not found: clientId=%s,userId=%s,appName=%s", clientId, userId, appName)
			rError(w, 404, "Port mapping not found")
			return
		}
		s.Infof("Port query: clientId=%s,userId=%s,appName=%s, port=%d", clientId, userId, appName, ports[0].MappingPort)
		rJSON(w, 200, PortQueryResponse{
			MappingPort: ports[0].MappingPort,
		})
		return
	}
	s.Infof("Port query: clientId=%s,userId=%s,appName=%s, ports=%+v", clientId, userId, appName, ports)
	rJSON(w, http.StatusOK, ports)
}

// handleCreatePort 创建新端口
func (s *Server) handleCreatePort(w http.ResponseWriter, r *http.Request) {
	var req PortAllocationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.Errorf("Invalid request body")
		rError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if req.UserId == "" {
		req.UserId = s.getUserId(r)
	}

	if req.ClientId == "" || req.AppName == "" || req.UserId == "" {
		s.Errorf("Missing required fields: %+v", req)
		rError(w, http.StatusBadRequest, "Missing required fields")
		return
	}
	ret, err := s.allocator.AllocatePort(req.ClientId, req.UserId, req.AppName, req.ClientPort)
	if err != nil {
		s.Errorf("No available ports: %+v", req)
		rError(w, http.StatusInsufficientStorage, "No available ports")
		return
	}
	s.Infof("Allocate port: %+v", req)
	rJSON(w, http.StatusCreated, ret)
}

// handleDeletePort 删除端口
func (s *Server) handleDeletePort(w http.ResponseWriter, r *http.Request) {
	clientID := r.URL.Query().Get("clientid")
	appName := r.URL.Query().Get("appname")
	userId := s.getUserId(r)

	if clientID == "" || appName == "" || userId == "" {
		s.Errorf("Missing parameters: clientId=%s,userId=%s,appName=%s", r.URL.Path, clientID, userId, appName)
		rError(w, http.StatusBadRequest, "Missing clientid or appname parameters")
		return
	}
	ports := s.allocator.ReleasePort(clientID, userId, appName)

	if len(ports) == 0 {
		s.Errorf("Port not found: clientId=%s,userId=%s,appName=%s", clientID, userId, appName)
		rError(w, http.StatusNotFound, "Port not found")
		return
	}
	s.Infof("Port removed: clientId=%s,appName=%s,userId=%s, ports=%+v", clientID, appName, userId, ports)

	rJSON(w, http.StatusOK, "Port deleted successfully")
}
