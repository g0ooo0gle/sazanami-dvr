package mirakurun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/g0ooo0gle/sazanami-dvr/internal/core/provider"
)

var errBodyOverLimit = errors.New("mirakurun: response body over limit")

type cappedReader struct {
	reader io.Reader
	limit  int64
	read   int64
}

// Readは上限まで元の応答を読み、上限を1 byteでも超えた時点で固定errorを返す。
func (reader *cappedReader) Read(buffer []byte) (int, error) {
	if reader.read >= reader.limit {
		var one [1]byte
		count, err := reader.reader.Read(one[:])
		if count != 0 {
			return 0, errBodyOverLimit
		}
		return 0, err
	}
	remaining := reader.limit - reader.read
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	count, err := reader.reader.Read(buffer)
	reader.read += int64(count)
	return count, err
}

type responseOperation struct {
	ctx     context.Context
	cancel  context.CancelFunc
	release func()
	body    io.ReadCloser
	decoder *json.Decoder

	closeOnce sync.Once
	closeErr  error
}

func newResponseOperation(ctx context.Context, cancel context.CancelFunc, release func(), body io.ReadCloser, limit int64) *responseOperation {
	decoder := json.NewDecoder(&cappedReader{reader: body, limit: limit})
	decoder.UseNumber()
	return &responseOperation{ctx: ctx, cancel: cancel, release: release, body: body, decoder: decoder}
}

func (operation *responseOperation) beginArray() error {
	token, err := operation.decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '[' {
		return provider.NewFailure(provider.ReasonMalformed, "top-level-array-required")
	}
	return nil
}

func (operation *responseOperation) finishArray() error {
	token, err := operation.decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != ']' {
		return provider.NewFailure(provider.ReasonMalformed, "array-end-required")
	}
	return operation.finishDocument()
}

func (operation *responseOperation) finishDocument() error {
	_, err := operation.decoder.Token()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return provider.NewFailure(provider.ReasonMalformed, "trailing-json-token")
	}
	return err
}

func (operation *responseOperation) failure(err error) error {
	failure := classifyDecodeFailure(operation.ctx, err)
	_ = operation.close()
	return failure
}

func (operation *responseOperation) close() error {
	if operation == nil {
		return nil
	}
	operation.closeOnce.Do(func() {
		operation.cancel()
		if operation.body != nil {
			if err := operation.body.Close(); err != nil {
				operation.closeErr = provider.NewFailure(provider.ReasonUnavailable, "response-body-close-failed")
			}
		}
		operation.release()
	})
	return operation.closeErr
}

func classifyDecodeFailure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var failure *provider.Failure
	if errors.As(err, &failure) {
		return failure
	}
	if contextFailure := provider.ContextFailure(ctx); contextFailure != nil {
		return contextFailure
	}
	if errors.Is(err, errBodyOverLimit) {
		return provider.NewFailure(provider.ReasonOverLimit, "response-body-over-limit")
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return provider.NewFailure(provider.ReasonEarlyEOF, "truncated-json")
	}
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &syntaxError) || errors.As(err, &typeError) {
		return provider.NewFailure(provider.ReasonMalformed, "invalid-json")
	}
	if errors.Is(err, http.ErrBodyReadAfterClose) {
		return provider.NewFailure(provider.ReasonCancelled, "response-body-closed")
	}
	return provider.NewFailure(provider.ReasonUnavailable, "response-read-failed")
}
