package buffer

import (
	"context"
	"sync"
	"time"
)

// PCR is the Program Clock Reference carried in MPEG-TS adaptation fields,
// expressed in 27 MHz ticks (90 kHz base * 300 extension).
const (
	pcrHz        = 27_000_000
	pcrWrapTicks = int64(1) << 33 * 300
)

const (
	pacerProbeBytes = 2 << 20
	pacerMaxLead    = 10 * time.Second
	pacerRateWindow = 8 * time.Second
	pacerSleepSlice = 500 * time.Millisecond
	pcrMaxJumpTicks = int64(60 * pcrHz)
	pacerMinSamples = 3
	pacerMaxWait    = 30 * time.Second
)

type pcrSample struct {
	offset int64
	cum    int64
}

// pcrPacer paces media writes to the stream's own program clock so a
// faster-than-realtime source cannot lap attached readers. The writer is
// allowed a bounded lead ahead of realtime, then settles to 1x, which keeps
// the ring holding seconds of content instead of pump-rate fragments.
type pcrPacer struct {
	mu sync.Mutex

	ringBytes int64

	offset  int64
	carry   []byte
	samples []pcrSample
	lastRaw int64

	anchor       *pcrSample
	anchorWall   time.Time
	anchorWallOn bool
	rate         float64

	found    bool
	disabled bool
}

func newPCRPacer(ringBytes int64) *pcrPacer {
	return &pcrPacer{ringBytes: ringBytes}
}

// pace scans b for PCR samples, then blocks until the end of b is due on
// the program clock (minus the allowed lead). It returns ctx.Err() if the
// context ends while waiting. Chunks must be fed in stream order.
func (p *pcrPacer) pace(ctx context.Context, b []byte) error {
	p.mu.Lock()
	if p.disabled {
		p.mu.Unlock()
		return nil
	}
	p.scan(b)
	wait := p.waitLocked(time.Now(), p.offset)
	p.mu.Unlock()

	for wait > 0 {
		s := min(wait, pacerSleepSlice)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s):
		}
		wait -= s
	}
	return nil
}

// scan parses 188-byte TS packets out of b (plus any carry from the
// previous call) and records PCR samples.
func (p *pcrPacer) scan(b []byte) {
	var buf []byte
	if len(p.carry) > 0 {
		buf = make([]byte, 0, len(p.carry)+len(b))
		buf = append(buf, p.carry...)
		buf = append(buf, b...)
	} else {
		buf = b
	}

	i := 0
	for {
		for i < len(buf) && buf[i] != 0x47 {
			i++
		}
		if len(buf)-i < 188 {
			break
		}
		if i+188 < len(buf) && buf[i+188] != 0x47 {
			i++
			continue
		}
		pkt := buf[i : i+188 : i+188]
		afc := (pkt[3] >> 4) & 0x3
		if afc == 2 || afc == 3 {
			if afLen := int(pkt[4]); afLen >= 7 && pkt[5]&0x10 != 0 {
				base := int64(pkt[6])<<25 | int64(pkt[7])<<17 | int64(pkt[8])<<9 |
					int64(pkt[9])<<1 | int64(pkt[10])>>7
				ext := int64(pkt[10]&0x1)<<8 | int64(pkt[11])
				p.addSample(p.offset+int64(i), base*300+ext)
			}
		}
		i += 188
	}

	p.offset += int64(i)
	if rest := buf[i:]; len(rest) > 0 {
		p.carry = append(p.carry[:0], rest...)
	} else {
		p.carry = p.carry[:0]
	}

	if !p.found && !p.disabled && p.offset > pacerProbeBytes {
		p.disabled = true
	}
}

func (p *pcrPacer) addSample(offset, ticks int64) {
	if !p.found {
		p.found = true
		p.lastRaw = ticks
		p.anchor = &pcrSample{offset: offset}
		p.anchorWallOn = false
		p.samples = append(p.samples[:0], *p.anchor)
		return
	}

	delta := ticks - p.lastRaw
	if delta < -pcrWrapTicks/2 {
		delta += pcrWrapTicks
	}
	p.lastRaw = ticks

	if delta < 0 || delta > pcrMaxJumpTicks {
		p.anchor = &pcrSample{offset: offset}
		p.anchorWallOn = false
		p.samples = append(p.samples[:0], *p.anchor)
		return
	}

	s := pcrSample{offset: offset, cum: p.samples[len(p.samples)-1].cum + delta}
	p.samples = append(p.samples, s)

	windowTicks := int64(pacerRateWindow.Seconds() * pcrHz)
	for len(p.samples) > 2 && s.cum-p.samples[0].cum > windowTicks {
		p.samples = p.samples[1:]
	}

	if len(p.samples) >= pacerMinSamples {
		first := p.samples[0]
		if s.cum > first.cum && s.offset > first.offset {
			p.rate = float64(s.offset-first.offset) /
				(float64(s.cum-first.cum) / pcrHz)
		}
	}
}

// waitLocked returns how long to wait before publishing bytes up to
// endOffset. A lead (bounded by ring capacity) lets the writer burst ahead
// of realtime so readers always have backlog to absorb jitter.
func (p *pcrPacer) waitLocked(now time.Time, endOffset int64) time.Duration {
	if !p.found || p.rate <= 0 {
		return 0
	}
	if !p.anchorWallOn {
		p.anchorWall = now
		p.anchorWallOn = true
		return 0
	}

	last := p.samples[len(p.samples)-1]
	if endOffset <= last.offset {
		return 0
	}
	cumAtEnd := last.cum + int64(float64(endOffset-last.offset)/p.rate*pcrHz)

	dueSecs := float64(cumAtEnd-p.anchor.cum) / pcrHz
	wait := time.Duration(dueSecs*float64(time.Second)) - now.Sub(p.anchorWall) - p.lead()
	if wait <= 0 {
		return 0
	}
	if wait > pacerMaxWait {
		p.anchor = &pcrSample{offset: endOffset, cum: cumAtEnd}
		p.anchorWall = now
		return 0
	}
	return wait
}

func (p *pcrPacer) lead() time.Duration {
	if p.rate <= 0 {
		return 0
	}
	ringDur := time.Duration(float64(p.ringBytes) / p.rate * float64(time.Second))
	return min(ringDur/2, pacerMaxLead)
}
