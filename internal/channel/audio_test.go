package channel

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func buildTestOggOpus(preSkip uint16, granulePos uint64, packetSizes []int) []byte {
	var buf bytes.Buffer
	writePage := func(granule uint64, seq uint32, segments []byte, body []byte) {
		buf.WriteString("OggS")
		buf.WriteByte(0) // version
		buf.WriteByte(0) // header type
		_ = binary.Write(&buf, binary.LittleEndian, granule)
		_ = binary.Write(&buf, binary.LittleEndian, uint32(1)) // serial
		_ = binary.Write(&buf, binary.LittleEndian, seq)
		_ = binary.Write(&buf, binary.LittleEndian, uint32(0)) // crc (ignored by duration/waveform parser)
		buf.WriteByte(byte(len(segments)))
		buf.Write(segments)
		buf.Write(body)
	}

	// Page 0: OpusHead (19 bytes)
	head := make([]byte, 19)
	copy(head[0:8], "OpusHead")
	head[8] = 1 // version
	head[9] = 1 // channels
	binary.LittleEndian.PutUint16(head[10:12], preSkip)
	binary.LittleEndian.PutUint32(head[12:16], 48000)
	writePage(0, 0, []byte{byte(len(head))}, head)

	// Page 1: OpusTags
	tags := []byte("OpusTags\x04\x00\x00\x00test\x00\x00\x00\x00")
	writePage(0, 1, []byte{byte(len(tags))}, tags)

	// Page 2: Audio packets
	var segs []byte
	var body []byte
	for idx, sz := range packetSizes {
		rem := sz
		for rem >= 255 {
			segs = append(segs, 255)
			rem -= 255
		}
		segs = append(segs, byte(rem))
		for j := 0; j < sz; j++ {
			body = append(body, byte((idx*17+j*31)&0xFF))
		}
	}
	writePage(granulePos, 2, segs, body)
	return buf.Bytes()
}

func buildTestWAV16(sampleRate uint32, seconds int) []byte {
	numSamples := int(sampleRate) * seconds
	dataSize := uint32(numSamples * 2)
	byteRate := sampleRate * 2

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16)) // fmt chunk size
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // PCM
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // mono
	_ = binary.Write(&buf, binary.LittleEndian, sampleRate)
	_ = binary.Write(&buf, binary.LittleEndian, byteRate)
	_ = binary.Write(&buf, binary.LittleEndian, uint16(2))  // block align
	_ = binary.Write(&buf, binary.LittleEndian, uint16(16)) // bits per sample
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, dataSize)
	for i := 0; i < numSamples; i++ {
		// Amplitude envelope ramping up across the clip so waveform varies.
		env := float64(i) / float64(numSamples)
		sample := int16(env * 28000.0 * math.Sin(2*math.Pi*440*float64(i)/float64(sampleRate)))
		_ = binary.Write(&buf, binary.LittleEndian, sample)
	}
	return buf.Bytes()
}

func buildTestMP3(kbps128Bytes int) []byte {
	var buf bytes.Buffer
	// ID3v2 header with 0 payload bytes
	buf.Write([]byte{'I', 'D', '3', 3, 0, 0, 0, 0, 0, 0})
	// MPEG1 Layer III, 128 kbps (brIdx=9), 44.1 kHz (srIdx=0): 0xFF 0xFB 0x90 0x00
	buf.Write([]byte{0xFF, 0xFB, 0x90, 0x00})
	for i := 4; i < kbps128Bytes; i++ {
		buf.WriteByte(byte((i * 13) & 0xFF))
	}
	return buf.Bytes()
}

func TestAudioExtensionsAndMimeType(t *testing.T) {
	audioFiles := map[string]struct {
		ptt  bool
		mime string
	}{
		"voice.ogg":  {ptt: true, mime: "audio/ogg; codecs=opus"},
		"note.OPUS":  {ptt: true, mime: "audio/ogg; codecs=opus"},
		"song.mp3":   {ptt: false, mime: "audio/mpeg"},
		"clip.m4a":   {ptt: false, mime: "audio/mp4"},
		"rec.aac":    {ptt: false, mime: "audio/aac"},
		"speech.WAV": {ptt: false, mime: "audio/wav"},
	}
	for name, want := range audioFiles {
		if !IsAudioExtension(name) {
			t.Errorf("IsAudioExtension(%q) = false, want true", name)
		}
		if got := IsVoiceNoteExtension(name); got != want.ptt {
			t.Errorf("IsVoiceNoteExtension(%q) = %v, want %v", name, got, want.ptt)
		}
		if got := AudioMimeType(name); got != want.mime {
			t.Errorf("AudioMimeType(%q) = %q, want %q", name, got, want.mime)
		}
	}
	for _, nonAudio := range []string{"doc.pdf", "photo.png", "notes.txt", "archive.zip", ""} {
		if IsAudioExtension(nonAudio) {
			t.Errorf("IsAudioExtension(%q) = true, want false", nonAudio)
		}
		if IsVoiceNoteExtension(nonAudio) {
			t.Errorf("IsVoiceNoteExtension(%q) = true, want false", nonAudio)
		}
		if got := AudioMimeType(nonAudio); got != "" {
			t.Errorf("AudioMimeType(%q) = %q, want empty", nonAudio, got)
		}
	}
}

func TestAudioDurationAndWaveform(t *testing.T) {
	// OGG/Opus: preSkip=312, granule = 312 + 48000*7 => 7 seconds.
	ogg := buildTestOggOpus(312, 312+48000*7, []int{20, 45, 80, 120, 30, 95, 110, 25})
	if got := AudioDurationSeconds("voice.ogg", ogg); got != 7 {
		t.Fatalf("ogg duration = %d, want 7", got)
	}
	wfOgg := AudioWaveform(ogg)
	if len(wfOgg) != 64 {
		t.Fatalf("ogg waveform len = %d, want 64", len(wfOgg))
	}
	assertValidWaveform(t, "ogg", wfOgg, true)

	// WAV: 8000 Hz, 16-bit mono, 4 seconds.
	wav := buildTestWAV16(8000, 4)
	if got := AudioDurationSeconds("speech.wav", wav); got != 4 {
		t.Fatalf("wav duration = %d, want 4", got)
	}
	wfWav := AudioWaveform(wav)
	if len(wfWav) != 64 {
		t.Fatalf("wav waveform len = %d, want 64", len(wfWav))
	}
	assertValidWaveform(t, "wav", wfWav, true)
	if wfWav[63] <= wfWav[0] {
		t.Fatalf("expected rising WAV waveform: start=%d end=%d", wfWav[0], wfWav[63])
	}

	// MP3: 128 kbps = 16000 bytes/sec; 80000 bytes => 5 seconds.
	mp3 := buildTestMP3(80000)
	if got := AudioDurationSeconds("clip.mp3", mp3); got != 5 {
		t.Fatalf("mp3 duration = %d, want 5", got)
	}
	wfMP3 := AudioWaveform(mp3)
	if len(wfMP3) != 64 {
		t.Fatalf("mp3 waveform len = %d, want 64", len(wfMP3))
	}
	assertValidWaveform(t, "mp3", wfMP3, false)

	// Empty and malformed inputs should never panic.
	if got := AudioDurationSeconds("empty.ogg", nil); got != 0 {
		t.Fatalf("empty duration = %d, want 0", got)
	}
	if got := AudioWaveform(nil); got != nil {
		t.Fatalf("empty waveform = %v, want nil", got)
	}
	if got := AudioDurationSeconds("broken.ogg", []byte("OggS\x00truncated")); got != 0 {
		t.Fatalf("broken ogg duration = %d, want 0", got)
	}
}

func assertValidWaveform(t *testing.T, label string, wf []byte, expectVariation bool) {
	t.Helper()
	lo, hi := byte(255), byte(0)
	for i, v := range wf {
		if v > 100 {
			t.Fatalf("%s waveform[%d] = %d, exceeds 100", label, i, v)
		}
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	if expectVariation && hi <= lo {
		t.Fatalf("%s waveform has no variation (lo=%d hi=%d)", label, lo, hi)
	}
}
