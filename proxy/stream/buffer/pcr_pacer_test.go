package buffer

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// tsPacket builds a 188-byte TS packet with an adaptation field carrying
// pcr (27 MHz ticks) when withPCR is set, else a plain PES payload packet.
func tsPacket(withPCR bool, pcr int64, pusi bool) []byte {
	p := make([]byte, 188)
	p[0] = 0x47
	if pusi {
		p[1] |= 0x40
	}
	p[1] |= 0x1f & byte(0x10>>8)
	p[3] = 0x30
	var afLen byte
	if withPCR {
		afLen = 7
	}
	p[4] = afLen
	if withPCR {
		p[5] = 0x10
		base := pcr / 300
		ext := pcr % 300
		p[6] = byte(base >> 25)
		p[7] = byte(base >> 17)
		p[8] = byte(base >> 9)
		p[9] = byte(base >> 1)
		p[10] = byte(base&1)<<7 | byte(ext>>8)
		p[11] = byte(ext)
	}
	return p
}

func buildStream(n int, ticksPerPacket int64, startTicks int64) []byte {
	var buf bytes.Buffer
	ticks := startTicks
	for i := range n {
		if i%2 == 0 {
			buf.Write(tsPacket(true, ticks, i%10 == 0))
		} else {
			buf.Write(tsPacket(false, 0, i%10 == 0))
		}
		ticks += ticksPerPacket
	}
	return buf.Bytes()
}

func TestPacerFailsOpenWithoutPCR(t *testing.T) {
	p := newPCRPacer(8 << 20)
	data := bytes.Repeat([]byte{0xAA}, pacerProbeBytes+1024)
	if err := p.pace(context.Background(), data); err != nil {
		t.Fatalf("pace: %v", err)
	}
	if !p.disabled {
		t.Fatal("expected pacer to disable on garbage")
	}
}

func TestPacerExtractsPCRAcrossChunkSplits(t *testing.T) {
	stream := buildStream(50, 270000, 0)
	p := newPCRPacer(8 << 20)
	for i := 0; i < len(stream); i += 137 {
		end := min(i+137, len(stream))
		if err := p.pace(context.Background(), stream[i:end]); err != nil {
			t.Fatalf("pace: %v", err)
		}
		if p.disabled {
			t.Fatal("disabled unexpectedly")
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.samples) < pacerMinSamples {
		t.Fatalf("expected >= %d samples, got %d", pacerMinSamples, len(p.samples))
	}
	wantRate := float64(188) / (float64(270000) / pcrHz)
	if p.rate == 0 || p.rate > wantRate*1.01 || p.rate < wantRate*0.99 {
		t.Fatalf("rate estimate %v, want ~%v", p.rate, wantRate)
	}
	if p.offset != int64(len(stream)) {
		t.Fatalf("offset %d, want %d", p.offset, len(stream))
	}
}

func TestPacerAllowsInitialBurstThenPaces(t *testing.T) {
	// 300 packets * 10ms = 3s of content; ring 100KB at ~18.8KB/s gives
	// a ~2.7s lead.
	stream := buildStream(300, 270000, 0)
	p := newPCRPacer(100 << 10)
	start := time.Now()
	if err := p.pace(context.Background(), stream); err != nil {
		t.Fatalf("pace: %v", err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("initial burst slept unexpectedly")
	}

	// A further second of content is past the lead: expect ~1.3s of wait.
	more := buildStream(100, 270000, 270000*300)
	p.mu.Lock()
	wait := p.waitLocked(time.Now(), p.offset+int64(len(more)))
	p.mu.Unlock()
	if wait <= 0 || wait > 2*time.Second {
		t.Fatalf("wait %v, want ~1.3s", wait)
	}
}

func TestPacerWraparound(t *testing.T) {
	wrapped := pcrWrapTicks - 2700000
	stream := buildStream(20, 270000, wrapped)
	p := newPCRPacer(8 << 20)
	p.mu.Lock()
	p.scan(stream)
	if len(p.samples) < 3 {
		t.Fatalf("samples: %d", len(p.samples))
	}
	last := p.samples[len(p.samples)-1]
	if last.cum <= 0 {
		t.Fatalf("cumulative ticks %d should be positive across wrap", last.cum)
	}
	p.mu.Unlock()
}

func TestPacerDiscontinuityReanchors(t *testing.T) {
	stream := buildStream(10, 270000, 0)
	p := newPCRPacer(8 << 20)
	p.mu.Lock()
	p.scan(stream)
	anchorBefore := p.anchor
	jump := buildStream(10, 270000, 300*pcrHz)
	p.scan(jump)
	if p.anchor == anchorBefore {
		t.Fatal("expected re-anchor on PCR discontinuity")
	}
	p.mu.Unlock()
}

func TestPacerContextCancelDuringWait(t *testing.T) {
	p := newPCRPacer(100 << 10)
	p.mu.Lock()
	p.found = true
	p.rate = 18800
	p.anchor = &pcrSample{offset: 0}
	p.anchorWall = time.Now()
	p.anchorWallOn = true
	p.samples = []pcrSample{{offset: 0}, {offset: 188 * 100, cum: 30 * pcrHz}}
	p.offset = 188 * 100
	p.mu.Unlock()

	p.mu.Lock()
	wait := p.waitLocked(time.Now(), p.offset+188)
	p.mu.Unlock()
	if wait <= 0 {
		t.Fatal("expected long wait")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.pace(ctx, tsPacket(true, 0, false)); err == nil {
		t.Fatal("expected ctx.Err on canceled context")
	}
}
