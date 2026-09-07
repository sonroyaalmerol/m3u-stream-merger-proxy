package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCgroupMemoryLimit(t *testing.T) {
	dir := t.TempDir()

	v2 := filepath.Join(dir, "memory.max")
	require.NoError(t, os.WriteFile(v2, []byte("536870912\n"), 0o644))
	got, ok := cgroupMemoryLimitAt(v2, filepath.Join(dir, "nope", "memory.limit_in_bytes"))
	require.True(t, ok)
	require.Equal(t, int64(536870912), got)

	require.NoError(t, os.WriteFile(v2, []byte("max\n"), 0o644))
	_, ok = cgroupMemoryLimitAt(v2, filepath.Join(dir, "nope"))
	require.False(t, ok)

	v1 := filepath.Join(dir, "memory", "memory.limit_in_bytes")
	require.NoError(t, os.MkdirAll(filepath.Dir(v1), 0o755))
	require.NoError(t, os.WriteFile(v1, []byte("536870912"), 0o644))
	got, ok = cgroupMemoryLimitAt(filepath.Join(dir, "absent"), v1)
	require.True(t, ok)
	require.Equal(t, int64(536870912), got)
}
