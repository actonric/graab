package main

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

// oggPage builds a single Ogg page (CRC left zero; the parser ignores it).
func oggPage(headerType byte, granule uint64, seq uint32, packets ...[]byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("OggS")
	buf.WriteByte(0)
	buf.WriteByte(headerType)
	_ = binary.Write(&buf, binary.LittleEndian, granule)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0xBEEF)) // serial
	_ = binary.Write(&buf, binary.LittleEndian, seq)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0)) // crc
	var segs []byte
	for _, p := range packets {
		n := len(p)
		for n >= 255 {
			segs = append(segs, 255)
			n -= 255
		}
		segs = append(segs, byte(n))
	}
	buf.WriteByte(byte(len(segs)))
	buf.Write(segs)
	for _, p := range packets {
		buf.Write(p)
	}
	return buf.Bytes()
}

func opusHead(preSkip uint16) []byte {
	var buf bytes.Buffer
	buf.WriteString("OpusHead")
	buf.WriteByte(1) // version
	buf.WriteByte(1) // channels
	_ = binary.Write(&buf, binary.LittleEndian, preSkip)
	_ = binary.Write(&buf, binary.LittleEndian, uint32(48000))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // gain
	buf.WriteByte(0)                                       // mapping family
	return buf.Bytes()
}

func opusTags() []byte {
	var buf bytes.Buffer
	buf.WriteString("OpusTags")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(0))
	return buf.Bytes()
}

// synthOgg builds an Opus stream of the given length in seconds with 20 ms
// frames, 50 frames per page.
func synthOgg(seconds int, preSkip uint16) []byte {
	rng := rand.New(rand.NewSource(1))
	var out []byte
	out = append(out, oggPage(2, 0, 0, opusHead(preSkip))...)
	out = append(out, oggPage(0, 0, 1, opusTags())...)
	seq := uint32(2)
	var granule uint64
	for s := 0; s < seconds; s++ {
		var packets [][]byte
		for f := 0; f < 50; f++ {
			p := make([]byte, 40+rng.Intn(80))
			rng.Read(p)
			packets = append(packets, p)
			granule += 960
		}
		flag := byte(0)
		if s == seconds-1 {
			flag = 4 // end of stream
		}
		out = append(out, oggPage(flag, granule, seq, packets...)...)
		seq++
	}
	return out
}

func TestAnalyzeOggOpus(t *testing.T) {
	data := synthOgg(3, 312)
	info, err := analyzeOggOpus(data)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if info.Seconds != 3 {
		t.Errorf("seconds = %d, want 3", info.Seconds)
	}
	if len(info.Waveform) != 64 {
		t.Errorf("waveform len = %d, want 64", len(info.Waveform))
	}
	var max byte
	for _, v := range info.Waveform {
		if v < 4 || v > 100 {
			t.Errorf("waveform sample %d out of range", v)
		}
		if v > max {
			max = v
		}
	}
	if max != 100 {
		t.Errorf("waveform should be normalised to 100, max = %d", max)
	}
}

func TestAnalyzeOggOpusRejectsGarbage(t *testing.T) {
	if _, err := analyzeOggOpus([]byte("not an ogg file at all, definitely")); err == nil {
		t.Errorf("expected error for non-Ogg data")
	}
	// Valid Ogg container but not Opus (no OpusHead).
	data := oggPage(2, 0, 0, []byte("vorbis-ish"))
	if _, err := analyzeOggOpus(data); err == nil {
		t.Errorf("expected error for Ogg without OpusHead")
	}
	if _, err := analyzeOggOpus(nil); err == nil {
		t.Errorf("expected error for empty input")
	}
}
