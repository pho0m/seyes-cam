package main

import (
	"bytes"
	"encoding/base64"
	"io"
	"io/ioutil"
	"os/exec"

	"math"
	"net/url"
	"strings"
	"time"

	"github.com/deepch/vdk/format/rtmp"
	"github.com/gin-gonic/gin"

	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/format/rtspv2"
	"github.com/sirupsen/logrus"
)

// StreamServerRunStreamDo stream run do mux
func StreamServerRunStreamDo(streamID string, channelID string) {
	var status int
	defer func() {
		//TODO fix it no need unlock run if delete stream
		if status != 2 {
			Storage.StreamChannelUnlock(streamID, channelID)
		}
	}()
	for {
		baseLogger := log.WithFields(logrus.Fields{
			"module":  "core",
			"stream":  streamID,
			"channel": channelID,
			"func":    "StreamServerRunStreamDo",
		})

		baseLogger.WithFields(logrus.Fields{"call": "Run"}).Infoln("Run stream")
		opt, err := Storage.StreamChannelControl(streamID, channelID)
		if err != nil {
			baseLogger.WithFields(logrus.Fields{
				"call": "StreamChannelControl",
			}).Infoln("Exit", err)
			return
		}
		if opt.OnDemand && !Storage.ClientHas(streamID, channelID) {
			baseLogger.WithFields(logrus.Fields{
				"call": "ClientHas",
			}).Infoln("Stop stream no client")
			return
		}
		status, err = StreamServerRunStream(streamID, channelID, opt)
		if status > 0 {
			baseLogger.WithFields(logrus.Fields{
				"call": "StreamServerRunStream",
			}).Infoln("Stream exit by signal or not client")
			return
		}
		if err != nil {
			log.WithFields(logrus.Fields{
				"call": "Restart",
			}).Errorln("Stream error restart stream", err)
		}
		time.Sleep(2 * time.Second)

	}
}

// StreamServerRunStream core stream
func StreamServerRunStream(streamID string, channelID string, opt *ChannelST) (int, error) {
	if url, err := url.Parse(opt.URL); err == nil && strings.ToLower(url.Scheme) == "rtmp" {
		return StreamServerRunStreamRTMP(streamID, channelID, opt)
	}
	keyTest := time.NewTimer(20 * time.Second)
	checkClients := time.NewTimer(20 * time.Second)
	var start bool
	var fps int
	var preKeyTS = time.Duration(0)
	var Seq []*av.Packet
	RTSPClient, err := rtspv2.Dial(rtspv2.RTSPClientOptions{URL: opt.URL, InsecureSkipVerify: opt.InsecureSkipVerify, DisableAudio: true, DialTimeout: 3 * time.Second, ReadWriteTimeout: 5 * time.Second, Debug: opt.Debug, OutgoingProxy: true})
	if err != nil {
		return 0, err
	}
	Storage.StreamChannelStatus(streamID, channelID, ONLINE)
	defer func() {
		RTSPClient.Close()
		Storage.StreamChannelStatus(streamID, channelID, OFFLINE)
		Storage.StreamHLSFlush(streamID, channelID)
	}()
	var WaitCodec bool
	/*
		Example wait codec
	*/
	// var videoIDX int

	if RTSPClient.WaitCodec {
		WaitCodec = true
	} else {
		if len(RTSPClient.CodecData) > 0 {
			Storage.StreamChannelCodecsUpdate(streamID, channelID, RTSPClient.CodecData, RTSPClient.SDPRaw)
		}
		// for i, codec := range RTSPClient.CodecData {

		// 	if codec.Type().IsVideo() {
		// 		videoIDX = i
		// 	}
		// }
	}
	log.WithFields(logrus.Fields{
		"module":  "core",
		"stream":  streamID,
		"channel": channelID,
		"func":    "StreamServerRunStream",
		"call":    "Start",
	}).Infoln("Success connection RTSP")
	var ProbeCount int
	var ProbeFrame int
	var ProbePTS time.Duration
	Storage.NewHLSMuxer(streamID, channelID)
	defer Storage.HLSMuxerClose(streamID, channelID)
	for {
		select {
		//Check stream have clients
		case <-checkClients.C:
			if opt.OnDemand && !Storage.ClientHas(streamID, channelID) {
				return 1, ErrorStreamNoClients
			}
			checkClients.Reset(20 * time.Second)
		//Check stream send key
		case <-keyTest.C:
			return 0, ErrorStreamNoVideo
		//Read core signals
		case signals := <-opt.signals:
			switch signals {
			case SignalStreamStop:
				return 2, ErrorStreamStopCoreSignal
			case SignalStreamRestart:
				return 0, ErrorStreamRestart
			case SignalStreamClient:
				return 1, ErrorStreamNoClients
			}
		//Read rtsp signals
		case signals := <-RTSPClient.Signals:
			switch signals {
			case rtspv2.SignalCodecUpdate:
				Storage.StreamChannelCodecsUpdate(streamID, channelID, RTSPClient.CodecData, RTSPClient.SDPRaw)
				WaitCodec = false
			case rtspv2.SignalStreamRTPStop:
				return 0, ErrorStreamStopRTSPSignal
			}
		case packetRTP := <-RTSPClient.OutgoingProxyQueue:
			Storage.StreamChannelCastProxy(streamID, channelID, packetRTP)
		case packetAV := <-RTSPClient.OutgoingPacketQueue:
			if WaitCodec {
				continue
			}

			if packetAV.IsKeyFrame {
				keyTest.Reset(50 * time.Second)
				if preKeyTS > 0 {
					Storage.StreamHLSAdd(streamID, channelID, Seq, packetAV.Time-preKeyTS)
					Seq = []*av.Packet{}
				}
				preKeyTS = packetAV.Time
			}
			Seq = append(Seq, packetAV)
			Storage.StreamChannelCast(streamID, channelID, packetAV)
			/*
			   HLS LL Test
			*/
			if packetAV.IsKeyFrame && !start {
				start = true
			}

			// var FrameDecoderSingle *ffmpeg.VideoDecoder

			// FrameDecoderSingle, err = ffmpeg.NewVideoDecoder(RTSPClient.CodecData[videoIDX].(av.VideoCodecData))
			// if err != nil {
			// 	log.Fatalln("FrameDecoderSingle Error", err)
			// }

			// if packetAV.IsKeyFrame {
			// 	mychan1 := make(chan string, 2)
			// 	 select {
  
			// 		// Case statement
			// 		case out := <-mychan1:
			// 				fmt.Println(out)
				
			// 		// Calling After method
			// 		case <-time.After(10 * time.Second):
			// 				fmt.Println("timeout....1")
			// 	}
			// 	//sample single frame decode encode to jpeg save on disk //
			// 	if pic, err := FrameDecoderSingle.DecodeSingle(packetAV.Data); err == nil && pic != nil {
			// 		// ttt := time.Now()
			// 		path := filepath.Join("./storage" + "/output-" + streamID + "-" + ".jpg")

			// 		if out, err := os.Create(path); err == nil {
			// 			if err = jpeg.Encode(out, &pic.Image, nil); err == nil {
			// 				logrus.Print("image save !" + out.Name())
			// 			}
			// 		}
			// 	}
			// }

			/*
				FPS mode probe
			*/
			if start {
				ProbePTS += packetAV.Duration
				ProbeFrame++
				if packetAV.IsKeyFrame && ProbePTS.Seconds() >= 1 {
					ProbeCount++
					if ProbeCount == 2 {
						fps = int(math.Round(float64(ProbeFrame) / ProbePTS.Seconds()))
					}
					ProbeFrame = 0
					ProbePTS = 0
				}
			}
			if start && fps != 0 {
				//TODO fix it
				packetAV.Duration = time.Duration((float32(1000)/float32(fps))*1000*1000) * time.Nanosecond
				Storage.HlsMuxerSetFPS(streamID, channelID, fps)
				Storage.HlsMuxerWritePacket(streamID, channelID, packetAV)
			}
		}
	}
}


func StreamServerRunStreamRTMP(streamID string, channelID string, opt *ChannelST) (int, error) {
	keyTest := time.NewTimer(20 * time.Second)
	checkClients := time.NewTimer(20 * time.Second)
	OutgoingPacketQueue := make(chan *av.Packet, 1000)
	Signals := make(chan int, 100)
	var start bool
	var fps int
	var preKeyTS = time.Duration(0)
	var Seq []*av.Packet

	conn, err := rtmp.DialTimeout(opt.URL, 3*time.Second)
	if err != nil {
		return 0, err
	}

	Storage.StreamChannelStatus(streamID, channelID, ONLINE)
	defer func() {
		conn.Close()
		Storage.StreamChannelStatus(streamID, channelID, OFFLINE)
		Storage.StreamHLSFlush(streamID, channelID)
	}()
	var WaitCodec bool

	codecs, err := conn.Streams()
	if err != nil {
		return 0, err
	}
	preDur := make([]time.Duration, len(codecs))
	Storage.StreamChannelCodecsUpdate(streamID, channelID, codecs, []byte{})

	log.WithFields(logrus.Fields{
		"module":  "core",
		"stream":  streamID,
		"channel": channelID,
		"func":    "StreamServerRunStream",
		"call":    "Start",
	}).Infoln("Success connection RTSP")
	var ProbeCount int
	var ProbeFrame int
	var ProbePTS time.Duration
	Storage.NewHLSMuxer(streamID, channelID)
	defer Storage.HLSMuxerClose(streamID, channelID)

	go func() {
		for {
			ptk, err := conn.ReadPacket()
			if err != nil {
				break
			}
			OutgoingPacketQueue <- &ptk
		}
		Signals <- 1
	}()

	for {
		select {
		//Check stream have clients
		case <-checkClients.C:
			if opt.OnDemand && !Storage.ClientHas(streamID, channelID) {
				return 1, ErrorStreamNoClients
			}
			checkClients.Reset(20 * time.Second)
		//Check stream send key
		case <-keyTest.C:
			return 0, ErrorStreamNoVideo
		//Read core signals
		case signals := <-opt.signals:
			switch signals {
			case SignalStreamStop:
				return 2, ErrorStreamStopCoreSignal
			case SignalStreamRestart:
				return 0, ErrorStreamRestart
			case SignalStreamClient:
				return 1, ErrorStreamNoClients
			}
		//Read rtsp signals
		case <-Signals:
			return 0, ErrorStreamStopRTSPSignal
		case packetAV := <-OutgoingPacketQueue:
			if preDur[packetAV.Idx] != 0 {
				packetAV.Duration = packetAV.Time - preDur[packetAV.Idx]
			}

			preDur[packetAV.Idx] = packetAV.Time

			if WaitCodec {
				continue
			}

			if packetAV.IsKeyFrame {
				keyTest.Reset(20 * time.Second)
				if preKeyTS > 0 {
					Storage.StreamHLSAdd(streamID, channelID, Seq, packetAV.Time-preKeyTS)
					Seq = []*av.Packet{}
				}
				preKeyTS = packetAV.Time
			}
			Seq = append(Seq, packetAV)
			Storage.StreamChannelCast(streamID, channelID, packetAV)
			/*
			   HLS LL Test
			*/
			if packetAV.IsKeyFrame && !start {
				start = true
			}
			/*
				FPS mode probe
			*/
			if start {
				ProbePTS += packetAV.Duration
				ProbeFrame++
				if packetAV.IsKeyFrame && ProbePTS.Seconds() >= 1 {
					ProbeCount++
					if ProbeCount == 2 {
						fps = int(math.Round(float64(ProbeFrame) / ProbePTS.Seconds()))
					}
					ProbeFrame = 0
					ProbePTS = 0
				}
			}
			if start && fps != 0 {
				//TODO fix it
				packetAV.Duration = time.Duration((float32(1000)/float32(fps))*1000*1000) * time.Nanosecond
				Storage.HlsMuxerSetFPS(streamID, channelID, fps)
				Storage.HlsMuxerWritePacket(streamID, channelID, packetAV)
			}
		}
	}
}




func populateStdin(file []byte) func(io.WriteCloser) {
    return func(stdin io.WriteCloser) {
        defer stdin.Close()
        io.Copy(stdin, bytes.NewReader(file)) 
    }
}



func GetImageFromDisk(c *gin.Context) {
	streamID := c.Params.ByName("uuid")
	channelID := c.Params.ByName("channel")

	cmd :=  exec.Command("ffmpeg","-y", "-rtsp_transport", "tcp", "-i", "rtsp://202.44.35.76:5541/"+streamID+"/"+channelID, "-vframes" ,"1" ,"./storage/output-c319f57f-6db1-4ada-9ca4-f0fdb38c13f2-.jpg")

logrus.Print(cmd)
logrus.Print("starting snapshot")



err := cmd.Run()
if err != nil {
	c.IndentedJSON(500, Message{Status: 0, Payload: err.Error()})
    return 
}

	fileBytes, err := ioutil.ReadFile("./storage" + "/output-" + streamID + "-.jpg")
	if err != nil {
		panic(err)
	}

	b64 := ConvertToBase64(fileBytes)

	if err != nil {
		c.IndentedJSON(500, Message{Status: 0, Payload: err.Error()})
		return 
	}

	c.JSON(200, gin.H{
		"image": b64,
		
	})
}

// Takes bytes and returns encoded base64 string
func toBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// Takes Image and converts returns it base64 string
func ConvertToBase64(imgByte []byte) string {
	return toBase64(imgByte)
}
