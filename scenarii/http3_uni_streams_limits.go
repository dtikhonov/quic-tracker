package scenarii

import (
	qt "github.com/QUIC-Tracker/quic-tracker"
	"github.com/QUIC-Tracker/quic-tracker/agents"
	"time"
)

const (
	H3USFC_TLSHandshakeFailed        = 1
	H3USFC_RequestTimeout            = 2
	H3USFC_NotEnoughStreamsAvailable = 3
	H3USFC_StreamIDError             = 4
)

type HTTP3UniStreamsLimitsScenario struct {
	AbstractScenario
}

func NewHTTP3UniStreamsLimitsScenario() *HTTP3UniStreamsLimitsScenario {
	return &HTTP3UniStreamsLimitsScenario{AbstractScenario{"http3_uni_streams_limits", 1, false, nil}}
}
func (s *HTTP3UniStreamsLimitsScenario) Run(conn *qt.Connection, trace *qt.Trace, preferredUrl string, debug bool) {
	s.timeout = time.NewTimer(10 * time.Second)
	conn.TLSTPHandler.MaxUniStreams = 1

	connAgents := s.CompleteHandshake(conn, trace, H3USFC_TLSHandshakeFailed)
	if connAgents == nil {
		return
	}
	defer connAgents.CloseConnection(false, 0, "")

	if conn.TLSTPHandler.ReceivedParameters.MaxUniStreams < 3 || conn.TLSTPHandler.ReceivedParameters.MaxBidiStreams == 0 {
		trace.ErrorCode = H3USFC_NotEnoughStreamsAvailable
		trace.Results["max_uni_streams"] = conn.TLSTPHandler.ReceivedParameters.MaxUniStreams
		trace.Results["max_bidi_streams"] = conn.TLSTPHandler.ReceivedParameters.MaxBidiStreams
		return
	}

	http := agents.HTTPAgent{}
	connAgents.Add(&http)
	responseReceived := make(chan interface{}, 1000)
	http.HTTPResponseReceived.Register(responseReceived)

	http.SendRequest(preferredUrl, "GET", trace.Host, nil)

	select {
	case <-responseReceived:
		trace.ErrorCode = 0
		<-s.Timeout().C
	case <-s.Timeout().C:
		trace.ErrorCode = H3USFC_RequestTimeout
	}

	if conn.Streams.NumberOfServerStreamsOpen() > 1 {
		trace.ErrorCode = H3USFC_StreamIDError
	}
}
