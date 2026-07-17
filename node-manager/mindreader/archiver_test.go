package mindreader

import (
	"context"
	"sync"
	"testing"

	"github.com/streamingfast/dstore"
	"github.com/stretchr/testify/assert"
)

// TestArchiver_ShutdownDrainsFileUploader ensures that shutting down the
// archiver triggers a final upload pass of the file uploader and waits for it,
// so that no one-block file remains stranded locally (GitHub issue #53). The
// final pass must succeed even though the context given to Start is already
// canceled, as it is in the real mindreader shutdown sequence.
func TestArchiver_ShutdownDrainsFileUploader(t *testing.T) {
	localStore := dstore.NewMockStore(nil)
	destStore := dstore.NewMockStore(nil)

	// The default MockStore.Walk ignores the context, make it context-aware so
	// this test properly simulates a canceled upload loop context.
	localStore.WalkFunc = func(ctx context.Context, prefix string, f func(filename string) error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for name := range localStore.Files {
			if err := f(name); err != nil {
				return err
			}
		}
		return nil
	}

	var mu sync.Mutex
	pushed := map[string]bool{}
	destStore.PushLocalFileFunc = func(_ context.Context, _, toBaseName string) error {
		mu.Lock()
		defer mu.Unlock()
		pushed[toBaseName] = true
		return nil
	}

	archiver := NewArchiver(1, "test", localStore, destStore, testLogger, testTracer)

	// Simulate the mindreader plugin lifecycle: the context wiring the upload
	// loop is canceled when the plugin starts terminating, before the archiver
	// itself gets shut down.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	archiver.Start(ctx)

	localStore.SetFile("0000000001-aaaa-bbbb-1-test", []byte{0x01})
	localStore.SetFile("0000000002-cccc-aaaa-1-test", []byte{0x02})

	archiver.Shutdown(nil)
	<-archiver.Terminated()

	// By the time the archiver is terminated, all local one-block files must
	// have been pushed to the destination store.
	mu.Lock()
	defer mu.Unlock()
	assert.True(t, pushed["0000000001-aaaa-bbbb-1-test"], "first one-block file was not uploaded before archiver termination")
	assert.True(t, pushed["0000000002-cccc-aaaa-1-test"], "second one-block file was not uploaded before archiver termination")
}
