package telegram

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func makeTelegramOggOpus(seconds uint64) []byte {
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
	writePage(312+48000*seconds, 2, []byte{16}, make([]byte, 16))
	return buf.Bytes()
}

func TestTelegramSendFileToRouting(t *testing.T) {
	type capturedCall struct {
		path     string
		chatID   string
		caption  string
		duration string
		field    string
		filename string
		body     []byte
	}

	var last capturedCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		last = capturedCall{
			path:     r.URL.Path,
			chatID:   r.FormValue("chat_id"),
			caption:  r.FormValue("caption"),
			duration: r.FormValue("duration"),
		}
		for field, fhs := range r.MultipartForm.File {
			if len(fhs) == 0 {
				continue
			}
			last.field = field
			last.filename = fhs[0].Filename
			f, err := fhs[0].Open()
			if err == nil {
				last.body, _ = io.ReadAll(f)
				_ = f.Close()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42}}`))
	}))
	defer server.Close()

	bot := &Bot{
		client: newClientAt("123:abc", server.URL),
		redactor: func(s string) string {
			return strings.ReplaceAll(s, "SECRET_TOKEN", "[REDACTED]")
		},
	}

	ogg := makeTelegramOggOpus(9)

	cases := []struct {
		name         string
		filename     string
		data         []byte
		caption      string
		wantEndpoint string
		wantField    string
		wantDuration string
		wantCaption  string
	}{
		{
			name:         "ogg routes to sendVoice",
			filename:     "speech.ogg",
			data:         ogg,
			caption:      "voice note SECRET_TOKEN",
			wantEndpoint: "/bot123:abc/sendVoice",
			wantField:    "voice",
			wantDuration: "9",
			wantCaption:  "voice note [REDACTED]",
		},
		{
			name:         "opus routes to sendVoice",
			filename:     "memo.OPUS",
			data:         ogg,
			wantEndpoint: "/bot123:abc/sendVoice",
			wantField:    "voice",
			wantDuration: "9",
		},
		{
			name:         "mp3 routes to sendAudio",
			filename:     "track.mp3",
			data:         []byte{0xFF, 0xFB, 0x90, 0x00, 1, 2, 3, 4},
			caption:      "audio clip",
			wantEndpoint: "/bot123:abc/sendAudio",
			wantField:    "audio",
			wantDuration: "1",
			wantCaption:  "audio clip",
		},
		{
			name:         "m4a routes to sendAudio",
			filename:     "recording.m4a",
			data:         []byte("m4a-bytes"),
			wantEndpoint: "/bot123:abc/sendAudio",
			wantField:    "audio",
			wantDuration: "",
		},
		{
			name:         "aac routes to sendAudio",
			filename:     "clip.aac",
			data:         []byte("aac-bytes"),
			wantEndpoint: "/bot123:abc/sendAudio",
			wantField:    "audio",
		},
		{
			name:         "wav routes to sendAudio",
			filename:     "sample.wav",
			data:         []byte("wav-bytes"),
			wantEndpoint: "/bot123:abc/sendAudio",
			wantField:    "audio",
		},
		{
			name:         "pdf routes to sendDocument",
			filename:     "report.pdf",
			data:         []byte("pdf-bytes"),
			caption:      "laporan",
			wantEndpoint: "/bot123:abc/sendDocument",
			wantField:    "document",
			wantCaption:  "laporan",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			last = capturedCall{}
			if err := bot.SendFileTo(context.Background(), 987654, tc.filename, tc.data, tc.caption); err != nil {
				t.Fatalf("SendFileTo(%s): %v", tc.filename, err)
			}
			if last.path != tc.wantEndpoint {
				t.Errorf("endpoint = %q, want %q", last.path, tc.wantEndpoint)
			}
			if last.field != tc.wantField {
				t.Errorf("multipart field = %q, want %q", last.field, tc.wantField)
			}
			if last.chatID != "987654" {
				t.Errorf("chat_id = %q, want 987654", last.chatID)
			}
			if last.filename != tc.filename {
				t.Errorf("filename = %q, want %q", last.filename, tc.filename)
			}
			if !bytes.Equal(last.body, tc.data) {
				t.Errorf("body mismatch")
			}
			if last.duration != tc.wantDuration {
				t.Errorf("duration = %q, want %q", last.duration, tc.wantDuration)
			}
			if last.caption != tc.wantCaption {
				t.Errorf("caption = %q, want %q", last.caption, tc.wantCaption)
			}
		})
	}
}
