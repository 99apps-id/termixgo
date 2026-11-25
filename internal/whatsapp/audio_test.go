package whatsapp

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

func makeSampleOggOpus(seconds uint64) []byte {
	var buf bytes.Buffer
	writePage := func(granule uint64, seq uint32, segs, body []byte) {
		buf.WriteString("OggS")
		buf.WriteByte(0)
		buf.WriteByte(0)
		_ = binary.Write(&buf, binary.LittleEndian, granule)
		_ = binary.Write(&buf, binary.LittleEndian, uint32(1))
		_ = binary.Write(&buf, binary.LittleEndian, seq)
		_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
		buf.WriteByte(byte(len(segs)))
		buf.Write(segs)
		buf.Write(body)
	}
	head := make([]byte, 19)
	copy(head[0:8], "OpusHead")
	head[8] = 1
	head[9] = 1
	binary.LittleEndian.PutUint16(head[10:12], 312)
	binary.LittleEndian.PutUint32(head[12:16], 48000)
	writePage(0, 0, []byte{byte(len(head))}, head)

	tags := []byte("OpusTags\x04\x00\x00\x00test\x00\x00\x00\x00")
	writePage(0, 1, []byte{byte(len(tags))}, tags)

	sizes := []int{24, 60, 96, 40, 110, 50}
	var segs, body []byte
	for i, sz := range sizes {
		segs = append(segs, byte(sz))
		for j := 0; j < sz; j++ {
			body = append(body, byte((i*29+j*7)&0xFF))
		}
	}
	writePage(312+48000*seconds, 2, segs, body)
	return buf.Bytes()
}

func TestSendFileToAudioPTTAndInline(t *testing.T) {
	oggData := makeSampleOggOpus(6)

	cases := []struct {
		name           string
		filename       string
		data           []byte
		caption        string
		wantMediaType  whatsmeow.MediaType
		wantMime       string
		wantPTT        bool
		wantSeconds    uint32
		wantWaveform   bool
		wantMsgCount   int
		wantCaptionTxt string
	}{
		{
			name:          "ogg voice note PTT",
			filename:      "reply.ogg",
			data:          oggData,
			wantMediaType: whatsmeow.MediaAudio,
			wantMime:      "audio/ogg; codecs=opus",
			wantPTT:       true,
			wantSeconds:   6,
			wantWaveform:  true,
			wantMsgCount:  1,
		},
		{
			name:           "opus voice note PTT with caption",
			filename:       "dir/note.OPUS",
			data:           oggData,
			caption:        "**Halo** ini catatan suara",
			wantMediaType:  whatsmeow.MediaAudio,
			wantMime:       "audio/ogg; codecs=opus",
			wantPTT:        true,
			wantSeconds:    6,
			wantWaveform:   true,
			wantMsgCount:   2,
			wantCaptionTxt: "*Halo* ini catatan suara",
		},
		{
			name:          "mp3 inline audio",
			filename:      "podcast.mp3",
			data:          []byte{0xFF, 0xFB, 0x90, 0x00, 1, 2, 3, 4},
			wantMediaType: whatsmeow.MediaAudio,
			wantMime:      "audio/mpeg",
			wantPTT:       false,
			wantSeconds:   1,
			wantWaveform:  false,
			wantMsgCount:  1,
		},
		{
			name:          "m4a inline audio",
			filename:      "voice.m4a",
			data:          []byte("fake-m4a-data"),
			wantMediaType: whatsmeow.MediaAudio,
			wantMime:      "audio/mp4",
			wantPTT:       false,
			wantWaveform:  false,
			wantMsgCount:  1,
		},
		{
			name:          "aac inline audio",
			filename:      "clip.aac",
			data:          []byte("fake-aac-data"),
			wantMediaType: whatsmeow.MediaAudio,
			wantMime:      "audio/aac",
			wantPTT:       false,
			wantWaveform:  false,
			wantMsgCount:  1,
		},
		{
			name:          "wav inline audio",
			filename:      "sound.wav",
			data:          []byte("fake-wav-data"),
			wantMediaType: whatsmeow.MediaAudio,
			wantMime:      "audio/wav",
			wantPTT:       false,
			wantWaveform:  false,
			wantMsgCount:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bot := New(nil, &fakeAgent{})
			var uploadedType whatsmeow.MediaType
			var uploadedData []byte
			var sent []*waE2E.Message

			bot.uploadHook = func(_ context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
				uploadedType = mediaType
				uploadedData = data
				return whatsmeow.UploadResponse{
					URL:           "https://mmg.whatsapp.net/audio/1",
					DirectPath:    "/v/t62.7117-24/1",
					MediaKey:      []byte("12345678901234567890123456789012"),
					FileEncSHA256: []byte("enc-hash"),
					FileSHA256:    []byte("raw-hash"),
					FileLength:    uint64(len(data)),
				}, nil
			}
			bot.sendMessageHook = func(_ context.Context, _ types.JID, msg *waE2E.Message) error {
				sent = append(sent, msg)
				return nil
			}

			if err := bot.SendFileTo(context.Background(), "082113487323", tc.filename, tc.data, tc.caption); err != nil {
				t.Fatalf("SendFileTo(%s): %v", tc.filename, err)
			}
			if uploadedType != tc.wantMediaType {
				t.Fatalf("uploaded mediaType = %v, want %v", uploadedType, tc.wantMediaType)
			}
			if !bytes.Equal(uploadedData, tc.data) {
				t.Fatalf("uploaded data mismatch")
			}
			if len(sent) != tc.wantMsgCount {
				t.Fatalf("sent %d messages, want %d", len(sent), tc.wantMsgCount)
			}
			audio := sent[0].GetAudioMessage()
			if audio == nil {
				t.Fatalf("expected AudioMessage, got %+v", sent[0])
			}
			if audio.GetMimetype() != tc.wantMime {
				t.Errorf("Mimetype = %q, want %q", audio.GetMimetype(), tc.wantMime)
			}
			if audio.GetPTT() != tc.wantPTT {
				t.Errorf("PTT = %v, want %v", audio.GetPTT(), tc.wantPTT)
			}
			if tc.wantSeconds > 0 && audio.GetSeconds() != tc.wantSeconds {
				t.Errorf("Seconds = %d, want %d", audio.GetSeconds(), tc.wantSeconds)
			}
			if tc.wantWaveform {
				if len(audio.GetWaveform()) != 64 {
					t.Errorf("Waveform len = %d, want 64", len(audio.GetWaveform()))
				}
				for i, b := range audio.GetWaveform() {
					if b > 100 {
						t.Errorf("Waveform[%d] = %d > 100", i, b)
					}
				}
			} else if len(audio.GetWaveform()) != 0 {
				t.Errorf("unexpected Waveform on non-PTT audio: len=%d", len(audio.GetWaveform()))
			}
			if tc.wantCaptionTxt != "" {
				if got := sent[1].GetConversation(); got != tc.wantCaptionTxt {
					t.Errorf("follow-up caption = %q, want %q", got, tc.wantCaptionTxt)
				}
			}
		})
	}
}

func TestSendFileToKeepsImageAndDocumentBranches(t *testing.T) {
	bot := New(nil, &fakeAgent{})
	var typesSeen []whatsmeow.MediaType
	var sent []*waE2E.Message

	bot.uploadHook = func(_ context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error) {
		typesSeen = append(typesSeen, mediaType)
		return whatsmeow.UploadResponse{URL: "https://mmg.whatsapp.net/x", FileLength: uint64(len(data))}, nil
	}
	bot.sendMessageHook = func(_ context.Context, _ types.JID, msg *waE2E.Message) error {
		sent = append(sent, msg)
		return nil
	}

	if err := bot.SendFileTo(context.Background(), "628123", "foto.png", []byte("png"), "cap"); err != nil {
		t.Fatalf("png: %v", err)
	}
	if err := bot.SendFileTo(context.Background(), "628123", "laporan.pdf", []byte("pdf"), "doc"); err != nil {
		t.Fatalf("pdf: %v", err)
	}
	if len(typesSeen) != 2 || typesSeen[0] != whatsmeow.MediaImage || typesSeen[1] != whatsmeow.MediaDocument {
		t.Fatalf("media types = %v, want [MediaImage MediaDocument]", typesSeen)
	}
	if sent[0].GetImageMessage() == nil || sent[1].GetDocumentMessage() == nil {
		t.Fatalf("expected ImageMessage then DocumentMessage, got %+v", sent)
	}
}
