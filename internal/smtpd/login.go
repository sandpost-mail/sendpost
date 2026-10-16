package smtpd

import (
	"github.com/emersion/go-sasl"
)

// loginServer implements the SASL LOGIN mechanism, which go-sasl only ships as
// a client. Many legacy mailers still use it instead of PLAIN.
type loginServer struct {
	authenticate func(username, password string) error
	state        int
	username     string
}

const (
	loginStart = iota
	loginUsername
	loginPassword
)

var (
	loginUserPrompt = []byte("Username:")
	loginPassPrompt = []byte("Password:")
)

func newLoginServer(authenticate func(username, password string) error) sasl.Server {
	return &loginServer{authenticate: authenticate}
}

// Next implements sasl.Server. A client may send the username as an initial
// response, otherwise the server prompts for it.
func (s *loginServer) Next(response []byte) (challenge []byte, done bool, err error) {
	switch s.state {
	case loginStart:
		s.state = loginUsername
		if response == nil {
			return loginUserPrompt, false, nil
		}
		fallthrough
	case loginUsername:
		s.username = string(response)
		s.state = loginPassword
		return loginPassPrompt, false, nil
	case loginPassword:
		return nil, true, s.authenticate(s.username, string(response))
	}
	return nil, true, sasl.ErrUnexpectedClientResponse
}
