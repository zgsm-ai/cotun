package chshare

//this file exists to maintain backwards compatibility

import (
	"github.com/zgsm-ai/cotun/share/ccrypto"
	"github.com/zgsm-ai/cotun/share/cio"
	"github.com/zgsm-ai/cotun/share/cnet"
	"github.com/zgsm-ai/cotun/share/cos"
	"github.com/zgsm-ai/cotun/share/settings"
)

const (
	DetermRandIter = ccrypto.DetermRandIter
)

type (
	Config     = settings.Config
	Remote     = settings.Remote
	Remotes    = settings.Remotes
	User       = settings.User
	Users      = settings.Users
	UserIndex  = settings.UserIndex
	HTTPServer = cnet.HTTPServer
	ConnStats  = cnet.ConnCount
	Logger     = cio.Logger
)

var (
	NewDetermRand    = ccrypto.NewDetermRand
	GenerateKey      = ccrypto.GenerateKey
	FingerprintKey   = ccrypto.FingerprintKey
	Pipe             = cio.Pipe
	NewLoggerFlag    = cio.NewLoggerFlag
	NewLogger        = cio.NewLogger
	DecodeConfig     = settings.DecodeConfig
	DecodeRemote     = settings.DecodeRemote
	NewUsers         = settings.NewUsers
	NewUserIndex     = settings.NewUserIndex
	UserAllowAll     = settings.UserAllowAll
	ParseAuth        = settings.ParseAuth
	NewRWCConn       = cnet.NewRWCConn
	NewWebSocketConn = cnet.NewWebSocketConn
	NewHTTPServer    = cnet.NewHTTPServer
	GoStats          = cos.GoStats
	SleepSignal      = cos.SleepSignal
)

// EncodeConfig old version
func EncodeConfig(c *settings.Config) ([]byte, error) {
	return settings.EncodeConfig(*c), nil
}
