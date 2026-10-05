package main

// Giao thức của bộ đo, nằm TRÊN internal/frame (trả nợ P1-1: trước đó file này
// có framer riêng `readFrame`, trùng logic mà không được fuzz nào bảo vệ).
//
// Điểm quan trọng vẫn giữ: MỌI tham số thí nghiệm nằm trong chính request, để
// `netlab -role server` ở máy A và `-role client` ở máy B đo được y hệt các thí
// nghiệm ở đây với RTT thật.
//
//	request : frame{type=typeReq,  payload = uint8 flags | uint32 svcMicros | uint32 respSize | data}
//	response: frame{type=typeResp, payload = respSize byte đệm}
//
// Trần frame, ReadFull, kiểm-trước-make: tất cả là của frame.Decoder.

import (
	"encoding/binary"
	"fmt"

	"github.com/ThaiG2Pro/edge-gate/internal/frame"
)

const (
	flagTwoWrites = 1 << 0 // server ghi header và body bằng 2 lần Write
	flagNagleOn   = 1 << 1 // server gọi SetNoDelay(false) cho connection này

	reqHeaderSize = 1 + 4 + 4 // flags + svcMicros + respSize
	maxFrameSize  = frame.DefaultMaxFrameSize

	typeReq  frame.Type = 1
	typeResp frame.Type = 2
)

type request struct {
	flags     uint8
	svcMicros uint32
	respSize  uint32
	payload   []byte
}

func (r request) encode() []byte {
	body := make([]byte, reqHeaderSize+len(r.payload))
	body[0] = r.flags
	binary.BigEndian.PutUint32(body[1:5], r.svcMicros)
	binary.BigEndian.PutUint32(body[5:9], r.respSize)
	copy(body[9:], r.payload)
	return frame.Append(make([]byte, 0, frame.HeaderSize+len(body)), frame.Frame{Type: typeReq, Payload: body})
}

func decodeRequest(f frame.Frame) (request, error) {
	if f.Type != typeReq {
		return request{}, fmt.Errorf("frame type %d, chờ typeReq", f.Type)
	}
	if len(f.Payload) < reqHeaderSize {
		return request{}, fmt.Errorf("request ngắn: %d byte", len(f.Payload))
	}
	return request{
		flags:     f.Payload[0],
		svcMicros: binary.BigEndian.Uint32(f.Payload[1:5]),
		respSize:  binary.BigEndian.Uint32(f.Payload[5:9]),
		payload:   f.Payload[9:],
	}, nil
}
