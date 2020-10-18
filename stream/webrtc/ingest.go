// Package webrtc provides the backend to simulate a WebRTC client to send stream
package webrtc

import (
	"bufio"
	"log"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"gitlab.crans.org/nounous/ghostream/stream"
)

var (
	activeStream map[string]struct{}
)

func autoIngest(streams map[string]*stream.Stream) {
	// Regulary check existing streams
	activeStream = make(map[string]struct{})
	for {
		for name, st := range streams {
			if strings.Contains(name, "@") {
				// Not a source stream, pass
				continue
			}

			if _, ok := activeStream[name]; ok {
				// Stream is already ingested
				continue
			}

			// Start ingestion
			log.Printf("Starting webrtc for '%s'", name)
			go ingest(name, st)
		}

		// Regulary pull stream list,
		// it may be better to tweak the messaging system
		// to get an event on a new stream.
		time.Sleep(time.Second)
	}
}

func ingest(name string, input *stream.Stream) {
	// Register to get stream
	videoInput := make(chan []byte, 1024)
	input.Register(videoInput)
	activeStream[name] = struct{}{}

	// Open a UDP Listener for RTP Packets on port 5004
	videoListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5004})
	if err != nil {
		log.Printf("Faited to open UDP listener %s", err)
		return
	}
	audioListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5005})
	if err != nil {
		log.Printf("Faited to open UDP listener %s", err)
		return
	}

	// Start ffmpag to convert videoInput to video and audio UDP
	ffmpeg, err := startFFmpeg(videoInput)
	if err != nil {
		log.Printf("Error while starting ffmpeg: %s", err)
		return
	}

	// Receive video
	go func() {
		inboundRTPPacket := make([]byte, 1500) // UDP MTU
		for {
			n, _, err := videoListener.ReadFromUDP(inboundRTPPacket)
			if err != nil {
				log.Printf("Failed to read from UDP: %s", err)
				break
			}
			packet := &rtp.Packet{}
			if err := packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
				log.Printf("Failed to unmarshal RTP srtPacket: %s", err)
				continue
			}

			if videoTracks[name] == nil {
				videoTracks[name] = make([]*webrtc.Track, 0)
			}

			// Write RTP srtPacket to all video tracks
			// Adapt payload and SSRC to match destination
			for _, videoTrack := range videoTracks[name] {
				packet.Header.PayloadType = videoTrack.PayloadType()
				packet.Header.SSRC = videoTrack.SSRC()
				if writeErr := videoTrack.WriteRTP(packet); writeErr != nil {
					log.Printf("Failed to write to video track: %s", err)
					continue
				}
			}
		}
	}()

	// Receive audio
	go func() {
		inboundRTPPacket := make([]byte, 1500) // UDP MTU
		for {
			n, _, err := audioListener.ReadFromUDP(inboundRTPPacket)
			if err != nil {
				log.Printf("Failed to read from UDP: %s", err)
				break
			}
			packet := &rtp.Packet{}
			if err := packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
				log.Printf("Failed to unmarshal RTP srtPacket: %s", err)
				continue
			}

			if audioTracks[name] == nil {
				audioTracks[name] = make([]*webrtc.Track, 0)
			}

			// Write RTP srtPacket to all audio tracks
			// Adapt payload and SSRC to match destination
			for _, audioTrack := range audioTracks[name] {
				packet.Header.PayloadType = audioTrack.PayloadType()
				packet.Header.SSRC = audioTrack.SSRC()
				if writeErr := audioTrack.WriteRTP(packet); writeErr != nil {
					log.Printf("Failed to write to audio track: %s", err)
					continue
				}
			}
		}
	}()

	// Wait for stopped ffmpeg
	if err = ffmpeg.Wait(); err != nil {
		log.Printf("Faited to wait for ffmpeg: %s", err)
	}

	// Close UDP listeners
	if err = videoListener.Close(); err != nil {
		log.Printf("Faited to close UDP listener: %s", err)
	}
	if err = audioListener.Close(); err != nil {
		log.Printf("Faited to close UDP listener: %s", err)
	}
	delete(activeStream, name)
}

func startFFmpeg(in <-chan []byte) (ffmpeg *exec.Cmd, err error) {
	ffmpegArgs := []string{"-hide_banner", "-loglevel", "error", "-i", "pipe:0",
		"-an", "-vcodec", "libvpx", "-crf", "10", "-cpu-used", "5", "-b:v", "6000k", "-maxrate", "8000k", "-bufsize", "12000k", // TODO Change bitrate when changing quality
		"-qmin", "10", "-qmax", "42", "-threads", "4", "-deadline", "1", "-error-resilient", "1",
		"-auto-alt-ref", "1",
		"-f", "rtp", "rtp://127.0.0.1:5004",
		"-vn", "-acodec", "libopus", "-cpu-used", "5", "-deadline", "1", "-qmin", "10", "-qmax", "42", "-error-resilient", "1", "-auto-alt-ref", "1",
		"-f", "rtp", "rtp://127.0.0.1:5005"}
	ffmpeg = exec.Command("ffmpeg", ffmpegArgs...)

	// Handle errors output
	errOutput, err := ffmpeg.StderrPipe()
	if err != nil {
		return nil, err
	}
	go func() {
		scanner := bufio.NewScanner(errOutput)
		for scanner.Scan() {
			log.Printf("[WEBRTC FFMPEG %s] %s", "demo", scanner.Text())
		}
	}()

	// Handle stream input
	input, err := ffmpeg.StdinPipe()
	if err != nil {
		return nil, err
	}
	go func() {
		for data := range in {
			if _, err := input.Write(data); err != nil {
				log.Printf("Failed to write data to ffmpeg input: %s", err)
			}
		}

		// End of stream
		ffmpeg.Process.Kill()
	}()

	// Start process
	err = ffmpeg.Start()
	return ffmpeg, err
}
