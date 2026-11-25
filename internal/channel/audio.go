package channel

import (
	"bytes"
	"encoding/binary"
	"math"
	"path/filepath"
	"strings"
)

// IsAudioExtension reports whether name ends with a supported chat audio
// extension that can be delivered as an inline audio player or voice note.
func IsAudioExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ogg", ".opus", ".mp3", ".m4a", ".aac", ".wav":
		return true
	}
	return false
}

// IsVoiceNoteExtension reports whether name ends with an OGG/Opus extension
// suitable for native push-to-talk (PTT) voice notes in WhatsApp and Telegram.
func IsVoiceNoteExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ogg", ".opus":
		return true
	}
	return false
}

// AudioMimeType returns the canonical MIME type for supported chat audio
// extensions, or empty string when the extension is not a recognized audio
// type. OGG/Opus returns "audio/ogg; codecs=opus" as required by WhatsApp PTT.
func AudioMimeType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ogg", ".opus":
		return "audio/ogg; codecs=opus"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".aac":
		return "audio/aac"
	case ".wav":
		return "audio/wav"
	default:
		return ""
	}
}

// AudioDurationSeconds extracts or estimates the playback duration in whole
// seconds for OGG/Opus, OGG/Vorbis, WAV, and MP3 audio bytes. Returns 0 when
// the duration cannot be determined from the container headers.
func AudioDurationSeconds(name string, data []byte) uint32 {
	if len(data) == 0 {
		return 0
	}
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case bytes.HasPrefix(data, []byte("OggS")) || ext == ".ogg" || ext == ".opus":
		if sec := oggDurationSeconds(data); sec > 0 {
			return sec
		}
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE" || ext == ".wav":
		if sec := wavDurationSeconds(data); sec > 0 {
			return sec
		}
	case ext == ".mp3" || bytes.HasPrefix(data, []byte("ID3")):
		if sec := mp3DurationSeconds(data); sec > 0 {
			return sec
		}
	}
	return 0
}

// AudioWaveform produces a 64-byte amplitude profile with values in [0, 100]
// for WhatsApp's AudioMessage.Waveform field. PCM WAV files use true sample
// RMS; OGG/Opus files use VBR packet-size energy; other formats fall back to
// payload energy buckets. Returns nil for empty input.
func AudioWaveform(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	if wf := wavWaveform(data); len(wf) == 64 {
		return wf
	}
	if wf := oggWaveform(data); len(wf) == 64 {
		return wf
	}
	return byteEnergyWaveform(data)
}

func oggDurationSeconds(data []byte) uint32 {
	var sampleRate uint64
	var preSkip uint64
	var maxGranule uint64
	hasGranule := false

	pos := 0
	for pos+27 <= len(data) {
		if string(data[pos:pos+4]) != "OggS" {
			next := bytes.Index(data[pos+1:], []byte("OggS"))
			if next < 0 {
				break
			}
			pos += 1 + next
			continue
		}
		if data[pos+4] != 0 {
			pos++
			continue
		}
		granule := binary.LittleEndian.Uint64(data[pos+6 : pos+14])
		nSeg := int(data[pos+26])
		if pos+27+nSeg > len(data) {
			break
		}
		bodyLen := 0
		for i := 0; i < nSeg; i++ {
			bodyLen += int(data[pos+27+i])
		}
		bodyStart := pos + 27 + nSeg
		bodyEnd := bodyStart + bodyLen
		if bodyEnd > len(data) {
			break
		}
		body := data[bodyStart:bodyEnd]

		if sampleRate == 0 {
			if len(body) >= 19 && string(body[0:8]) == "OpusHead" {
				// RFC 7845 Section 4: Opus granule positions are always 48 kHz.
				sampleRate = 48000
				preSkip = uint64(binary.LittleEndian.Uint16(body[10:12]))
			} else if len(body) >= 16 && body[0] == 1 && string(body[1:7]) == "vorbis" {
				sampleRate = uint64(binary.LittleEndian.Uint32(body[12:16]))
				preSkip = 0
			}
		}

		// 0xFFFFFFFFFFFFFFFF means no packet finishes on this page.
		if granule != math.MaxUint64 && granule > 0 {
			if !hasGranule || granule > maxGranule {
				maxGranule = granule
				hasGranule = true
			}
		}
		pos = bodyEnd
	}

	if sampleRate == 0 || !hasGranule || maxGranule <= preSkip {
		return 0
	}
	samples := maxGranule - preSkip
	sec := (samples + sampleRate/2) / sampleRate
	if sec == 0 {
		sec = 1
	}
	if sec > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(sec)
}

func oggWaveform(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte("OggS")) {
		return nil
	}
	var pktSizes []float64
	var curPkt int
	pos := 0
	for pos+27 <= len(data) {
		if string(data[pos:pos+4]) != "OggS" || data[pos+4] != 0 {
			break
		}
		nSeg := int(data[pos+26])
		if pos+27+nSeg > len(data) {
			break
		}
		bodyLen := 0
		for i := 0; i < nSeg; i++ {
			bodyLen += int(data[pos+27+i])
		}
		bodyStart := pos + 27 + nSeg
		bodyEnd := bodyStart + bodyLen
		if bodyEnd > len(data) {
			break
		}
		body := data[bodyStart:bodyEnd]
		isHeader := bytes.HasPrefix(body, []byte("OpusHead")) ||
			bytes.HasPrefix(body, []byte("OpusTags")) ||
			(len(body) >= 7 && (body[0] == 1 || body[0] == 3 || body[0] == 5) && string(body[1:7]) == "vorbis")

		if !isHeader {
			for i := 0; i < nSeg; i++ {
				seg := int(data[pos+27+i])
				curPkt += seg
				if seg < 255 {
					pktSizes = append(pktSizes, float64(curPkt))
					curPkt = 0
				}
			}
		}
		pos = bodyEnd
	}
	if curPkt > 0 {
		pktSizes = append(pktSizes, float64(curPkt))
	}
	if len(pktSizes) < 4 {
		return nil
	}
	return normalizeWaveformBuckets(pktSizes, 10, 95)
}

func wavDurationSeconds(data []byte) uint32 {
	_, byteRate, _, dataStart, dataLen := parseWAV(data)
	if byteRate == 0 || dataLen == 0 || dataStart < 0 {
		return 0
	}
	sec := (uint64(dataLen) + uint64(byteRate)/2) / uint64(byteRate)
	if sec == 0 {
		sec = 1
	}
	if sec > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(sec)
}

func wavWaveform(data []byte) []byte {
	audioFormat, _, bitsPerSample, dataStart, dataLen := parseWAV(data)
	if audioFormat != 1 || dataStart < 0 || dataLen < 64 {
		return nil
	}
	pcm := data[dataStart : dataStart+dataLen]
	var energies [64]float64
	switch bitsPerSample {
	case 16:
		totalSamples := len(pcm) / 2
		if totalSamples < 64 {
			return nil
		}
		for b := 0; b < 64; b++ {
			s0 := b * totalSamples / 64
			s1 := (b + 1) * totalSamples / 64
			if s1 <= s0 {
				s1 = s0 + 1
			}
			var sumSq float64
			for s := s0; s < s1; s++ {
				v := int16(binary.LittleEndian.Uint16(pcm[s*2 : s*2+2]))
				f := float64(v) / 32768.0
				sumSq += f * f
			}
			energies[b] = math.Sqrt(sumSq / float64(s1-s0))
		}
	case 8:
		totalSamples := len(pcm)
		for b := 0; b < 64; b++ {
			s0 := b * totalSamples / 64
			s1 := (b + 1) * totalSamples / 64
			if s1 <= s0 {
				s1 = s0 + 1
			}
			var sumSq float64
			for s := s0; s < s1; s++ {
				f := (float64(pcm[s]) - 128.0) / 128.0
				sumSq += f * f
			}
			energies[b] = math.Sqrt(sumSq / float64(s1-s0))
		}
	default:
		return nil
	}
	return normalizeWaveformBuckets(energies[:], 0, 100)
}

func parseWAV(data []byte) (audioFormat uint16, byteRate uint32, bitsPerSample uint16, dataStart, dataLen int) {
	dataStart = -1
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return
	}
	pos := 12
	for pos+8 <= len(data) {
		id := string(data[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		if size < 0 {
			break
		}
		chunkStart := pos + 8
		chunkEnd := chunkStart + size
		if chunkEnd > len(data) {
			chunkEnd = len(data)
		}
		switch id {
		case "fmt ":
			if chunkEnd-chunkStart >= 16 {
				audioFormat = binary.LittleEndian.Uint16(data[chunkStart : chunkStart+2])
				byteRate = binary.LittleEndian.Uint32(data[chunkStart+8 : chunkStart+12])
				bitsPerSample = binary.LittleEndian.Uint16(data[chunkStart+14 : chunkStart+16])
			}
		case "data":
			dataStart = chunkStart
			dataLen = chunkEnd - chunkStart
		}
		step := 8 + size
		if size&1 == 1 {
			step++
		}
		if step <= 0 || pos+step > len(data) {
			break
		}
		pos += step
	}
	return
}

func mp3DurationSeconds(data []byte) uint32 {
	offset := 0
	if len(data) >= 10 && string(data[0:3]) == "ID3" {
		size := int(data[6]&0x7F)<<21 | int(data[7]&0x7F)<<14 | int(data[8]&0x7F)<<7 | int(data[9]&0x7F)
		offset = 10 + size
		if data[5]&0x10 != 0 {
			offset += 10
		}
		if offset >= len(data) {
			return 0
		}
	}
	// MPEG1 Layer III bitrates (kbps) and MPEG2/2.5 Layer III bitrates (kbps).
	mpeg1L3 := [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mpeg2L3 := [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}

	limit := offset + 4096
	if limit > len(data)-4 {
		limit = len(data) - 4
	}
	for i := offset; i <= limit; i++ {
		if data[i] != 0xFF || (data[i+1]&0xE0) != 0xE0 {
			continue
		}
		ver := (data[i+1] >> 3) & 0x03
		layer := (data[i+1] >> 1) & 0x03
		brIdx := (data[i+2] >> 4) & 0x0F
		srIdx := (data[i+2] >> 2) & 0x03
		if ver == 1 || layer != 1 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
			continue
		}
		var kbps int
		if ver == 3 {
			kbps = mpeg1L3[brIdx]
		} else {
			kbps = mpeg2L3[brIdx]
		}
		if kbps <= 0 {
			continue
		}
		audioBytes := len(data) - i
		if audioBytes > 128 && string(data[len(data)-128:len(data)-125]) == "TAG" {
			audioBytes -= 128
		}
		if audioBytes <= 0 {
			return 0
		}
		bits := uint64(audioBytes) * 8
		bps := uint64(kbps) * 1000
		sec := (bits + bps/2) / bps
		if sec == 0 {
			sec = 1
		}
		if sec > math.MaxUint32 {
			return math.MaxUint32
		}
		return uint32(sec)
	}
	return 0
}

func byteEnergyWaveform(data []byte) []byte {
	const bars = 64
	buckets := make([]float64, bars)
	n := len(data)
	for b := 0; b < bars; b++ {
		s0 := b * n / bars
		s1 := (b + 1) * n / bars
		if s1 <= s0 {
			s1 = s0 + 1
		}
		if s1 > n {
			s1 = n
		}
		var acc float64
		prev := int(data[s0])
		for i := s0; i < s1; i++ {
			cur := int(data[i])
			d := cur - prev
			if d < 0 {
				d = -d
			}
			acc += float64(d)
			prev = cur
		}
		buckets[b] = acc / float64(s1-s0)
	}
	return normalizeWaveformBuckets(buckets, 10, 95)
}

func normalizeWaveformBuckets(samples []float64, minOut, maxOut int) []byte {
	const bars = 64
	out := make([]byte, bars)
	if len(samples) == 0 {
		return out
	}
	binned := make([]float64, bars)
	for b := 0; b < bars; b++ {
		s0 := b * len(samples) / bars
		s1 := (b + 1) * len(samples) / bars
		if s1 <= s0 {
			s1 = s0 + 1
		}
		if s1 > len(samples) {
			s1 = len(samples)
		}
		var sum float64
		for i := s0; i < s1; i++ {
			sum += samples[i]
		}
		binned[b] = sum / float64(s1-s0)
	}
	lo, hi := binned[0], binned[0]
	for _, v := range binned[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if hi <= 0 {
		for i := range out {
			out[i] = byte(minOut)
		}
		return out
	}
	span := hi - lo
	for i, v := range binned {
		var norm float64
		if span < 1e-9 {
			// Constant amplitude: place cleanly in mid-range.
			norm = 0.5
		} else {
			norm = (v - lo) / span
		}
		val := int(math.Round(float64(minOut) + norm*float64(maxOut-minOut)))
		if val < 0 {
			val = 0
		}
		if val > 100 {
			val = 100
		}
		out[i] = byte(val)
	}
	return out
}
