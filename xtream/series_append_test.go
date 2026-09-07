package xtream

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendAndCompactSeriesFragment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frag-1.m3u")

	require.NoError(t, AppendSeriesFragment(path, []FragmentEntry{
		{UpstreamID: 1, Lines: []string{"#EXTINF:-1,Old One", "http://old/1"}},
		{UpstreamID: 2, Lines: []string{"#EXTINF:-1,Two", "http://two/1"}},
	}))
	require.NoError(t, AppendSeriesFragment(path, []FragmentEntry{
		{UpstreamID: 1, Lines: []string{"#EXTINF:-1,New One", "http://new/1"}},
	}))

	entries, err := ReadSeriesFragment(path)
	require.NoError(t, err)
	byID := map[uint64]FragmentEntry{}
	for _, e := range entries {
		byID[e.UpstreamID] = e
	}
	require.Equal(t, "http://new/1", byID[1].Lines[1])
	require.Len(t, byID, 2)

	require.NoError(t, CompactSeriesFragment(path, map[uint64]struct{}{1: {}}))
	entries, err = ReadSeriesFragment(path)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, uint64(1), entries[0].UpstreamID)
	require.Equal(t, "http://new/1", entries[0].Lines[1])

	require.NoError(t, CompactSeriesFragment(filepath.Join(t.TempDir(), "none.m3u"), nil))
}

func TestAppendSeriesFragmentMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dir", "frag.m3u")
	require.NoError(t, AppendSeriesFragment(path, []FragmentEntry{{UpstreamID: 9, Lines: []string{"l"}}}))
	_, err := os.Stat(path)
	require.NoError(t, err)
}
