package xtream

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeriesCacheCodecs(t *testing.T) {
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "stubs.bin")

	stubs := []SeriesStub{
		{UpstreamID: 300, Name: "Lazy Show", Group: "Drama", Cover: "http://img/s.png"},
		{UpstreamID: 1, Name: "A", Group: "", Cover: ""},
	}
	require.NoError(t, WriteSeriesStubs(stubPath, stubs))
	back, err := ReadSeriesStubs(stubPath)
	require.NoError(t, err)
	require.Len(t, back, 2)
	assert.Equal(t, stubs[0], back[0])
	assert.Equal(t, stubs[1], back[1])

	raw, err := os.ReadFile(stubPath)
	require.NoError(t, err)
	bad := append([]byte{}, raw...)
	bad[0] = 'X'
	bad[1] = 'Y'
	require.NoError(t, os.WriteFile(stubPath, bad, 0644))
	_, err = ReadSeriesStubs(stubPath)
	assert.Error(t, err, "bad magic must be rejected")

	require.NoError(t, os.WriteFile(stubPath, raw[:len(raw)-4], 0644))
	_, err = ReadSeriesStubs(stubPath)
	assert.Error(t, err, "truncated stub must be rejected")

	fragPath := filepath.Join(dir, "frag.m3u")
	entries := []FragmentEntry{
		{UpstreamID: 300, Lines: []string{`#EXTINF:-1 tvg-name="Lazy Show S1E2",Lazy Show S1E2`, "http://up/series/u/p/301.mkv"}},
		{UpstreamID: 301, Lines: nil},
	}
	require.NoError(t, WriteSeriesFragment(fragPath, entries))
	parsed, err := ReadSeriesFragment(fragPath)
	require.NoError(t, err)
	require.Len(t, parsed, 2)
	assert.Equal(t, uint64(300), parsed[0].UpstreamID)
	require.Len(t, parsed[0].Lines, 2)
	assert.Empty(t, parsed[1].Lines)

	require.NoError(t, os.WriteFile(fragPath, []byte("#XSERIES nope\n"), 0644))
	_, err = ReadSeriesFragment(fragPath)
	assert.Error(t, err, "bad fragment header must be rejected")

	require.NoError(t, CompactSeriesFragment(fragPath, nil))
	healed, err := ReadSeriesFragment(fragPath)
	require.NoError(t, err)
	assert.Empty(t, healed, "compact rewrites the corrupt file as a valid empty fragment")
}

func TestReplaySeriesFragmentUsesLastVersionInStubOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frag.m3u")
	require.NoError(t, WriteSeriesFragment(path, []FragmentEntry{
		{UpstreamID: 1, Lines: []string{"old-1"}},
		{UpstreamID: 2, Lines: []string{"new-2-a", "new-2-b"}},
		{UpstreamID: 1, Lines: []string{"new-1"}},
		{UpstreamID: 3, Lines: []string{"stale-3"}},
	}))

	var lines []string
	n, err := replaySeriesFragment(path, []SeriesStub{{UpstreamID: 2}, {UpstreamID: 1}}, func(line string) error {
		lines = append(lines, line)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, []string{"new-2-a", "new-2-b", "new-1"}, lines)
}
