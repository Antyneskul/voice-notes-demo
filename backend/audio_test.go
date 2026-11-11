package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func writeChunk(b *bytes.Buffer, id string, body []byte) {
	b.WriteString(id)
	binary.Write(b, binary.LittleEndian, uint32(len(body)))
	b.Write(body)
	if len(body)%2 == 1 { // RIFF chunks are padded to an even length
		b.WriteByte(0)
	}
}

// buildWAV returns a structurally valid 16 kHz mono 16-bit WAV: RIFF header, an
// optional JUNK chunk (iOS writes one first), a "fmt " chunk, then a "data"
// chunk holding audioBytes bytes. The RIFF and chunk sizes are correct.
func buildWAV(junk bool, audioBytes int) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVE")
	if junk {
		writeChunk(&b, "JUNK", make([]byte, 28))
	}
	f := make([]byte, 16)
	binary.LittleEndian.PutUint16(f[0:], 1)     // PCM
	binary.LittleEndian.PutUint16(f[2:], 1)     // mono
	binary.LittleEndian.PutUint32(f[4:], 16000) // sample rate
	binary.LittleEndian.PutUint32(f[8:], 32000) // byte rate
	binary.LittleEndian.PutUint16(f[12:], 2)    // block align
	binary.LittleEndian.PutUint16(f[14:], 16)   // bits per sample
	writeChunk(&b, "fmt ", f)
	writeChunk(&b, "data", make([]byte, audioBytes))
	out := b.Bytes()
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
	return out
}

// buildWAVWithTrailer is buildWAV plus a LIST metadata chunk after the data
// chunk, a common layout. RIFF and chunk sizes are correct.
func buildWAVWithTrailer(audioBytes int) []byte {
	b := buildWAV(false, audioBytes)
	var tail bytes.Buffer
	writeChunk(&tail, "LIST", make([]byte, 20))
	b = append(b, tail.Bytes()...)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	return b
}

// unfinalizedIOSWAV reproduces a real file left behind in the iOS Simulator when
// the app was killed mid-recording: the header iOS writes at the start (RIFF
// size 4088, data size 0, a 4008-byte FLLR padding chunk) was never updated,
// but 29562 bytes of audio were written after it.
func unfinalizedIOSWAV() []byte {
	var b bytes.Buffer
	b.WriteString("RIFF\x00\x00\x00\x00WAVE")
	writeChunk(&b, "JUNK", make([]byte, 28))
	writeChunk(&b, "fmt ", make([]byte, 16))
	writeChunk(&b, "FLLR", make([]byte, 4008))
	writeChunk(&b, "data", nil)
	b.Write(make([]byte, 29562))
	return setRIFFSize(b.Bytes(), 4088)
}

func setRIFFSize(b []byte, n uint32) []byte {
	binary.LittleEndian.PutUint32(b[4:], n)
	return b
}

func setDataSize(b []byte, n uint32) []byte {
	i := bytes.Index(b, []byte("data"))
	binary.LittleEndian.PutUint32(b[i+4:], n)
	return b
}

// truncatedWAV is a valid recording cut short mid-upload: the RIFF size field
// still describes the full file.
func truncatedWAV() []byte {
	b := buildWAV(true, 1000)
	return b[:len(b)-500]
}

func syncsafe(n int) []byte {
	return []byte{byte(n>>21) & 0x7f, byte(n>>14) & 0x7f, byte(n>>7) & 0x7f, byte(n) & 0x7f}
}

// id3MP3 builds an ID3v2 header, `tagSize` bytes of tag body, an optional
// footer, then `frame` followed by padding.
func id3MP3(tagSize int, footer bool, frame []byte) []byte {
	flags := byte(0)
	if footer {
		flags = 0x10
	}
	b := append([]byte("ID3\x04\x00"), flags)
	b = append(b, syncsafe(tagSize)...)
	b = append(b, make([]byte, tagSize)...)
	if footer {
		b = append(b, make([]byte, 10)...)
	}
	b = append(b, frame...)
	return append(b, make([]byte, 64)...)
}

var (
	layer3Frame = []byte{0xFF, 0xFB, 0x90, 0x00} // MPEG-1 Layer III, the common MP3 frame
	mpeg2Layer3 = []byte{0xFF, 0xF3, 0x90, 0x50} // MPEG-2 Layer III, as in a real file on macOS
	layer2Frame = []byte{0xFF, 0xFD, 0x90, 0x00} // MPEG-1 Layer II
	layer1Frame = []byte{0xFF, 0xFF, 0x90, 0x00} // MPEG-1 Layer I
	adtsFrame   = []byte{0xFF, 0xF1, 0x50, 0x80} // AAC in an ADTS stream: same sync bits, layer bits 00
)

func TestDetectAudioFormat(t *testing.T) {
	notSyncsafe := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x80"), make([]byte, 200)...)

	// RIFF + a fmt chunk and nothing else (no data chunk at all).
	noDataChunk := setRIFFSize(buildWAV(false, 0)[:36], 28)
	// A data chunk that comes before the fmt chunk.
	var dataFirst bytes.Buffer
	dataFirst.WriteString("RIFF\x00\x00\x00\x00WAVE")
	writeChunk(&dataFirst, "data", make([]byte, 100))
	writeChunk(&dataFirst, "fmt ", make([]byte, 16))
	// A chunk whose declared size runs past the end of the file.
	var runsPastEnd bytes.Buffer
	runsPastEnd.WriteString("RIFF\x00\x00\x00\x00WAVE")
	runsPastEnd.WriteString("JUNK\x88\x13\x00\x00")
	runsPastEnd.Write(make([]byte, 100))

	cases := []struct {
		name string
		data []byte
		want string // "" means unsupported
	}{
		// WAV that must be accepted
		{"wav in the shape iOS records (JUNK chunk first)", buildWAV(true, 1000), "wav"},
		{"wav with fmt chunk first", buildWAV(false, 1000), "wav"},
		{"wav with an odd number of audio bytes (padded chunk)", buildWAV(false, 1001), "wav"},
		{"wav with extra data after the declared size", append(buildWAV(true, 1000), make([]byte, 500)...), "wav"},
		{"wav with a zero RIFF size (streaming placeholder)", setRIFFSize(buildWAV(false, 1000), 0), "wav"},
		{"wav with 0xFFFFFFFF RIFF and data sizes (streaming placeholder)", setDataSize(setRIFFSize(buildWAV(false, 1000), 0xFFFFFFFF), 0xFFFFFFFF), "wav"},
		{"wav whose RIFF and data sizes are both 0 placeholders, audio follows", setDataSize(setRIFFSize(buildWAV(false, 1000), 0), 0), "wav"},
		{"wav with a metadata chunk after a non-empty data chunk", buildWAVWithTrailer(1000), "wav"},

		// WAV that must be rejected
		{"wav header only", []byte("RIFF\x04\x00\x00\x00WAVE"), ""},
		{"wav with an empty data chunk (header and no audio)", buildWAV(false, 0), ""},
		{"wav with an empty data chunk after JUNK", buildWAV(true, 0), ""},
		{"wav with an empty data chunk followed by a metadata chunk", buildWAVWithTrailer(0), ""},
		{"wav whose data chunk declares more audio than is present (RIFF size is a placeholder)", setDataSize(setRIFFSize(buildWAV(false, 1000), 0), 5000), ""},
		{"wav whose data chunk declares more audio than is present (RIFF size consistent)", setDataSize(buildWAV(false, 1000), 5000), ""},
		{"wav never finalized: start-of-recording header, audio written after it (real iOS file)", unfinalizedIOSWAV(), ""},
		{"wav with a real RIFF size, a data size of 0, and bytes after it", setDataSize(buildWAV(false, 1000), 0), ""},
		{"wav cut off inside the fmt chunk", setRIFFSize(buildWAV(false, 100)[:30], 0), ""},
		{"wav truncated: declared size is larger than the file", truncatedWAV(), ""},
		{"wav with no data chunk", noDataChunk, ""},
		{"wav with the data chunk before fmt", dataFirst.Bytes(), ""},
		{"wav with a chunk that runs past the end of the file", runsPastEnd.Bytes(), ""},
		{"riff but not wave (avi)", append([]byte("RIFF\x24\x08\x00\x00AVI LIST"), make([]byte, 100)...), ""},

		// MP3
		{"mp3: ID3 tag then a Layer III frame", id3MP3(200, false, layer3Frame), "mp3"},
		{"mp3: ID3 tag then an MPEG-2 Layer III frame (real file shape)", id3MP3(6312, false, mpeg2Layer3), "mp3"},
		{"mp3: ID3v2.4 tag with a footer", id3MP3(200, true, layer3Frame), "mp3"},
		{"mp3: raw Layer III frame, no tag", append(append([]byte{}, layer3Frame...), make([]byte, 100)...), "mp3"},
		{"ID3 header only", []byte("ID3\x04\x00\x00\x00\x00\x00\x00"), ""},
		{"ID3 only, tag size larger than the file", append([]byte("ID3\x04\x00\x00\x00\x00\x7f\x7f"), make([]byte, 50)...), ""},
		{"ID3 tag followed by no frame", id3MP3(200, false, make([]byte, 4)), ""},
		{"ID3 size field is not syncsafe", notSyncsafe, ""},
		{"ID3 tag then a Layer II frame", id3MP3(200, false, layer2Frame), ""},
		{"ID3 tag then a Layer I frame", id3MP3(200, false, layer1Frame), ""},
		{"raw Layer II frame", append(append([]byte{}, layer2Frame...), make([]byte, 100)...), ""},
		{"aac adts stream", append(append([]byte{}, adtsFrame...), make([]byte, 100)...), ""},

		// not audio we accept
		{"m4a/aac in mp4 container", append([]byte("\x00\x00\x00\x20ftypM4A "), make([]byte, 100)...), ""},
		{"caf container", append([]byte("caff\x00\x01\x00\x00\x00\x00\x00\x00"), make([]byte, 100)...), ""},
		{"plain text", []byte("hello, this is not audio, just a longer line of text to pass any length check"), ""},
		{"too short", []byte("RIFF"), ""},
		{"empty", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := detectAudioFormat(tc.data)
			if tc.want == "" {
				if ok {
					t.Fatalf("want unsupported, got %q", got)
				}
				return
			}
			if !ok || got != tc.want {
				t.Fatalf("got (%q, %v), want %q", got, ok, tc.want)
			}
		})
	}
}
