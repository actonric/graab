package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// oggInfo is what WhatsApp needs to render a voice note: its length in
// seconds and a 64-sample amplitude sketch for the bubble's waveform.
type oggInfo struct {
	Seconds  uint32
	Waveform []byte
}

// analyzeOggOpus walks the Ogg pages of an Opus file to compute the duration
// from the final granule position and builds an approximate waveform from the
// per-page payload sizes (larger Opus packets mean louder audio, roughly).
func analyzeOggOpus(data []byte) (oggInfo, error) {
	if len(data) < 27 || string(data[:4]) != "OggS" {
		return oggInfo{}, errors.New("not an Ogg container (missing OggS signature)")
	}

	var (
		preSkip      uint16
		sampleRate   uint32 = 48000 // Opus granule positions are always at 48 kHz
		lastGranule  uint64
		foundHead    bool
		pageSizes    []int
		pageGranules []uint64
	)

	off := 0
	for off+27 <= len(data) {
		if string(data[off:off+4]) != "OggS" {
			// Resync: scan forward for the next capture pattern.
			off++
			continue
		}
		granule := binary.LittleEndian.Uint64(data[off+6 : off+14])
		nSegs := int(data[off+26])
		headerLen := 27 + nSegs
		if off+headerLen > len(data) {
			break
		}
		payloadLen := 0
		for i := 0; i < nSegs; i++ {
			payloadLen += int(data[off+27+i])
		}
		payloadStart := off + headerLen
		payloadEnd := payloadStart + payloadLen
		if payloadEnd > len(data) {
			payloadEnd = len(data)
		}
		payload := data[payloadStart:payloadEnd]

		if !foundHead && len(payload) >= 19 && string(payload[:8]) == "OpusHead" {
			foundHead = true
			preSkip = binary.LittleEndian.Uint16(payload[10:12])
			// Bytes 12..16 hold the original input sample rate; granules stay at 48 kHz.
		} else if foundHead && !(len(payload) >= 8 && string(payload[:8]) == "OpusTags") {
			pageSizes = append(pageSizes, payloadLen)
			pageGranules = append(pageGranules, granule)
		}
		if granule != ^uint64(0) && granule > lastGranule {
			lastGranule = granule
		}
		off = payloadEnd
	}

	if !foundHead {
		return oggInfo{}, errors.New("no OpusHead packet found; is this an Opus file?")
	}
	if lastGranule == 0 {
		return oggInfo{}, errors.New("could not determine duration (no granule positions)")
	}

	samples := lastGranule
	if uint64(preSkip) < samples {
		samples -= uint64(preSkip)
	}
	seconds := uint32((samples + uint64(sampleRate) - 1) / uint64(sampleRate))
	if seconds == 0 {
		seconds = 1
	}

	return oggInfo{Seconds: seconds, Waveform: buildWaveform(pageSizes, pageGranules, lastGranule)}, nil
}

// buildWaveform maps page payload sizes onto 64 buckets spread over the
// duration and scales them to 0..100 the way WhatsApp expects.
func buildWaveform(sizes []int, granules []uint64, total uint64) []byte {
	const buckets = 64
	wave := make([]byte, buckets)
	if len(sizes) == 0 || total == 0 {
		for i := range wave {
			wave[i] = 50
		}
		return wave
	}
	sums := make([]float64, buckets)
	counts := make([]int, buckets)
	for i, size := range sizes {
		b := int(granules[i] * buckets / total)
		if b >= buckets {
			b = buckets - 1
		}
		sums[b] += float64(size)
		counts[b]++
	}
	// Fill empty buckets by carrying the previous value forward.
	var max float64
	prev := 0.0
	for i := 0; i < buckets; i++ {
		if counts[i] > 0 {
			prev = sums[i] / float64(counts[i])
		}
		sums[i] = prev
		if prev > max {
			max = prev
		}
	}
	if max == 0 {
		max = 1
	}
	for i := 0; i < buckets; i++ {
		v := sums[i] / max * 100
		if v < 4 {
			v = 4
		}
		wave[i] = byte(v)
	}
	return wave
}

func (o oggInfo) String() string {
	return fmt.Sprintf("%ds, %d waveform samples", o.Seconds, len(o.Waveform))
}
