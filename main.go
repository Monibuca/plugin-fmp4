package fmp4

import (
	"io"
	"net/http"
	"strings"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/mp4"
	. "m7s.live/engine/v4"
	"m7s.live/engine/v4/codec"
	"m7s.live/engine/v4/config"
	"m7s.live/engine/v4/track"
)

type Fmp4Config struct {
	config.HTTP
	config.Subscribe
}

type MediaContext interface {
	io.Writer
	GetSeqNumber() uint32
}

type TrackContext struct {
	TrackId  uint32
	fragment *mp4.Fragment
	ts       uint32 // 每个小片段起始时间戳
	abs      uint32 // 绝对起始时间戳
	absSet   bool   // 是否设置过abs
}

func (m *TrackContext) Push(ctx MediaContext, dt uint32, dur uint32, data []byte, flags uint32) {
	if !m.absSet {
		m.abs = dt
		m.absSet = true
	}
	dt -= m.abs
	if m.fragment != nil && dt-m.ts > 1000 {
		m.fragment.Encode(ctx)
		m.fragment = nil
	}
	if m.fragment == nil {
		m.fragment, _ = mp4.CreateFragment(ctx.GetSeqNumber(), m.TrackId)
		m.ts = dt
	}
	m.fragment.AddFullSample(mp4.FullSample{
		Data:       data,
		DecodeTime: uint64(dt),
		Sample: mp4.Sample{
			Flags: flags,
			Dur:   dur,
			Size:  uint32(len(data)),
		},
	})
}

func (c *Fmp4Config) OnEvent(event any) {
	switch event.(type) {
	case FirstConfig:
	}
}

var Fmp4Plugin = InstallPlugin(new(Fmp4Config))

type Fmp4Subscriber struct {
	Subscriber
	initSegment *mp4.InitSegment `json:"-" yaml:"-"`
	ftyp        *mp4.FtypBox
	video       TrackContext
	audio       TrackContext
	seqNumber   uint32
	avccOffset  int // mp4 写入的 avcc 的偏移量即，从第几个字节开始写入，前面是头，仅供 rtmp 协议使用
}

func (sub *Fmp4Subscriber) GetSeqNumber() uint32 {
	sub.seqNumber++
	return sub.seqNumber
}

func (sub *Fmp4Subscriber) OnEvent(event any) {
	switch v := event.(type) {
	case ISubscriber:
		sub.ftyp.Encode(sub)
		sub.initSegment.Moov.Encode(sub)
	case *track.Video:
		moov := sub.initSegment.Moov
		trackID := moov.Mvhd.NextTrackID
		moov.Mvhd.NextTrackID++
		newTrak := mp4.CreateEmptyTrak(trackID, 1000, "video", "chi")
		moov.AddChild(newTrak)
		moov.Mvex.AddChild(mp4.CreateTrex(trackID))
		sub.video.TrackId = trackID
		switch v.CodecID {
		case codec.CodecID_H264:
			sub.avccOffset = 5
			sub.ftyp = mp4.NewFtyp("isom", 0x200, []string{
				"isom", "iso2", "avc1", "mp41",
			})
			newTrak.SetAVCDescriptor("avc1", v.ParamaterSets[0:1], v.ParamaterSets[1:2], true)
		case codec.CodecID_H265:
			sub.avccOffset = 8
			sub.ftyp = mp4.NewFtyp("isom", 0x200, []string{
				"isom", "iso2", "hvc1", "mp41",
			})
			newTrak.SetHEVCDescriptor("hvc1", v.ParamaterSets[0:1], v.ParamaterSets[1:2], v.ParamaterSets[2:3], nil, true)
		case codec.CodecID_AV1:
			sub.avccOffset = 5
			sub.ftyp = mp4.NewFtyp("isom", 0x200, []string{
				"isom", "iso2", "av01", "mp41",
			})
		}
		sub.AddTrack(v)
	case *track.Audio:
		moov := sub.initSegment.Moov
		trackID := moov.Mvhd.NextTrackID
		moov.Mvhd.NextTrackID++
		newTrak := mp4.CreateEmptyTrak(trackID, 1000, "audio", "chi")
		moov.AddChild(newTrak)
		moov.Mvex.AddChild(mp4.CreateTrex(trackID))
		sub.audio.TrackId = trackID
		switch v.CodecID {
		case codec.CodecID_AAC:
			switch v.AudioObjectType {
			case 1:
				newTrak.SetAACDescriptor(aac.HEAACv1, int(v.SampleRate))
			case 2:
				newTrak.SetAACDescriptor(aac.AAClc, int(v.SampleRate))
			case 3:
				newTrak.SetAACDescriptor(aac.HEAACv2, int(v.SampleRate))
			}
		case codec.CodecID_PCMA:
			stsd := newTrak.Mdia.Minf.Stbl.Stsd
			pcma := mp4.CreateAudioSampleEntryBox("pcma",
				uint16(v.Channels),
				uint16(v.SampleSize), uint16(v.SampleRate), nil)
			stsd.AddChild(pcma)
		case codec.CodecID_PCMU:
			stsd := newTrak.Mdia.Minf.Stbl.Stsd
			pcmu := mp4.CreateAudioSampleEntryBox("pcmu",
				uint16(v.Channels),
				uint16(v.SampleSize), uint16(v.SampleRate), nil)
			stsd.AddChild(pcmu)
		}
		sub.AddTrack(v)
	case AudioFrame:
		sub.audio.Push(sub, v.AbsTime, v.DeltaTime, v.AUList.ToBytes(), mp4.SyncSampleFlags)
	case VideoFrame:
		flags := mp4.NonSyncSampleFlags
		if v.IFrame {
			flags = mp4.SyncSampleFlags
		}
		if data := v.AVCC.ToBytes(); len(data) > sub.avccOffset {
			sub.video.Push(sub, v.AbsTime, v.DeltaTime, data[sub.avccOffset:], flags)
		}
	default:
		sub.Subscriber.OnEvent(event)
	}
}

func (*Fmp4Config) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	streamPath := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".mp4")
	if r.URL.RawQuery != "" {
		streamPath += "?" + r.URL.RawQuery
	}
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Content-Type", "video/mp4")
	sub := &Fmp4Subscriber{
		initSegment: mp4.CreateEmptyInit(),
	}
	sub.initSegment.Moov.Mvhd.NextTrackID = 1

	sub.ID = r.RemoteAddr
	sub.SetIO(w)
	sub.SetParentCtx(r.Context())
	if err := Fmp4Plugin.SubscribeBlock(streamPath, sub, SUBTYPE_RAW); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}
