package muxer

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"github.com/viderstv/common/streaming/av"
	"github.com/viderstv/common/streaming/protocol/hls"
	"github.com/viderstv/common/streaming/protocol/hls/cache"
	"github.com/viderstv/common/streaming/protocol/rtmp"
	"github.com/viderstv/common/streaming/protocol/rtmp/handler"
	"github.com/viderstv/common/structures"
	"github.com/viderstv/common/utils"
	"github.com/viderstv/muxer/src/global"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func New(gCtx global.Context) <-chan struct{} {
	done := make(chan struct{})

	mtx := sync.Mutex{}
	mp := map[string]*Stream{}
	idMp := map[string]string{}

	handler := handler.New()

	server := rtmp.New(rtmp.Config{
		Logger: logrus.New(),
		OnError: func(err error) {
			logrus.Error("error in rtmp: ", err)
		},
		OnNewStream: func(addr net.Addr) bool {
			return true
		},
		AuthStream: func(info *av.Info, addr net.Addr) bool {
			localLog := logrus.WithField("info", info.String()).WithField("addr", addr.String())

			if !info.Publisher {
				return false
			}

			if info.App == "transmux" {
				pl := structures.JwtMuxerPayload{}

				if err := structures.DecodeJwt(&pl, gCtx.Config().RTMP.Auth.JwtToken, info.Name); err != nil {
					localLog.Error("decode failed: ", err)
					return false
				}

				mtx.Lock()
				mp[info.ID] = &Stream{
					info:         *info,
					MuxerPayload: pl,
				}
				idMp[fmt.Sprintf("%s-%s", pl.StreamID.Hex(), pl.Variant.Name)] = info.ID
				mtx.Unlock()

				return true
			}

			return false
		},
		HandlePublisher: func(info av.Info, reader av.ReadCloser) {
			mtx.Lock()
			stream := mp[info.ID]
			mtx.Unlock()
			localLog := logrus.WithField("info", info.String()).WithField("stream", stream.MuxerPayload)

			ctx, cancel := context.WithCancel(gCtx)
			defer cancel()

			stream.Cache = cache.New()
			hlsHandler := hls.New(info, hls.Config{
				MinSegmentDuration: time.Second,
				Logger:             localLog,
				Cache:              stream.Cache,
			})

			handler.HandleReader(reader)
			handler.HandleWriter(hlsHandler)

			go func() {
				defer reader.Close()
				defer cancel()
				events := stream.Cache.NewItems()
				buf := bytes.NewBuffer(nil)
				for {
					select {
					case <-events:
					case <-ctx.Done():
					}
					buf.Reset()
					items := stream.Cache.Items()
					seqNum := 0
					if len(items) != 0 {
						seqNum = items[0].SeqNum()
					}
					_, _ = buf.WriteString(fmt.Sprintf("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-VERSION:4\n#EXT-X-MEDIA-SEQUENCE:%d\n", seqNum))
					previousStart := time.Time{}
					previousDuration := time.Duration(0)
					for _, item := range items {
						if item.Start().IsZero() && stream.Cache.Done() {
							break
						}
						start := item.Start()
						if start.IsZero() {
							start = previousStart.Add(previousDuration)
						}
						previousStart = start
						previousDuration = item.Duration()
						_, _ = buf.WriteString(fmt.Sprintf("#EXT-X-PROGRAM-DATE-TIME:%s\n", start.Format(time.RFC3339)))
						dur := math.Round(float64(item.Duration()/time.Millisecond)/10) / 100
						if dur == 0 {
							dur = 2
						}
						_, _ = buf.WriteString(fmt.Sprintf("#EXTINF: %.2f,live\n", dur))
						_, _ = buf.WriteString(fmt.Sprintf("%s.ts\n", item.Name()))
						if item.Start().IsZero() {
							break
						}
					}

					if stream.Cache.Done() {
						_, _ = buf.WriteString("#EXT-X-ENDLIST")
					}

					pipe := gCtx.Inst().Redis.RawClient().Pipeline()

					pipe.SetEX(gCtx, fmt.Sprintf("live-playlists:%s:%s", stream.MuxerPayload.StreamID.Hex(), stream.MuxerPayload.Variant.Name), buf.String(), time.Minute*2)
					pipe.SetEX(gCtx, fmt.Sprintf("live-playlists:%s:%s:ip", stream.MuxerPayload.StreamID.Hex(), stream.MuxerPayload.Variant.Name), gCtx.Config().Pod.IP, time.Minute*2)

					if _, err := pipe.Exec(gCtx); err != nil {
						localLog.Error("failed to set playlist in redis: ", err)
					}
					select {
					case <-ctx.Done():
						return
					default:
					}
				}
			}()

			<-reader.Running()
		},
	})

	ln, err := net.Listen("tcp", gCtx.Config().RTMP.Bind)
	if err != nil {
		logrus.Fatal("failed to listen to rtmp: ", err)
	}

	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := server.Serve(ln); err != nil {
			logrus.Fatal("failed to listen to rtmp: ", err)
		}
	}()
	fst := fasthttp.Server{
		Handler: func(ctx *fasthttp.RequestCtx) {
			// defer func() {
			// 	if err := recover(); err != nil {
			// 		logrus.Error("panic: ", err)
			// 	}
			// }()

			splits := strings.SplitN(utils.B2S(ctx.Path()), "/", 4)

			streamID, err := primitive.ObjectIDFromHex(splits[1])
			if err != nil {
				ctx.SetStatusCode(404)
				return
			}
			variant := splits[2]
			segmentName := splits[3]
			if !strings.HasSuffix(segmentName, ".ts") {
				if strings.HasSuffix(segmentName, ".m3u8") {
					val, err := gCtx.Inst().Redis.Get(ctx, fmt.Sprintf("live-playlists:%s:%s", streamID.Hex(), variant))
					if err != nil {
						logrus.Error(err)
						ctx.SetStatusCode(500)
						return
					}
					ctx.SetBodyString(val.(string))
					return
				}
				ctx.SetStatusCode(404)
				return
			}

			segmentName = strings.TrimSuffix(segmentName, ".ts")

			mtx.Lock()
			id := idMp[fmt.Sprintf("%s-%s", streamID.Hex(), variant)]
			mtx.Unlock()
			if id == "" {
				ctx.SetStatusCode(404)
				return
			}

			mtx.Lock()
			stream := mp[id]
			mtx.Unlock()
			if stream == nil {
				ctx.SetStatusCode(404)
				return
			}

			item := stream.Cache.GetItem(segmentName)
			if item == nil {
				ctx.SetStatusCode(404)
				return
			}

			reader, writer := io.Pipe()
			logrus.Info(item)
			_, err = item.AddWriter(writer)
			if err != nil {
				logrus.Error("unable to add writer: ", err)
				ctx.SetStatusCode(500)
				return
			}

			ctx.Response.Header.Set("Content-Type", "video/MP2T")
			ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
				buf := make([]byte, 4096)
				for {
					n, err := reader.Read(buf)
					if n != 0 {
						_, _ = w.Write(buf[:n])
						_ = w.Flush()
					}
					if err != nil {
						return
					}
				}
			})
		},
		GetOnly:          true,
		DisableKeepalive: true,
		IdleTimeout:      time.Second * 10,
		ReadTimeout:      time.Second * 10,
		WriteTimeout:     time.Second * 10,
		CloseOnShutdown:  true,
	}

	go func() {
		<-gCtx.Done()
		_ = server.Shutdown()
		_ = fst.Shutdown()
	}()

	go func() {
		defer wg.Done()
		if err := fst.ListenAndServe(gCtx.Config().Edge.Bind); err != nil {
			logrus.Fatal("unable to start edge: ", err)
		}
	}()

	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}
