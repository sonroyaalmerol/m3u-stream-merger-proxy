package stream

import "testing"

func TestAlignToPayloadStart(t *testing.T) {
	pkt := func(pusi byte) []byte {
		p := make([]byte, 188)
		p[0] = 0x47
		p[1] = pusi
		return p
	}
	data := append([]byte{0x00, 0x01, 0x02}, pkt(0)...)
	data = append(data, pkt(0x40)...)
	data = append(data, pkt(0)...)

	got := alignToPayloadStart(data)
	if len(got) != len(data)-3-188 {
		t.Fatalf("aligned at %d bytes, want %d", len(got), len(data)-3-188)
	}
	if got[1]&0x40 == 0 {
		t.Fatal("aligned data does not start at a PUSI packet")
	}

	if r := alignToPayloadStart(pkt(0x40)); len(r) != 188 {
		t.Fatal("already-aligned data must be unchanged")
	}
	if r := alignToPayloadStart(make([]byte, 200)); len(r) != 200 {
		t.Fatal("garbage input must be returned unchanged")
	}
}
