package main

import (
	"bytes"
	"encoding/binary"
)

// detectAudioFormat identifies WAV and MP3 from the file's bytes. We sniff
// instead of trusting the filename or Content-Type because the client
// controls both, and OpenAI needs the real format in `input_audio.format`.
//
// This is a sanity check, not a decoder: it rejects the obvious mistakes
// (wrong container, truncated upload, header with no audio behind it) so they
// fail fast as 415. A file that passes but still can't be decoded is rejected
// by OpenAI and surfaces as a 502.
func detectAudioFormat(b []byte) (format string, ok bool) {
	switch {
	case isWAV(b):
		return "wav", true
	case isMP3(b):
		return "mp3", true
	}
	return "", false
}

func isWAV(b []byte) bool {
	if len(b) < 12 || !bytes.Equal(b[0:4], []byte("RIFF")) || !bytes.Equal(b[8:12], []byte("WAVE")) {
		return false
	}
	// The RIFF size field is the file length minus 8. If it claims more than
	// we received, the upload was cut short. 0 and 0xFFFFFFFF are placeholders
	// that streaming recorders write, and bytes after the declared size are
	// legal, so both are accepted.
	size := binary.LittleEndian.Uint32(b[4:8])
	riffUnknown := size == 0 || size == 0xFFFFFFFF
	if !riffUnknown && uint64(size)+8 > uint64(len(b)) {
		return false
	}

	// Walk the chunks after the 12-byte header. A usable file has a "fmt "
	// chunk and then a "data" chunk with audio behind it. The order of other
	// chunks is not checked: iOS writes a "JUNK" chunk before "fmt ".
	sawFmt := false
	for pos := 12; pos+8 <= len(b); {
		id := string(b[pos : pos+4])
		n := uint64(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			sawFmt = true
		case "data":
			if !sawFmt {
				return false
			}
			// A size of 0xFFFFFFFF, or 0 in a file whose RIFF size is also a
			// placeholder, marks a streamed file of unknown length: the audio
			// runs to the end of the file.
			if n == 0xFFFFFFFF || (n == 0 && riffUnknown) {
				return body < len(b)
			}
			// Otherwise the declared length is real: it must be non-empty and
			// entirely present. Chunks after the audio (LIST metadata) are fine.
			return n > 0 && uint64(body)+n <= uint64(len(b))
		}
		next := uint64(body) + n + n&1 // chunks are padded to an even length
		if next > uint64(len(b)) {
			return false
		}
		pos = int(next)
	}
	return false
}

func isMP3(b []byte) bool {
	if len(b) >= 3 && bytes.Equal(b[0:3], []byte("ID3")) {
		// An ID3v2 tag is a 10-byte header ("ID3", version, flags, then a
		// 4-byte size with 7 bits per byte), the tag body, and an optional
		// 10-byte footer. The audio frames start right after it.
		if len(b) < 10 || (b[6]|b[7]|b[8]|b[9])&0x80 != 0 {
			return false
		}
		size := int(b[6])<<21 | int(b[7])<<14 | int(b[8])<<7 | int(b[9])
		start := 10 + size
		if b[5]&0x10 != 0 {
			start += 10
		}
		return start+4 <= len(b) && isMP3Frame(b[start:])
	}
	return len(b) >= 4 && isMP3Frame(b)
}

// isMP3Frame checks a frame header: 11 sync bits, a valid MPEG version, and
// Layer III (layer bits 01). OpenAI's "mp3" is Layer III; Layers I and II share
// the sync bits but are not MP3, and ADTS AAC has layer bits 00.
func isMP3Frame(b []byte) bool {
	return b[0] == 0xFF && b[1]&0xE0 == 0xE0 && b[1]&0x18 != 0x08 && b[1]&0x06 == 0x02
}
