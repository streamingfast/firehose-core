package mindreader

import (
	"context"
	"testing"
	"time"

	"github.com/streamingfast/dstore"
	"github.com/streamingfast/firehose-core/node-manager/metrics"
	"github.com/stretchr/testify/require"
)

func TestFileUploader(t *testing.T) {
	localStore := dstore.NewMockStore(nil)
	localStore.SetFile("test1", nil)
	localStore.SetFile("test2", nil)
	localStore.SetFile("test3", nil)

	destinationStore := dstore.NewMockStore(nil)

	done := make(chan interface{})
	out := make(chan bool, 3)

	destinationStore.PushLocalFileFunc = func(_ context.Context, _, _ string) (err error) {
		out <- true
		return nil
	}
	go func() {
		for i := 0; i < 3; i++ {
			<-out
		}
		close(done)
	}()

	uploader := NewFileUploader(localStore, destinationStore, testLogger)
	err := uploader.uploadFiles(context.Background())
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("took took long")
	}
}

func TestFileUploader_BacklogMetrics(t *testing.T) {
	now := time.Now()
	entries := []dstore.ObjectEntry{
		{Name: "test1", LastModified: now.Add(-10 * time.Second)},
		{Name: "test2", LastModified: now.Add(-30 * time.Second)},
		{Name: "test3", LastModified: now.Add(-5 * time.Second)},
	}

	localStore := dstore.NewMockStore(nil)
	localStore.WalkAttributesFunc = func(_ context.Context, _ string, f func(entry dstore.ObjectEntry) error) error {
		for _, entry := range entries {
			if err := f(entry); err != nil {
				return err
			}
		}
		return nil
	}

	destinationStore := dstore.NewMockStore(nil)
	destinationStore.PushLocalFileFunc = func(_ context.Context, _, _ string) error {
		return nil
	}

	uploader := NewFileUploader(localStore, destinationStore, testLogger)
	require.NoError(t, uploader.uploadFiles(context.Background()))

	require.Equal(t, float64(3), metrics.OneBlockUploadPendingFiles.Get())
	require.InDelta(t, 30, metrics.OneBlockUploadOldestPendingSeconds.Get(), 5)

	entries = nil
	require.NoError(t, uploader.uploadFiles(context.Background()))

	require.Equal(t, float64(0), metrics.OneBlockUploadPendingFiles.Get())
	require.Equal(t, float64(0), metrics.OneBlockUploadOldestPendingSeconds.Get())
}
