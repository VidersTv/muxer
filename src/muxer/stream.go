package muxer

import (
	"github.com/viderstv/common/streaming/av"
	"github.com/viderstv/common/streaming/protocol/hls/cache"
	"github.com/viderstv/common/structures"
)

type Stream struct {
	info         av.Info
	Cache        *cache.Cache
	MuxerPayload structures.JwtMuxerPayload
}
