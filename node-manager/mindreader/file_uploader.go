package mindreader

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/abourget/llerrgroup"
	"github.com/streamingfast/dstore"
	"github.com/streamingfast/shutter"
	"go.uber.org/zap"
)

type FileUploader struct {
	*shutter.Shutter
	mutex            sync.Mutex
	localStore       dstore.Store
	destinationStore dstore.Store
	logger           *zap.Logger
	complete         chan struct{}
}

func NewFileUploader(localStore dstore.Store, destinationStore dstore.Store, logger *zap.Logger) *FileUploader {
	return &FileUploader{
		Shutter:          shutter.New(),
		complete:         make(chan struct{}),
		localStore:       localStore,
		destinationStore: destinationStore,
		logger:           logger,
	}
}

// Done is closed once the upload loop has fully stopped, after a final upload
// pass has drained the remaining local files.
func (fu *FileUploader) Done() <-chan struct{} {
	return fu.complete
}

func (fu *FileUploader) Start(ctx context.Context) {
	defer close(fu.complete)

	fu.OnTerminating(func(_ error) {
		<-fu.complete
	})

	for !fu.IsTerminating() {
		err := fu.uploadFiles(ctx)
		if err != nil {
			fu.logger.Warn("failed to upload file", zap.Error(err))
		}

		select {
		case <-fu.Terminating():
		case <-time.After(500 * time.Millisecond):
		}
	}

	fu.logger.Info("terminating, running a final upload pass")

	// The context we received is most likely already canceled at this point (it
	// is tied to the mindreader plugin lifecycle), so we use a fresh bounded
	// context to give the final pass a chance to complete.
	finalCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := fu.uploadFiles(finalCtx); err != nil {
		fu.logger.Warn("failed to upload remaining files while terminating", zap.Error(err))
	}
}

func (fu *FileUploader) uploadFiles(ctx context.Context) error {
	fu.mutex.Lock()
	defer fu.mutex.Unlock()

	eg := llerrgroup.New(200)
	_ = fu.localStore.Walk(ctx, "", func(filename string) error {
		if eg.Stop() {
			return nil
		}
		eg.Go(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()

			if traceEnabled {
				fu.logger.Debug("uploading file to storage", zap.String("local_file", filename))
			}

			// Do not write the enclosing walk callback's return value from this
			// goroutine, it would be a data race with the other upload goroutines.
			if err := fu.destinationStore.PushLocalFile(ctx, fu.localStore.ObjectPath(filename), filename); err != nil {
				return fmt.Errorf("moving file %q to storage: %w", filename, err)
			}
			return nil
		})

		return nil
	})

	return eg.Wait()
}
