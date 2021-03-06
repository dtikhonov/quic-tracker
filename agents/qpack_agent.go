package agents

import (
	. "github.com/QUIC-Tracker/quic-tracker"
	"github.com/dustin/go-broadcast"
	"github.com/mpiraux/ls-qpack-go"
	"math"
)

type HTTPHeader struct {
	Name, Value string
}

type DecodedHeaders struct {
	StreamID uint64
	Headers  []HTTPHeader
}

type EncodedHeaders struct {
	StreamID uint64
	Headers  []byte
}

// The QPACK Agent is TODO
type QPACKAgent struct {
	BaseAgent
	EncoderStreamID uint64
	DecoderStreamID uint64
	DecodeHeaders   chan EncodedHeaders
	DecodedHeaders  broadcast.Broadcaster //type: DecodedHeaders
	EncodeHeaders   chan DecodedHeaders
	EncodedHeaders  broadcast.Broadcaster //type: EncodedHeaders
	encoder         *ls_qpack_go.QPackEncoder
	decoder         *ls_qpack_go.QPackDecoder
}

const (
	QPACKNoStream uint64 = math.MaxUint64
)

func (a *QPACKAgent) Run(conn *Connection) {
	a.Init("QPACKAgent", conn.SourceCID)
	a.DecodedHeaders = broadcast.NewBroadcaster(1000)
	a.EncodedHeaders = broadcast.NewBroadcaster(1000)
	a.DecodeHeaders = make(chan EncodedHeaders, 1000)
	a.EncodeHeaders = make(chan DecodedHeaders, 1000)

	incomingPackets := make(chan interface{}, 1000)
	conn.IncomingPackets.Register(incomingPackets)

	a.encoder = ls_qpack_go.NewQPackEncoder(false)
	a.decoder = ls_qpack_go.NewQPackDecoder(1024, 100)

	peerEncoderStreamId := QPACKNoStream
	peerDecoderStreamId := QPACKNoStream
	peerEncoderStream := make(chan interface{}, 1000)
	peerDecoderStream := make(chan interface{}, 1000)

	checkForDecodedHeaders := func() {
		for _, dhb := range a.decoder.DecodedHeaderBlocks() {
			if len(dhb.Headers()) > 0 {
				headers := make([]HTTPHeader, len(dhb.Headers()))
				for i, h := range dhb.Headers() {
					headers[i] = HTTPHeader{h.Name, h.Value}
				}
				a.DecodedHeaders.Submit(DecodedHeaders{dhb.StreamID, headers})
				if len(dhb.DecoderStream()) > 0 {
					conn.FrameQueue.Submit(QueuedFrame{NewStreamFrame(a.DecoderStreamID, conn.Streams.Get(a.DecoderStreamID), dhb.DecoderStream(), false), EncryptionLevelBest})
				}
				a.Logger.Printf("Submitted %d decoded headers on stream %d\n", len(headers), dhb.StreamID)
			}
		}
	}

	go func() {
		defer a.Logger.Println("Agent terminated")
		defer close(a.closed)
		for {
			select {
			case i := <-incomingPackets:
				p := i.(Packet)
				if p.PNSpace() == PNSpaceAppData {
					for _, f := range p.(Framer).GetAll(StreamType) {
						s := f.(*StreamFrame)
						if s.Offset == 0 && s.StreamId&0x2 == 2 {
							if s.StreamData[0] == 'H' {
								if peerEncoderStreamId != QPACKNoStream {
									a.Logger.Printf("Peer attempted to open another encoder stream on stream %d\n", s.StreamId)
									continue
								}
								peerEncoderStreamId = s.StreamId
								conn.Streams.Get(s.StreamId).ReadChan.Register(peerEncoderStream)
								a.Logger.Printf("Peer opened encoder stream on stream %d\n", s.StreamId)
								if s.Length > 1 {
									peerEncoderStream <- s.StreamData[1:]
								}
							} else if s.StreamData[0] == 'h' {
								if peerDecoderStreamId != QPACKNoStream {
									a.Logger.Printf("Peer attempted to open another decoder stream on stream %d\n", s.StreamId)
									continue
								}
								peerDecoderStreamId = s.StreamId
								conn.Streams.Get(s.StreamId).ReadChan.Register(peerDecoderStream)
								a.Logger.Printf("Peer opened decoder stream on stream %d\n", s.StreamId)
								if s.Length > 1 {
									peerDecoderStream <- s.StreamData[1:]
								}
							}
						}
					}
				}
			case i := <-peerEncoderStream:
				data := i.([]byte)
				if a.decoder.EncoderIn(data) {
					a.Logger.Printf("Decoder failed on encoder stream input\n")
					return
				}
				a.Logger.Printf("Fed %d bytes from the encoder stream to the decoder\n", len(data))
				checkForDecodedHeaders()
			case i := <-peerDecoderStream:
				data := i.([]byte)
				if a.encoder.DecoderIn(data) {
					a.Logger.Printf("Encoder failed on decoder stream input\n")
					return
				}
				a.Logger.Printf("Fed %d bytes from the decoder stream to the encoder\n", len(data))
				checkForDecodedHeaders()
			case e := <-a.EncodeHeaders:
				if a.encoder.StartHeaderBlock(e.StreamID, /*TODO*/ 0) {
					a.Logger.Printf("Encoder failed to start header block\n")
					return
				}
				var encStream []byte
				var encHeaders []byte
				for _, h := range e.Headers {
					es, eh := a.encoder.Encode(h.Name, h.Value)
					encStream = append(encStream, es...)
					encHeaders = append(encHeaders, eh...)
				}
				hdp := a.encoder.EndHeaderBlock()
				payload := append(hdp, encHeaders...)
				a.EncodedHeaders.Submit(EncodedHeaders{e.StreamID, payload})
				a.Logger.Printf("Encoded %d headers in %d bytes, with %d additional bytes on the encoder stream\n", len(e.Headers), len(payload), len(encStream))
				if len(encStream) > 0 {
					conn.FrameQueue.Submit(QueuedFrame{NewStreamFrame(a.EncoderStreamID, conn.Streams.Get(a.EncoderStreamID), encStream, false), EncryptionLevelBest})
					a.Logger.Printf("Enqueued %d bytes on the encoder stream\n", len(encStream))
				}
			case d := <-a.DecodeHeaders:
				ret := a.decoder.HeaderIn(d.Headers, d.StreamID)
				if ret < len(d.Headers) {
					a.Logger.Printf("Decoder is blocked and waiting for encoder input before decoding the %d bytes remaining on stream %d\n", len(d.Headers) - ret, d.StreamID)
				}
				checkForDecodedHeaders()
			case <-a.close:
				return
			}
		}
	}()

	conn.FrameQueue.Submit(QueuedFrame{NewStreamFrame(a.EncoderStreamID, conn.Streams.Get(a.EncoderStreamID), []byte{'H'}, false), EncryptionLevelBest})
	conn.FrameQueue.Submit(QueuedFrame{NewStreamFrame(a.DecoderStreamID, conn.Streams.Get(a.DecoderStreamID), []byte{'h'}, false), EncryptionLevelBest})
}
func (a *QPACKAgent) InitEncoder(headerTableSize uint, dynamicTablesize uint, maxRiskedStreams uint, opts uint32) {
	a.encoder.Init(headerTableSize, dynamicTablesize, maxRiskedStreams, opts)
	a.Logger.Printf("Encoder initialized with HTS=%d, DTS=%d, MRS=%d and opts=%d\n", headerTableSize, dynamicTablesize, maxRiskedStreams, opts)
}